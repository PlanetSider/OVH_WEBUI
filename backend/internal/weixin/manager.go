package weixin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ovh-webui/server/internal/app"
)

type TextHandler func(senderID, text string) string

type Manager struct {
	state   *app.State
	store   *Store
	client  *Client
	handler TextHandler
	ownerID string

	credentialsMu sync.RWMutex
	credentials   Credentials

	lifecycleMu sync.Mutex
	pollCancel  context.CancelFunc
	pollDone    chan struct{}

	sendMu sync.Mutex

	loginMu       sync.Mutex
	loginSessions map[string]*loginSession

	statusMu  sync.RWMutex
	polling   bool
	connected bool
	lastPoll  time.Time
	lastError string
}

func NewManager(state *app.State, handler TextHandler) *Manager {
	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          10,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       20 * time.Second,
			ResponseHeaderTimeout: 42 * time.Second,
		},
	}
	return newManager(state, NewStore(state.DB), NewClient(httpClient, DefaultBaseURL), handler)
}

func newManager(state *app.State, store *Store, client *Client, handler TextHandler) *Manager {
	manager := &Manager{
		state:         state,
		store:         store,
		client:        client,
		handler:       handler,
		ownerID:       uuid.NewString(),
		loginSessions: make(map[string]*loginSession),
	}
	if credentials, ok, err := store.LoadCredentials(); err != nil {
		state.Logger.Error("加载微信 iLink 凭据失败: "+err.Error(), "weixin")
	} else if ok {
		manager.credentials = credentials
	}
	return manager
}

func (m *Manager) Configured() bool {
	credentials := m.currentCredentials()
	return credentials.AccountID != "" && credentials.Token != "" && credentials.BaseURL != "" && credentials.UserID != ""
}

