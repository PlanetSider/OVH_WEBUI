package handlers

import (
	"strings"
	"testing"

	"github.com/ovh-webui/server/internal/app"
)

func TestWeixinHelpExplainsAccountSwitch(t *testing.T) {
	reply := HandleWeixinText(&app.State{}, nil, "user", "帮助")
	if !strings.Contains(reply, "请在 WebUI 切换默认账户") {
		t.Fatalf("unexpected help: %q", reply)
	}
}

func TestWeixinUnknownTextIsIgnored(t *testing.T) {
	reply := HandleWeixinText(&app.State{}, nil, "user", "这不是命令")
	if reply != "" {
		t.Fatalf("unexpected reply: %q", reply)
	}
}
