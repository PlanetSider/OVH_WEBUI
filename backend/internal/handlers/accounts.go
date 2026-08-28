package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	ovhsdk "github.com/ovh/go-ovh/ovh"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/db"
	"github.com/ovh-webui/server/internal/types"
)

// ── 输入 / 输出 DTO ────────────────────────────────────────────────────────

// accountInput POST/PUT body
type accountInput struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"` // 可空,会按 zone 推断
	Zone        string `json:"zone"`
	AppKey      string `json:"appKey"`
	AppSecret   string `json:"appSecret"`
	ConsumerKey string `json:"consumerKey"`
	IAM         string `json:"iam"`      // 可空,会自动生成 go-ovh-<zone>
	SetDefault  bool   `json:"setDefault"`
}

// endpointForZone 根据 zone 推 endpoint
func endpointForZone(zone string) string {
	switch strings.ToUpper(zone) {
	case "US":
		return "ovh-us"
	case "CA", "QC", "ASIA", "SG", "AU", "IN":
		return "ovh-ca"
	default:
		return "ovh-eu"
	}
}

// fillDerived 补全 Endpoint / IAM
func (in *accountInput) normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Zone = strings.ToUpper(strings.TrimSpace(in.Zone))
	in.AppKey = strings.TrimSpace(in.AppKey)
	in.AppSecret = strings.TrimSpace(in.AppSecret)
	in.ConsumerKey = strings.TrimSpace(in.ConsumerKey)
	in.IAM = strings.TrimSpace(in.IAM)
	if in.Zone == "" {
		in.Zone = "IE"
	}
	if in.Endpoint == "" {
		in.Endpoint = endpointForZone(in.Zone)
	}
	if in.IAM == "" {
		in.IAM = "go-ovh-" + strings.ToLower(in.Zone)
	}
}

// normalizeUpdate 只清理更新请求，不把缺省 zone 当成 IE。PUT 支持部分
// 更新时，空 zone 应保留数据库里的现值，而不是无意中改区并重建 endpoint。
func (in *accountInput) normalizeUpdate() {
	in.Name = strings.TrimSpace(in.Name)
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	in.Zone = strings.ToUpper(strings.TrimSpace(in.Zone))
	in.AppKey = strings.TrimSpace(in.AppKey)
	in.AppSecret = strings.TrimSpace(in.AppSecret)
	in.ConsumerKey = strings.TrimSpace(in.ConsumerKey)
	in.IAM = strings.TrimSpace(in.IAM)
	if in.Zone != "" {
		if in.Endpoint == "" {
			in.Endpoint = endpointForZone(in.Zone)
		}
		if in.IAM == "" {
			in.IAM = "go-ovh-" + strings.ToLower(in.Zone)
		}
	}
}

func (in *accountInput) validate() string {
	if in.Name == "" {
		return "缺少 name"
	}
	if in.AppKey == "" || in.AppSecret == "" || in.ConsumerKey == "" {
		return "缺少 OVH 凭据 (appKey / appSecret / consumerKey)"
	}
	return ""
}

// ── handlers ───────────────────────────────────────────────────────────────

// ListAccounts GET /api/accounts
func ListAccounts(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		accs, err := state.DB.ListAccounts()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if accs == nil {
			accs = []types.OVHAccount{}
		}
		c.JSON(http.StatusOK, gin.H{"accounts": accs, "total": len(accs)})
	}
}

// GetAccountByID GET /api/accounts/:id
func GetAccountByID(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		acc, ok, err := state.DB.GetAccount(id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "账户不存在"})
			return
		}
		c.JSON(http.StatusOK, acc)
	}
}