func (m *Manager) Start() {
	if !m.Configured() {
		return
	}
	m.lifecycleMu.Lock()
	if m.pollCancel != nil {
		m.lifecycleMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.pollCancel = cancel
	m.pollDone = done
	m.lifecycleMu.Unlock()
	go m.pollLoop(ctx, done)
}

func (m *Manager) Stop() {
	m.lifecycleMu.Lock()
	cancel := m.pollCancel
	done := m.pollDone
	m.pollCancel = nil
	m.pollDone = nil
	m.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (m *Manager) restart() {
	m.Stop()
	m.Start()
}

func (m *Manager) SendDefault(message string) bool {
	credentials := m.currentCredentials()
	if credentials.UserID == "" {
		return false
	}
	if err := m.SendTo(context.Background(), credentials.UserID, message); err != nil {
		m.state.Logger.Warn("微信通知发送失败: "+err.Error(), "weixin")
		return false
	}
	return true
}

func (m *Manager) SendTest(ctx context.Context) error {
	if !m.state.Config.Get().IsWeixinNotificationsEnabled() {
		return fmt.Errorf("微信通知已关闭")
	}
	credentials := m.currentCredentials()
	if credentials.UserID == "" {
		return fmt.Errorf("微信 iLink Bot 尚未连接")
	}
	return m.SendTo(ctx, credentials.UserID,
		"🔔 微信 iLink Bot 测试通知\n\n时间: "+time.Now().Format("2006-01-02 15:04:05")+"\n\n✅ 通知配置正常！")
}

func (m *Manager) SendTo(ctx context.Context, userID, message string) error {
	credentials := m.currentCredentials()
	if credentials.Token == "" || strings.TrimSpace(userID) == "" {
		return fmt.Errorf("微信 iLink Bot 尚未连接")
	}
	chunks := splitText(strings.TrimSpace(message), maxMessageRunes)
	if len(chunks) == 0 {
		return fmt.Errorf("消息内容为空")
	}
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	contextToken, err := m.store.ContextToken(credentials.AccountID, userID)
	if err != nil {
		m.state.Logger.Warn("读取微信 context_token 失败: "+err.Error(), "weixin")
		contextToken = ""
	}
	for index, chunk := range chunks {
		if index > 0 {
			if err := sleepContext(ctx, 350*time.Millisecond); err != nil {
				return err
			}
		}
		tokenValid, err := m.sendChunk(ctx, credentials, userID, chunk, contextToken)
		if err != nil {
			return err
		}
		if !tokenValid {
			contextToken = ""
		}
	}
	return nil
}

func (m *Manager) sendChunk(ctx context.Context, credentials Credentials, userID, text, contextToken string) (bool, error) {
	clientID := "ovh-webui-weixin-" + uuid.NewString()
	retriedWithoutToken := false
	for attempt := 0; attempt < 3; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		result, err := m.client.SendMessage(requestCtx, credentials, userID, text, contextToken, clientID)
		cancel()
		if err != nil {
			if attempt == 2 {
				return contextToken != "", err
			}
			if err := sleepContext(ctx, time.Duration(attempt+1)*2*time.Second); err != nil {
				return contextToken != "", err
			}
			continue
		}
		if result.OK() {
			return contextToken != "", nil
		}
		if result.staleSession() && contextToken != "" && !retriedWithoutToken {
			retriedWithoutToken = true
			contextToken = ""
			_ = m.store.DeleteContextToken(credentials.AccountID, userID)
			m.state.Logger.Warn("微信会话上下文已失效，正在无 context_token 重试", "weixin")
			continue
		}
		if result.rateLimited() && attempt < 2 {
			if err := sleepContext(ctx, time.Duration(attempt+1)*6*time.Second); err != nil {
				return contextToken != "", err
			}
			continue
		}
		return contextToken != "", fmt.Errorf("iLink sendmessage ret=%v errcode=%v: %s", result.Ret, result.ErrCode, firstNonEmpty(result.ErrMsg, result.Message))
	}
	return contextToken != "", fmt.Errorf("iLink sendmessage 重试次数已用尽")
}

func (m *Manager) StartLogin(ctx context.Context) (LoginStart, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	response, err := m.client.GetBotQRCode(requestCtx)
	if err != nil {
		return LoginStart{}, err
	}
	if !response.OK() || strings.TrimSpace(response.QRCode) == "" {
		return LoginStart{}, fmt.Errorf("iLink 未返回有效二维码: %s", firstNonEmpty(response.ErrMsg, response.Message))
	}
	content := strings.TrimSpace(response.ImageContent)
	if content == "" {
		content = strings.TrimSpace(response.QRCode)
	}
	session := &loginSession{
		ID:        uuid.NewString(),
		QRCode:    strings.TrimSpace(response.QRCode),
		QRContent: content,
		BaseURL:   DefaultBaseURL,
		ExpiresAt: time.Now().Add(8 * time.Minute),
	}
	m.loginMu.Lock()
	m.cleanupLoginSessionsLocked()
	m.loginSessions[session.ID] = session
	m.loginMu.Unlock()
	return LoginStart{SessionID: session.ID, QRContent: session.QRContent, ExpiresIn: 480, Status: "wait"}, nil
}

func (m *Manager) PollLogin(ctx context.Context, sessionID string) (LoginStatus, error) {
	sessionID = strings.TrimSpace(sessionID)
	m.loginMu.Lock()
	session := m.loginSessions[sessionID]
	if session == nil {
		m.loginMu.Unlock()
		return LoginStatus{}, fmt.Errorf("扫码会话不存在或已过期")
	}
	if session.Result != nil {
		result := *session.Result
		m.loginMu.Unlock()
		return result, nil
	}
	if time.Now().After(session.ExpiresAt) {
		result := LoginStatus{Status: "expired", Error: "二维码已过期，请重新生成"}
		session.Result = &result
		m.loginMu.Unlock()
		return result, nil
	}
	baseURL, qrcode := session.BaseURL, session.QRCode
	m.loginMu.Unlock()

	requestCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	response, err := m.client.GetQRCodeStatus(requestCtx, baseURL, qrcode)
	cancel()
	if err != nil {
		return LoginStatus{}, err
	}
	if !response.OK() {
		return LoginStatus{}, fmt.Errorf("查询 iLink 扫码状态失败: %s", firstNonEmpty(response.ErrMsg, response.Message))
	}
	switch strings.TrimSpace(response.Status) {
	case "", "wait":
		return LoginStatus{Status: "wait"}, nil
	case "scaned":
		return LoginStatus{Status: "scanned"}, nil
	case "scaned_but_redirect":
		if host := strings.TrimSpace(response.RedirectHost); host != "" {
			m.loginMu.Lock()
			if current := m.loginSessions[sessionID]; current != nil {
				if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
					current.BaseURL = strings.TrimRight(host, "/")
				} else {
					current.BaseURL = "https://" + host
				}
			}
			m.loginMu.Unlock()
		}
		return LoginStatus{Status: "scanned"}, nil
	case "expired":
		result := LoginStatus{Status: "expired", Error: "二维码已过期，请重新生成"}
		m.setLoginResult(sessionID, result)
		return result, nil
	case "confirmed":
		credentials := Credentials{
			AccountID: strings.TrimSpace(response.BotID),
			Token:     strings.TrimSpace(response.BotToken),
			BaseURL:   strings.TrimRight(strings.TrimSpace(response.BaseURL), "/"),
			UserID:    strings.TrimSpace(response.UserID),
			UpdatedAt: time.Now().UnixMilli(),
		}
		if credentials.BaseURL == "" {
			credentials.BaseURL = baseURL
		}
		if credentials.AccountID == "" || credentials.Token == "" || credentials.UserID == "" {
			return LoginStatus{}, fmt.Errorf("扫码已确认，但 iLink 返回的凭据不完整")
		}
		if err := m.store.SaveCredentials(credentials); err != nil {
			return LoginStatus{}, err
		}
		m.credentialsMu.Lock()
		m.credentials = credentials
		m.credentialsMu.Unlock()
		if token := strings.TrimSpace(response.ContextToken); token != "" {
			if err := m.store.SaveContextToken(credentials.AccountID, credentials.UserID, token); err != nil {
				m.state.Logger.Warn("保存扫码返回的微信 context_token 失败: "+err.Error(), "weixin")
			}
		}
		result := LoginStatus{
			Status: "confirmed", Connected: true,
			AccountID: credentials.AccountID, UserID: credentials.UserID,
		}
		m.setLoginResult(sessionID, result)
		m.restart()
		return result, nil
	default:
		return LoginStatus{Status: "error", Error: "未知扫码状态: " + response.Status}, nil
	}
}

