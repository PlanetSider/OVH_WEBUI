package handlers

import "testing"

func TestIsAccountSwitchRequest(t *testing.T) {
	if !isAccountSwitchRequest([]string{"switch"}) || !isAccountSwitchRequest([]string{"SWITCH"}) {
		t.Fatal("switch 子命令应被识别")
	}
	if isAccountSwitchRequest(nil) || isAccountSwitchRequest([]string{"switch", "extra"}) {
		t.Fatal("无效 switch 参数不应被识别")
	}
}

func TestFeishuLooksLikeOrder(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "型号", text: "24ska01", want: true},
		{name: "型号机房数量", text: "24ska01 gra 2", want: true},
		{name: "普通中文", text: "你好", want: false},
		{name: "命令", text: "/buy 24ska01 gra", want: false},
		{name: "标点句子", text: "订单123，怎么样", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := feishuLooksLikeOrder(tc.text); got != tc.want {
				t.Fatalf("feishuLooksLikeOrder(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestFeishuEventID(t *testing.T) {
	tests := []struct {
		name string
	body map[string]interface{}
	want string
	}{
		{name: "v2 header", body: map[string]interface{}{"header": map[string]interface{}{"event_id": "evt-v2"}}, want: "evt-v2"},
		{name: "nested event header", body: map[string]interface{}{"event": map[string]interface{}{"header": map[string]interface{}{"event_id": "evt-nested"}}}, want: "evt-nested"},
		{name: "missing", body: map[string]interface{}{}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := feishuEventID(tc.body); got != tc.want {
				t.Fatalf("feishuEventID() = %q, want %q", got, tc.want)
			}
		})
	}
}