// CreateAccount POST /api/accounts
// 先用新凭据调 OVH /me 验证，验证通过后才写入并发布账户。
func CreateAccount(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in accountInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		in.normalize()
		if msg := in.validate(); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}

		acc := types.OVHAccount{
			ID:          uuid.NewString(),
			Name:        in.Name,
			Endpoint:    in.Endpoint,
			Zone:        in.Zone,
			AppKey:      in.AppKey,
			AppSecret:   in.AppSecret,
			ConsumerKey: in.ConsumerKey,
			IAM:         in.IAM,
			CreatedAt:   types.NowISO(),
		}
		// 新账户在凭据验证完成前不能发布到数据库和运行内存。否则验证
		// 网络请求期间，队列可能已经用这个账户启动购买；一旦验证失败，
		// 随后的级联回滚会和购买流程并发，造成任务或审计记录被删除。
		if !verifyOVHAccount(state, acc) {
			state.Logger.Warn("创建账户校验失败，未写入账户: "+acc.Name+" ("+acc.Zone+")", "accounts")
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "OVH 凭据校验失败，账户未保存",
				"valid": false,
				"account": nil,
			})
			return
		}
		// 创建、首账户默认判定和内存重载必须处于同一账户写入临界区。
		// 否则两个并发创建都可能看到 count=0，或旧重载覆盖新账户。
		var previousDefault types.OVHAccount
		var hadPreviousDefault bool
		if err := state.WithAccountMutationRollback(func() error {
			count, err := state.DB.CountAccounts()
			if err != nil {
				return err
			}
			previousDefault, hadPreviousDefault, err = state.DB.GetDefaultAccount()
			if err != nil {
				return err
			}
			// 历史数据若已有账户却缺少默认标记，新建账户时顺便修复。
			acc.IsDefault = in.SetDefault || count == 0 || !hadPreviousDefault
			if err := state.DB.UpsertAccount(acc); err != nil {
				return err
			}
			return nil
		}, func() error {
			deleteErr := state.DB.DeleteAccount(acc.ID)
			restoreErr := restoreDefaultAccount(state, previousDefault, hadPreviousDefault)
			return errors.Join(deleteErr, restoreErr)
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		state.Logger.Info("创建账户: "+acc.Name+" ("+acc.Zone+") valid=true", "accounts")

		c.JSON(http.StatusOK, gin.H{"account": acc, "valid": true})
	}
}

// UpdateAccount PUT /api/accounts/:id
func UpdateAccount(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var in accountInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		in.normalizeUpdate()
		accountNotFound := errors.New("账户不存在")
		accountChanged := errors.New("账户已被其他请求修改")

		// 先读取并合并候选值。涉及 OVH 客户端连接参数的变更必须在写库前
		// 验证；验证网络请求不持有全局账户/checkout 锁，因此不会阻塞其他
		// 账户的抢购。提交时会重新读取并比较快照，防止验证期间的并发更新
		// 被本请求覆盖。
		existing, ok, err := state.DB.GetAccount(id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "账户不存在"})
			return
		}
		acc := mergeAccountUpdate(existing, in)
		credentialsVerified := false
		if accountClientConfigChanged(existing, acc) {
			if !verifyOVHAccount(state, acc) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error":   "OVH 凭据校验失败，账户未更新",
					"valid":   false,
					"account": nil,
				})
				return
			}
			credentialsVerified = true
		}

		var previousDefault types.OVHAccount
		var hadPreviousDefault bool
		err = state.WithAccountCheckoutGuard(id, func() error {
			// 在账户闸门内重新读取，避免请求开始时的旧快照覆盖另一个
			// 已提交的更新。购买流程也在同一闸门登记，不能在此期间
			// 使用旧凭据继续 checkout。
			current, ok, err := state.DB.GetAccount(id)
			if err != nil {
				return err
			}
			if !ok {
				return accountNotFound
			}
			if !sameAccountSnapshot(current, existing) {
				return accountChanged
			}
			previousDefault, hadPreviousDefault, err = state.DB.GetDefaultAccount()
			if err != nil {
				return err
			}
			if err := state.DB.UpsertAccount(acc); err != nil {
				return err
			}
			if err := state.ReloadAccounts(); err != nil {
				rollbackErr := state.DB.UpsertAccount(existing)
				restoreDefaultErr := restoreDefaultAccount(state, previousDefault, hadPreviousDefault)
				if rollbackErr = errors.Join(rollbackErr, restoreDefaultErr); rollbackErr != nil {
					return fmt.Errorf("账户已更新，但刷新运行状态失败: %w；回滚也失败: %v", err, rollbackErr)
				}
				if restoreErr := state.ReloadAccounts(); restoreErr != nil {
					return fmt.Errorf("账户更新后的运行状态刷新失败: %w；数据库已回滚，但恢复运行状态也失败: %v", err, restoreErr)
				}
				return fmt.Errorf("账户更新后的运行状态刷新失败，数据库变更已回滚: %w", err)
			}
			state.OVH.Invalidate(acc.ID)
			return nil
		})
		if errors.Is(err, accountNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "账户不存在"})
			return
		}
		if errors.Is(err, accountChanged) {
			c.JSON(http.StatusConflict, gin.H{"error": "账户已被其他请求修改，请刷新后重试"})
			return
		}
		if errors.Is(err, app.ErrQueueCheckoutInProgress) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// 客户端参数有变更时已经在保存前验证；名称、默认标记等元数据
		// 更新沿用原接口行为，在保存后返回当前凭据的实时验证结果。
		valid := credentialsVerified || verifyOVHAccount(state, acc)
		c.JSON(http.StatusOK, gin.H{"account": acc, "valid": valid})
	}
}