func (m *Manager) Disconnect() error {
	m.Stop()
	if err := m.store.DeleteAll(); err != nil {
		return err
	}
	m.credentialsMu.Lock()
	m.credentials = Credentials{}
	m.credentialsMu.Unlock()
	m.statusMu.Lock()
	m.connected = false
	m.lastError = ""
	m.statusMu.Unlock()
	return nil
}

func (m *Manager) Status() Status {
	credentials := m.currentCredentials()
	m.statusMu.RLock()
	result := Status{
		Configured: m.Configured(),
		Connected:  m.connected,
		Polling:    m.polling,
		AccountID:  credentials.AccountID,
		UserID:     credentials.UserID,
		LastError:  m.lastError,
	}
	if !m.lastPoll.IsZero() {
		result.LastPollAt = m.lastPoll.Format(time.RFC3339)
	}
	m.statusMu.RUnlock()
	return result
}

func (m *Manager) pollLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	defer func() {
		_ = m.store.ReleaseLease("poller", m.ownerID)
		m.statusMu.Lock()
		m.polling = false
		m.connected = false
		m.statusMu.Unlock()
		m.lifecycleMu.Lock()
		if m.pollDone == done {
			m.pollCancel = nil
			m.pollDone = nil
		}
		m.lifecycleMu.Unlock()
	}()

	credentials := m.currentCredentials()
	syncBuf, err := m.store.LoadSyncBuf(credentials.AccountID)
	if err != nil {
		m.setPollError(err)
		return
	}
	m.statusMu.Lock()
	m.polling = true
	m.statusMu.Unlock()

	consecutiveFailures := 0
	for ctx.Err() == nil {
		acquired, err := m.store.AcquireLease("poller", m.ownerID, time.Now(), 90*time.Second)
		if err != nil {
			m.setPollError(err)
			return
		}
		if !acquired {
			m.setPollError(fmt.Errorf("另一个 OVH WebUI 实例正在消费微信消息"))
			return
		}
		requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		response, err := m.client.GetUpdates(requestCtx, credentials, syncBuf)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			consecutiveFailures++
			m.setPollError(err)
			delay := 2 * time.Second
			if consecutiveFailures >= 3 {
				delay = 30 * time.Second
				consecutiveFailures = 0
			}
			if sleepContext(ctx, delay) != nil {
				return
			}
			continue
		}
		if !response.OK() {
			m.setPollError(fmt.Errorf("iLink getupdates ret=%v errcode=%v: %s",
				response.Ret, response.ErrCode, firstNonEmpty(response.ErrMsg, response.Message)))
			delay := 2 * time.Second
			if response.staleSession() {
				delay = 60 * time.Second
			} else if response.rateLimited() {
				delay = 30 * time.Second
			}
			if sleepContext(ctx, delay) != nil {
				return
			}
			continue
		}
		consecutiveFailures = 0
		m.statusMu.Lock()
		m.connected = true
		m.lastPoll = time.Now()
		m.lastError = ""
		m.statusMu.Unlock()
		if response.SyncBuf != "" && response.SyncBuf != syncBuf {
			syncBuf = response.SyncBuf
			if err := m.store.SaveSyncBuf(credentials.AccountID, syncBuf); err != nil {
				m.state.Logger.Warn("保存微信同步游标失败: "+err.Error(), "weixin")
			}
		}
		for _, message := range response.Messages {
			m.processInbound(credentials, message)
		}
	}
}

