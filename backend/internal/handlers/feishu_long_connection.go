package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/ovh-webui/server/internal/app"
	"github.com/ovh-webui/server/internal/monitor"
)

// FeishuLongConnectionManager 管理飞书官方 WebSocket 长连接。
//
// Webhook 和长连接是两种互斥的事件接收模式：默认仍使用 Webhook，只有
// FeishuConnectionMode=long_connection 时才会建立 WebSocket。通知发送仍然
// 使用 monitor 中的 HTTP Open API，因此切换接收模式不会影响发卡片逻辑。
type FeishuLongConnectionManager struct {
	state *app.State
	mon   *monitor.Monitor

	mu        sync.Mutex
	client    *larkws.Client
	configKey string
	version   uint64
	stopped   bool
}

// NewFeishuLongConnectionManager 创建管理器并按当前配置决定是否启动连接。
func NewFeishuLongConnectionManager(state *app.State, mon *monitor.Monitor) *FeishuLongConnectionManager {
	m := &FeishuLongConnectionManager{state: state, mon: mon}
	m.Reconfigure()
	return m
}

// Reconfigure 在设置保存后调用。凭据、域名、模式发生变化时会关闭旧连接
// 并创建新连接；相同配置不会重复建连。
func (m *FeishuLongConnectionManager) Reconfigure() {
	if m == nil || m.state == nil {
		return
	}
	cfg := m.state.Config.Get()
	mode := strings.ToLower(strings.TrimSpace(cfg.FeishuConnectionMode))
	if mode == "" {
		mode = "webhook"
	}
	want := mode == "long_connection" && cfg.FeishuEnabled && strings.TrimSpace(cfg.FeishuAppID) != "" && strings.TrimSpace(cfg.FeishuAppSecret) != ""
	configKey := strings.Join([]string{mode, cfg.FeishuAppID, cfg.FeishuAppSecret, cfg.FeishuDomain}, "\x00")

	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	if !want {
		old := m.client
		m.client = nil
		m.configKey = ""
		m.version++
		m.mu.Unlock()
		if old != nil {
			old.Close()
			m.state.Logger.Info("飞书长连接已停止（当前为 Webhook 或配置未完成）", "feishu")
		}
		return
	}
	// 已有连接时，配置不变就继续复用。配置变化由保存设置前后版本判断。
	if m.client != nil && m.configKey == configKey {
		m.mu.Unlock()
		return
	}
	old := m.client
	m.client = nil
	m.configKey = configKey
	m.version++
	version := m.version
	m.mu.Unlock()
	if old != nil {
		old.Close()
		m.state.Logger.Info("飞书长连接配置已变化，正在重建连接", "feishu")
	}

	m.start(cfg.FeishuAppID, cfg.FeishuAppSecret, cfg.FeishuDomain, version)
}

// Stop 关闭当前连接。SDK Start 的主循环不会因 context 取消而返回，但
// Close 会立即关闭底层 WebSocket 并停止自动重连；退出时只保留一个无网络
// 的 SDK goroutine，不影响进程结束。
func (m *FeishuLongConnectionManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.stopped = true
	old := m.client
	m.client = nil
	m.version++
	m.mu.Unlock()
	if old != nil {
		old.Close()
		m.state.Logger.Info("飞书长连接已关闭", "feishu")
	}
}

func (m *FeishuLongConnectionManager) start(appID, appSecret, domain string, version uint64) {
	wsDomain := "https://open.feishu.cn"
	if strings.EqualFold(strings.TrimSpace(domain), "lark") {
		wsDomain = "https://open.larksuite.com"
	}

	d := dispatcher.NewEventDispatcher("", "")
	d.OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
		body, err := json.Marshal(event)
		if err != nil {
			return err
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		if !m.acceptLongConnectionPayload(payload) {
			return fmt.Errorf("飞书长连接事件 app_id 与当前配置不一致")
		}
		if !claimFeishuEvent(m.state, payload) {
			return nil
		}
		processFeishuMessage(m.state, m.mon, payload)
		return nil
	})
	d.OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
		body, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		if !m.acceptLongConnectionPayload(payload) {
			return nil, fmt.Errorf("飞书长连接卡片 app_id 与当前配置不一致")
		}
		if !claimFeishuEvent(m.state, payload) {
			return &callback.CardActionTriggerResponse{
				Toast: &callback.Toast{Type: "success", Content: "操作已处理"},
			}, nil
		}
		result := processFeishuCardActionBody(m.state, payload)
		if result.SendText && result.OpenID != "" {
			// 卡片 toast 仍立即返回；补发文本沿用 Webhook 行为，便于用户看到
			// 切换账户/入队的完整结果。
			_ = monitor.FeishuSendText(m.state, result.OpenID, result.Content)
		}
		return &callback.CardActionTriggerResponse{
			Toast: &callback.Toast{Type: result.Type, Content: result.Content},
		}, nil
	})

	client := larkws.NewClient(
		strings.TrimSpace(appID),
		strings.TrimSpace(appSecret),
		larkws.WithDomain(wsDomain),
		larkws.WithEventHandler(d),
		larkws.WithOnReady(func() {
			m.state.Logger.Info("飞书长连接已就绪", "feishu")
		}),
		larkws.WithOnError(func(err error) {
			m.state.Logger.Warn("飞书长连接错误: "+err.Error(), "feishu")
		}),
		larkws.WithOnReconnecting(func() {
			m.state.Logger.Warn("飞书长连接断开，正在自动重连", "feishu")
		}),
		larkws.WithOnReconnected(func() {
			m.state.Logger.Info("飞书长连接已重新连接", "feishu")
		}),
		larkws.WithOnDisconnected(func() {
			m.state.Logger.Info("飞书长连接已断开", "feishu")
		}),
	)

	m.mu.Lock()
	if m.stopped || version != m.version {
		m.mu.Unlock()
		client.Close()
		return
	}
	m.client = client
	m.mu.Unlock()

	m.state.Logger.Info("正在启动飞书长连接: "+wsDomain, "feishu")
	go func() {
		err := client.Start(context.Background())
		m.mu.Lock()
		if m.client == client {
			m.client = nil
		}
		m.mu.Unlock()
		if err != nil {
			m.state.Logger.Error("飞书长连接启动失败: "+err.Error(), "feishu")
		}
	}()
}

func (m *FeishuLongConnectionManager) acceptLongConnectionPayload(body map[string]interface{}) bool {
	header, _ := body["header"].(map[string]interface{})
	appID, _ := header["app_id"].(string)
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return true
	}
	return appID == strings.TrimSpace(m.state.Config.Get().FeishuAppID)
}