// DeleteAccountByID DELETE /api/accounts/:id  级联删除
// mon 可为 nil（测试）；真实 Monitor 在监控持久化临界区内完成删除与订阅重载。
// 保留 interface{} 参数以兼容只提供 LoadFromDB 的历史测试替身。
func DeleteAccountByID(state *app.State, mon interface{}) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		deleted := false
		var reloadErr error
		deleteAccount := func() error {
			if err := state.DB.DeleteAccount(id); err != nil {
				return err
			}
			deleted = true
			state.OVH.Invalidate(id)
			// 数据库删除已经提交，随后尽量从数据库重载内存。任何重载失败时，
			// reloadAfterAccountDelete 都会至少按 account_id 保守清理旧内存，
			// 防止已删除账户的旧任务重新写回数据库。
			reloadErr = reloadAfterAccountDelete(state, id)
			return nil
		}
		// Monitor 检查与自动补货持有 persistMu。统一临界区使账户删除先等待
		// 当前检查结束，再按 persistMu → checkoutMu 的顺序删除数据库记录并
		// 重载订阅，防止旧订阅在删除后继续入队。
		var err error
		if guarded, ok := mon.(interface { WithPersistenceGuard(func() error) error }); ok {
			err = guarded.WithPersistenceGuard(func() error {
				return state.WithAccountCheckoutGuard(id, deleteAccount)
			})
		} else {
			err = state.WithAccountCheckoutGuard(id, deleteAccount)
		}
		if err != nil {
			if errors.Is(err, app.ErrQueueCheckoutInProgress) || errors.Is(err, db.ErrUnresolvedCheckoutAttempts) {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			if errors.Is(err, db.ErrAccountNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "账户不存在"})
				return
			}
			// WithPersistenceGuard 会在数据库删除提交后重载监控快照。若这一步
			// 失败，删除本身仍已成功；返回明确的成功状态和安全降级警告，
			// 避免客户端误以为账户仍存在而反复删除。
			if deleted {
				state.SetQueueProcessorEnabled(false)
				if stopper, ok := mon.(interface{ Stop() bool }); ok {
					stopper.Stop()
				}
				state.Logger.Error("账户已删除，但监控运行状态重载失败: "+err.Error(), "accounts")
				c.JSON(http.StatusOK, gin.H{
					"status":  "success",
					"warning": "账户已删除，但部分运行状态同步失败；抢购与监控已安全停用，请重启服务并检查数据库",
				})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if loader, ok := mon.(interface{ LoadFromDB() }); ok {
			if _, guarded := mon.(interface { WithPersistenceGuard(func() error) error }); !guarded {
				loader.LoadFromDB()
			}
		}
		if reloadErr != nil {
			state.SetQueueProcessorEnabled(false)
			state.Logger.Error("账户已删除，但关联运行状态重载失败: "+reloadErr.Error(), "accounts")
			c.JSON(http.StatusOK, gin.H{
				"status":  "success",
				"warning": "账户已删除，但部分运行状态同步失败；抢购已安全停用，请重启服务并检查数据库",
			})
			return
		}
		state.Logger.Info("删除账户 + 级联清理: "+id, "accounts")
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	}
}