func (m *Manager) processInbound(credentials Credentials, message InboundMessage) {
	senderID := strings.TrimSpace(message.FromUserID)
	if senderID == "" || senderID == credentials.AccountID {
		return
	}
	isGroup := message.RoomID != "" || message.ChatRoomID != "" || strings.HasSuffix(senderID, "@chatroom") ||
		(message.MsgType == 1 && strings.TrimSpace(message.ToUserID) != "" &&
			strings.TrimSpace(message.ToUserID) != credentials.AccountID)
	if isGroup {
		return
	}
	if senderID != credentials.UserID {
		m.state.Logger.Warn("拒绝未绑定微信用户的消息: "+safeID(senderID), "weixin")
		return
	}
	if token := strings.TrimSpace(message.ContextToken); token != "" {
		if err := m.store.SaveContextToken(credentials.AccountID, senderID, token); err != nil {
			m.state.Logger.Warn("保存微信 context_token 失败: "+err.Error(), "weixin")
		}
	}
	text := extractText(message.ItemList)
	if text == "" {
		return
	}
	now := time.Now()
	if messageID := strings.TrimSpace(string(message.MessageID)); messageID != "" {
		duplicate, err := m.store.MarkSeen("id:"+messageID, now, 5*time.Minute)
		if err != nil || duplicate {
			return
		}
	}
	hash := sha256.Sum256([]byte(senderID + "|" + text))
	duplicate, err := m.store.MarkSeen("content:"+hex.EncodeToString(hash[:]), now, 5*time.Minute)
	if err != nil || duplicate {
		return
	}
	if m.handler == nil {
		return
	}
	go func() {
		reply := strings.TrimSpace(m.handler(senderID, text))
		if reply == "" {
			return
		}
		if err := m.SendTo(context.Background(), senderID, reply); err != nil {
			m.state.Logger.Warn("发送微信命令回复失败: "+err.Error(), "weixin")
		}
	}()
}

func (m *Manager) currentCredentials() Credentials {
	m.credentialsMu.RLock()
	defer m.credentialsMu.RUnlock()
	return m.credentials
}

func (m *Manager) setPollError(err error) {
	m.statusMu.Lock()
	m.connected = false
	m.lastError = err.Error()
	m.statusMu.Unlock()
	m.state.Logger.Warn("微信长轮询异常: "+err.Error(), "weixin")
}

func (m *Manager) setLoginResult(sessionID string, result LoginStatus) {
	m.loginMu.Lock()
	if session := m.loginSessions[sessionID]; session != nil {
		session.Result = &result
	}
	m.loginMu.Unlock()
}

func (m *Manager) cleanupLoginSessionsLocked() {
	now := time.Now()
	for id, session := range m.loginSessions {
		if now.After(session.ExpiresAt.Add(5 * time.Minute)) {
			delete(m.loginSessions, id)
		}
	}
}

func extractText(items []MessageItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if item.Type == itemTypeText && item.TextItem != nil {
			if value := strings.TrimSpace(item.TextItem.Text); value != "" {
				parts = append(parts, value)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, string(rune(10))))
}

func splitText(value string, maxRunes int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return []string{value}
	}
	runes := []rune(value)
	chunks := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	for len(runes) > 0 {
		end := maxRunes
		if len(runes) < end {
			end = len(runes)
		} else {
			for index := end; index > maxRunes/2; index-- {
				if runes[index-1] == rune(10) {
					end = index
					break
				}
			}
		}
		chunk := strings.TrimSpace(string(runes[:end]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = runes[end:]
	}
	return chunks
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func safeID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return value
	}
	return value[:8] + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "unknown error"
}
