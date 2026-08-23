package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Client struct {
	httpClient     *http.Client
	defaultBaseURL string
}

func NewClient(httpClient *http.Client, defaultBaseURL string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if strings.TrimSpace(defaultBaseURL) == "" {
		defaultBaseURL = DefaultBaseURL
	}
	return &Client{httpClient: httpClient, defaultBaseURL: strings.TrimRight(defaultBaseURL, "/")}
}

func (c *Client) GetBotQRCode(ctx context.Context) (QRCodeResponse, error) {
	var response QRCodeResponse
	err := c.get(ctx, c.defaultBaseURL, "ilink/bot/get_bot_qrcode", url.Values{"bot_type": {"3"}}, &response)
	return response, err
}

func (c *Client) GetQRCodeStatus(ctx context.Context, baseURL, qrcode string) (QRStatusResponse, error) {
	var response QRStatusResponse
	err := c.get(ctx, baseURL, "ilink/bot/get_qrcode_status", url.Values{"qrcode": {qrcode}}, &response)
	return response, err
}

func (c *Client) GetUpdates(ctx context.Context, credentials Credentials, syncBuf string) (UpdatesResponse, error) {
	var response UpdatesResponse
	err := c.post(ctx, credentials.BaseURL, "ilink/bot/getupdates", credentials.Token,
		map[string]any{"get_updates_buf": syncBuf}, &response)
	return response, err
}

func (c *Client) SendMessage(ctx context.Context, credentials Credentials, to, text, contextToken, clientID string) (APIResult, error) {
	message := map[string]any{
		"from_user_id":  "",
		"to_user_id":    to,
		"client_id":     clientID,
		"message_type":  messageTypeBot,
		"message_state": messageStateFinish,
		"item_list": []any{map[string]any{
			"type":      itemTypeText,
			"text_item": map[string]string{"text": text},
		}},
	}
	if contextToken != "" {
		message["context_token"] = contextToken
	}
	var response APIResult
	err := c.post(ctx, credentials.BaseURL, "ilink/bot/sendmessage", credentials.Token, map[string]any{"msg": message}, &response)
	return response, err
}

func (c *Client) get(ctx context.Context, baseURL, endpoint string, query url.Values, target any) error {
	requestURL := strings.TrimRight(baseURL, "/") + "/" + endpoint
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("iLink-App-Id", ilinkAppID)
	req.Header.Set("iLink-App-ClientVersion", ilinkClientVersion)
	return c.do(req, target)
}

func (c *Client) post(ctx context.Context, baseURL, endpoint, token string, payload map[string]any, target any) error {
	payload["base_info"] = map[string]string{"channel_version": channelVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	requestURL := strings.TrimRight(baseURL, "/") + "/" + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("iLink-App-Id", ilinkAppID)
	req.Header.Set("iLink-App-ClientVersion", ilinkClientVersion)
	req.Header.Set("X-WECHAT-UIN", randomWeChatUIN())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req, target)
}

func (c *Client) do(req *http.Request, target any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("iLink HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode iLink response: %w", err)
	}
	return nil
}

func randomWeChatUIN() string {
	var valueBytes [4]byte
	if _, err := rand.Read(valueBytes[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte("0"))
	}
	value := binary.BigEndian.Uint32(valueBytes[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(value), 10)))
}