// SetDefaultAccountByID POST /api/accounts/:id/set-default
func SetDefaultAccountByID(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		accountNotFound := errors.New("账户不存在")
		var previousDefault types.OVHAccount
		var hadPreviousDefault bool
		err := state.WithAccountMutationRollback(func() error {
			_, ok, err := state.DB.GetAccount(id)
			if err != nil {
				return err
			} else if !ok {
				return accountNotFound
			}
			previousDefault, hadPreviousDefault, err = state.DB.GetDefaultAccount()
			if err != nil {
				return err
			}
			if err := state.DB.SetDefaultAccount(id); err != nil {
				return err
			}
			return nil
		}, func() error {
			return restoreDefaultAccount(state, previousDefault, hadPreviousDefault)
		})
		if errors.Is(err, accountNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "账户不存在"})
			return
		}
		if err != nil {
			if errors.Is(err, db.ErrAccountNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "账户不存在"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	}
}

// VerifyAccount POST /api/accounts/:id/verify
func VerifyAccount(state *app.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		c.JSON(http.StatusOK, gin.H{"valid": verifyAccountCreds(state, id)})
	}
}

// ── 内部工具 ───────────────────────────────────────────────────────────────

func mergeAccountUpdate(existing types.OVHAccount, in accountInput) types.OVHAccount {
	if in.Name != "" {
		existing.Name = in.Name
	}
	if in.Zone != "" {
		existing.Zone = in.Zone
	}
	if in.Endpoint != "" {
		existing.Endpoint = in.Endpoint
	}
	if in.IAM != "" {
		existing.IAM = in.IAM
	}
	if in.AppKey != "" {
		existing.AppKey = in.AppKey
	}
	if in.AppSecret != "" {
		existing.AppSecret = in.AppSecret
	}
	if in.ConsumerKey != "" {
		existing.ConsumerKey = in.ConsumerKey
	}
	existing.IsDefault = existing.IsDefault || in.SetDefault
	return existing
}

// accountClientConfigChanged 判断是否需要在保存前重新验证 OVH /me。Zone 和
// IAM 不直接参与 SDK 客户端鉴权；Zone 变更时 normalizeUpdate 已同步派生 Endpoint。
func accountClientConfigChanged(before, after types.OVHAccount) bool {
	return before.Endpoint != after.Endpoint ||
		before.AppKey != after.AppKey ||
		before.AppSecret != after.AppSecret ||
		before.ConsumerKey != after.ConsumerKey
}

// sameAccountSnapshot 是账户更新的轻量 CAS。OVHAccount 当前全部字段均为可比较
// 标量，逐字段比较能避免引入反射，也会在未来新增字段时提醒维护者同步更新。
func sameAccountSnapshot(a, b types.OVHAccount) bool {
	return a.ID == b.ID &&
		a.Name == b.Name &&
		a.Endpoint == b.Endpoint &&
		a.Zone == b.Zone &&
		a.AppKey == b.AppKey &&
		a.AppSecret == b.AppSecret &&
		a.ConsumerKey == b.ConsumerKey &&
		a.IAM == b.IAM &&
		a.IsDefault == b.IsDefault &&
		a.CreatedAt == b.CreatedAt
}

func restoreDefaultAccount(state *app.State, previous types.OVHAccount, hadPrevious bool) error {
	if hadPrevious {
		return state.DB.SetDefaultAccount(previous.ID)
	}
	return state.DB.ClearDefaultAccount()
}

