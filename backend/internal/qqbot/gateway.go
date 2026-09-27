package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	gatewayReadyTimeout  = 15 * time.Second
	gatewayHandshakeWait = 15 * time.Second
	gatewayWriteTimeout  = 5 * time.Second
	gatewayReconnectMin  = 1 * time.Second
	gatewayReconnectMax  = 30 * time.Second
	gatewayIntents       = 1 // GUILDS；频道主动发送要求 Gateway 在线
)

type gatewaySession struct {
	token     string
	expiresAt time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	ready     chan struct{}
	done      chan struct{}

	mu          sync.RWMutex
	online      bool
	readyClosed bool
	lastErr     error
}

func newGatewaySession(token string, expiresAt time.Time) *gatewaySession {
	ctx, cancel := context.WithCancel(context.Background())
	return &gatewaySession{
		token:     token,
		expiresAt: expiresAt,
		ctx:       ctx,
		cancel:    cancel,
		ready:     make(chan struct{}),
		done:      make(chan struct{}),
	}
}

func (s *gatewaySession) setOnline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.online = true
	if !s.readyClosed {
		close(s.ready)
		s.readyClosed = true
	}
}

func (s *gatewaySession) setOffline(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.online = false
	s.lastErr = err
	if !s.readyClosed {
		close(s.ready)
	}
	s.ready = make(chan struct{})
	s.readyClosed = false
}

func (s *gatewaySession) snapshot() (ready, done chan struct{}, online bool, lastErr error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready, s.done, s.online, s.lastErr
}

func (s *gatewaySession) isOnline() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.online
}

func (s *gatewaySession) error() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

