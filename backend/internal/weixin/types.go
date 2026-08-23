package weixin

import (
	"strings"
	"time"
)

const (
	DefaultBaseURL     = "https://ilinkai.weixin.qq.com"
	channelVersion     = "2.2.0"
	ilinkAppID         = "bot"
	ilinkClientVersion = "131584"
	messageTypeBot     = 2
	messageStateFinish = 2
	itemTypeText       = 1
	maxMessageRunes    = 1800
)

type Credentials struct {
	AccountID string `json:"accountId" db:"account_id"`
	Token     string `json:"-" db:"bot_token"`
	BaseURL   string `json:"-" db:"base_url"`
	UserID    string `json:"userId" db:"user_id"`
	UpdatedAt int64  `json:"-" db:"updated_at"`
}

type APIResult struct {
	Ret     *int   `json:"ret,omitempty"`
	ErrCode *int   `json:"errcode,omitempty"`
	ErrMsg  string `json:"errmsg,omitempty"`
	Message string `json:"msg,omitempty"`
}

func (r APIResult) OK() bool {
	return (r.Ret == nil || *r.Ret == 0) && (r.ErrCode == nil || *r.ErrCode == 0)
}

func (r APIResult) codeIs(code int) bool {
	return (r.Ret != nil && *r.Ret == code) || (r.ErrCode != nil && *r.ErrCode == code)
}

func (r APIResult) staleSession() bool {
	if r.codeIs(-14) {
		return true
	}
	return r.codeIs(-2) && strings.EqualFold(strings.TrimSpace(r.ErrMsg), "unknown error")
}

func (r APIResult) rateLimited() bool {
	return r.codeIs(-2) && !r.staleSession()
}

type QRCodeResponse struct {
	APIResult
	QRCode       string `json:"qrcode"`
	ImageContent string `json:"qrcode_img_content"`
}

type QRStatusResponse struct {
	APIResult
	Status       string `json:"status"`
	RedirectHost string `json:"redirect_host"`
	BotID        string `json:"ilink_bot_id"`
	BotToken     string `json:"bot_token"`
	BaseURL      string `json:"baseurl"`
	UserID       string `json:"ilink_user_id"`
	ContextToken string `json:"context_token"`
}

type TextItem struct {
	Text string `json:"text"`
}

type MessageItem struct {
	Type     int       `json:"type"`
	TextItem *TextItem `json:"text_item,omitempty"`
}

type InboundMessage struct {
	MessageID    string        `json:"message_id"`
	FromUserID   string        `json:"from_user_id"`
	ToUserID     string        `json:"to_user_id"`
	MsgType      int           `json:"msg_type"`
	RoomID       string        `json:"room_id"`
	ChatRoomID   string        `json:"chat_room_id"`
	ContextToken string        `json:"context_token"`
	ItemList     []MessageItem `json:"item_list"`
}

type UpdatesResponse struct {
	APIResult
	Messages          []InboundMessage `json:"msgs"`
	SyncBuf           string           `json:"get_updates_buf"`
	LongPollingMillis int              `json:"longpolling_timeout_ms"`
}

type LoginStart struct {
	SessionID string `json:"sessionId"`
	QRContent string `json:"qrContent"`
	ExpiresIn int    `json:"expiresIn"`
	Status    string `json:"status"`
}

type LoginStatus struct {
	Status    string `json:"status"`
	Connected bool   `json:"connected"`
	AccountID string `json:"accountId,omitempty"`
	UserID    string `json:"userId,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Status struct {
	Configured bool   `json:"configured"`
	Connected  bool   `json:"connected"`
	Polling    bool   `json:"polling"`
	AccountID  string `json:"accountId,omitempty"`
	UserID     string `json:"userId,omitempty"`
	LastPollAt string `json:"lastPollAt,omitempty"`
	LastError  string `json:"lastError,omitempty"`
}

type loginSession struct {
	ID        string
	QRCode    string
	QRContent string
	BaseURL   string
	ExpiresAt time.Time
	Result    *LoginStatus
}