func verifyOVHAccount(state *app.State, account types.OVHAccount) bool {
	if strings.TrimSpace(account.AppKey) == "" || strings.TrimSpace(account.AppSecret) == "" ||
		strings.TrimSpace(account.ConsumerKey) == "" {
		return false
	}
	client, err := ovhsdk.NewClient(account.Endpoint, account.AppKey, account.AppSecret, account.ConsumerKey)
	if err != nil {
		state.Logger.Warn("verify account "+account.ID+": "+err.Error(), "accounts")
		return false
	}
	client.Timeout = 30 * time.Second
	var me map[string]interface{}
	if err := client.Get("/me", &me); err != nil {
		state.Logger.Warn("verify account "+account.ID+": "+err.Error(), "accounts")
		return false
	}
	return true
}

// verifyAccountCreds 用账户凭据调 OVH /me 验证有效
func verifyAccountCreds(state *app.State, accountID string) bool {
	account, ok := state.FindAccount(accountID)
	if !ok {
		return false
	}
	return verifyOVHAccount(state, account)
}

// reloadAfterAccountDelete 在数据库级联删除后刷新关联内存。读取失败时不会保留
// 已删除账户的数据，而是按 account_id 对旧快照做保守清理并返回错误。调用方会
// 停用队列处理器，避免数据库故障期间继续处理不完整状态。
func reloadAfterAccountDelete(state *app.State, accountID string) error {
	var reloadErrors []error
	if err := state.ReloadAccounts(); err != nil {
		reloadErrors = append(reloadErrors, fmt.Errorf("reload accounts: %w", err))
		state.AccountsMu.Lock()
		kept := make([]types.OVHAccount, 0, len(state.Accounts))
		for _, account := range state.Accounts {
			if account.ID != accountID {
				kept = append(kept, account)
			}
		}
		state.Accounts = kept
		state.AccountsMu.Unlock()
		state.OVH.InvalidateAll()
	}

	if items, err := state.DB.ListQueue(); err == nil {
		state.QueueMu.Lock()
		state.Queue = items
		if state.Queue == nil {
			state.Queue = []types.QueueItem{}
		}
		state.QueueMu.Unlock()
	} else {
		reloadErrors = append(reloadErrors, fmt.Errorf("reload queue: %w", err))
		state.QueueMu.Lock()
		kept := make([]types.QueueItem, 0, len(state.Queue))
		for _, item := range state.Queue {
			if item.AccountID != accountID {
				kept = append(kept, item)
			}
		}
		state.Queue = kept
		state.QueueMu.Unlock()
	}
	if items, err := state.DB.ListHistory(); err == nil {
		state.HistoryMu.Lock()
		state.History = items
		if state.History == nil {
			state.History = []types.PurchaseHistoryEntry{}
		}
		state.HistoryMu.Unlock()
	} else {
		reloadErrors = append(reloadErrors, fmt.Errorf("reload history: %w", err))
		state.HistoryMu.Lock()
		kept := make([]types.PurchaseHistoryEntry, 0, len(state.History))
		for _, item := range state.History {
			if item.AccountID != accountID {
				kept = append(kept, item)
			}
		}
		state.History = kept
		state.HistoryMu.Unlock()
	}
	// 监控订阅内存由 DeleteAccountByID 的持久化临界区在删除后刷新。
	if subs, err := state.DB.ListVPSSubscriptions(); err == nil {
		state.VPSSubsMu.Lock()
		state.VPSSubscriptions = subs
		if state.VPSSubscriptions == nil {
			state.VPSSubscriptions = []types.VPSSubscription{}
		}
		state.VPSSubsMu.Unlock()
	} else {
		reloadErrors = append(reloadErrors, fmt.Errorf("reload VPS subscriptions: %w", err))
		state.VPSSubsMu.Lock()
		for i := range state.VPSSubscriptions {
			if state.VPSSubscriptions[i].AutoOrderAccountID == accountID {
				state.VPSSubscriptions[i].AutoOrderAccountID = ""
			}
		}
		state.VPSSubsMu.Unlock()
	}
	return errors.Join(reloadErrors...)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