type gatewayPayload struct {
	ID string          `json:"id,omitempty"`
	Op int             `json:"op"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
	D  json.RawMessage `json:"d"`
}

type gatewayHello struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

type gatewayIdentify struct {
	Token      string                 `json:"token"`
	Intents    int                    `json:"intents"`
	Shard      [2]int                 `json:"shard"`
	Properties map[string]interface{} `json:"properties"`
}

type gatewayReadResult struct {
	data []byte
	err  error
}

func (c *Client) ensureGateway(ctx context.Context, token string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.gatewayMu.Lock()
		session := c.gateway
		c.tokenMu.Lock()
		tokenExpiresAt := c.tokenExpiresAt
		c.tokenMu.Unlock()
		now := c.now()
		needsNew := session == nil || session.token != token || isClosed(session.done) ||
			(!session.expiresAt.IsZero() && !now.Before(session.expiresAt.Add(-tokenRefreshBefore)))
		if needsNew {
			if session != nil {
				session.cancel()
			}
			session = newGatewaySession(token, tokenExpiresAt)
			c.gateway = session
			go c.gatewayLoop(session)
		}
		ready, done, online, lastErr := session.snapshot()
		c.gatewayMu.Unlock()

		if online {
			return nil
		}
		select {
		case <-ready:
			if session.isOnline() {
				return nil
			}
			continue
		case <-done:
			if err := session.error(); err != nil {
				return fmt.Errorf("QQ Gateway 已停止：%w", err)
			}
			return errors.New("QQ Gateway 已停止")
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("等待 QQ Gateway 在线失败：%w", lastErr)
			}
			return fmt.Errorf("等待 QQ Gateway 在线失败：%w", ctx.Err())
		}
	}
}

func (c *Client) gatewayLoop(session *gatewaySession) {
	defer close(session.done)
	backoff := gatewayReconnectMin
	for {
		if session.ctx.Err() != nil {
			return
		}
		err := c.connectGateway(session)
		if session.ctx.Err() != nil {
			return
		}
		session.setOffline(err)
		timer := time.NewTimer(backoff)
		select {
		case <-session.ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		if backoff < gatewayReconnectMax {
			backoff *= 2
			if backoff > gatewayReconnectMax {
				backoff = gatewayReconnectMax
			}
		}
	}
}

func (c *Client) connectGateway(session *gatewaySession) error {
	gatewayURL, err := c.fetchGatewayURL(session.ctx, session.token)
	if err != nil {
		return err
	}
	header := http.Header{}
	header.Set("Authorization", "QQBot "+session.token)
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = gatewayHandshakeWait
	conn, _, err := dialer.DialContext(session.ctx, gatewayURL, header)
	if err != nil {
		return fmt.Errorf("连接 QQ Gateway 失败: %w", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(gatewayHandshakeWait))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("读取 QQ Gateway Hello 失败: %w", err)
	}
	var hello gatewayPayload
	if err := json.Unmarshal(raw, &hello); err != nil {
		return fmt.Errorf("解析 QQ Gateway Hello 失败: %w", err)
	}
	if hello.Op != 10 {
		return fmt.Errorf("QQ Gateway 首帧不是 Hello：op=%d", hello.Op)
	}
	var helloData gatewayHello
	if err := json.Unmarshal(hello.D, &helloData); err != nil {
		return fmt.Errorf("解析 QQ Gateway 心跳参数失败: %w", err)
	}
	if helloData.HeartbeatInterval <= 0 {
		return errors.New("QQ Gateway 返回了无效心跳间隔")
	}

	identify := gatewayPayload{
		Op: 2,
		D: mustJSON(gatewayIdentify{
			Token:   "QQBot " + session.token,
			Intents: gatewayIntents,
			Shard:   [2]int{0, 1},
			Properties: map[string]interface{}{
				"$os":      "linux",
				"$browser": "ovh-webui",
				"$device":  "ovh-webui",
			},
		}),
	}
	if err := writeGatewayJSON(conn, identify); err != nil {
		return fmt.Errorf("QQ Gateway 鉴权失败: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})

	readResults := make(chan gatewayReadResult, 1)
	readCtx, readCancel := context.WithCancel(session.ctx)
	defer readCancel()
	go readGatewayMessages(readCtx, conn, readResults)
	heartbeat := time.NewTicker(time.Duration(helloData.HeartbeatInterval) * time.Millisecond)
	defer heartbeat.Stop()
	readyTimeout := time.NewTimer(gatewayReadyTimeout)
	defer readyTimeout.Stop()
	var sequence *int64
	for {
		select {
		case <-session.ctx.Done():
			return session.ctx.Err()
		case <-readyTimeout.C:
			if !session.isOnline() {
				return errors.New("等待 QQ Gateway READY 超时")
			}
		case result := <-readResults:
			if result.err != nil {
				return fmt.Errorf("QQ Gateway 连接中断: %w", result.err)
			}
			var payload gatewayPayload
			if err := json.Unmarshal(result.data, &payload); err != nil {
				return fmt.Errorf("解析 QQ Gateway 消息失败: %w", err)
			}
			if payload.S != nil {
				value := *payload.S
				sequence = &value
			}
			switch payload.Op {
			case 0:
				if payload.T == "READY" {
					session.setOnline()
				}
			case 1:
				if err := writeGatewayJSON(conn, gatewayPayload{Op: 11, D: mustJSON(sequence)}); err != nil {
					return fmt.Errorf("回复 QQ Gateway 心跳失败: %w", err)
				}
			case 7:
				return errors.New("QQ Gateway 要求重新连接")
			case 9:
				return errors.New("QQ Gateway 鉴权会话无效")
			}
		case <-heartbeat.C:
			if err := writeGatewayJSON(conn, gatewayPayload{Op: 1, D: mustJSON(sequence)}); err != nil {
				return fmt.Errorf("发送 QQ Gateway 心跳失败: %w", err)
			}
		}
	}
}

func readGatewayMessages(ctx context.Context, conn *websocket.Conn, results chan<- gatewayReadResult) {
	for {
		_, data, err := conn.ReadMessage()
		result := gatewayReadResult{data: data, err: err}
		select {
		case results <- result:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) fetchGatewayURL(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/gateway", nil)
	if err != nil {
		return "", fmt.Errorf("创建 QQ Gateway 请求失败: %w", err)
	}
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 QQ Gateway 地址失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取 QQ Gateway 响应失败: %w", err)
	}
	if len(body) > maxResponseBodyBytes {
		return "", errors.New("QQ Gateway 响应过大")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", parseAPIError(resp.StatusCode, body)
	}
	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析 QQ Gateway 地址失败: %w", err)
	}
	if strings.TrimSpace(result.URL) == "" {
		return "", errors.New("QQ Gateway 响应缺少 url")
	}
	return strings.TrimSpace(result.URL), nil
}

func writeGatewayJSON(conn *websocket.Conn, value interface{}) error {
	if err := conn.SetWriteDeadline(time.Now().Add(gatewayWriteTimeout)); err != nil {
		return err
	}
	return conn.WriteJSON(value)
}

func mustJSON(value interface{}) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Close stops the optional QQ Gateway connection. HTTP-only targets never start one.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.gatewayMu.Lock()
	defer c.gatewayMu.Unlock()
	if c.gateway != nil {
		c.gateway.cancel()
	}
}
