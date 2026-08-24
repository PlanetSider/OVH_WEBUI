package handlers

import (
	"encoding/json"
	"testing"
)

func TestTrustedFeishuVerificationURL(t *testing.T) {
	allowed := []string{
		"https://open.feishu.cn/page/launcher?user_code=ABCD-1234",
		"https://open.larksuite.com/page/launcher?user_code=ABCD-1234",
		"https://accounts.feishu.cn/oauth/verify?user_code=ABCD-1234",
		"https://accounts.larksuite.com/oauth/verify?user_code=ABCD-1234",
	}
	for _, raw := range allowed {
		if _, ok := trustedFeishuVerificationURL(raw); !ok {
			t.Errorf("trustedFeishuVerificationURL(%q) should be allowed", raw)
		}
	}

	denied := []string{
		"http://open.feishu.cn/page/launcher?user_code=ABCD-1234",
		"https://example.com/page/launcher?user_code=ABCD-1234",
		"https://open.feishu.cn:8443/page/launcher?user_code=ABCD-1234",
		"https://user:pass@open.feishu.cn/page/launcher?user_code=ABCD-1234",
	}
	for _, raw := range denied {
		if _, ok := trustedFeishuVerificationURL(raw); ok {
			t.Errorf("trustedFeishuVerificationURL(%q) should be denied", raw)
		}
	}
}

func TestFeishuRegistrationResponseSupportsCurrentExpiryField(t *testing.T) {
	var response feishuRegistrationResponse
	if err := json.Unmarshal([]byte(`{"expires_in":3600,"interval":5}`), &response); err != nil {
		t.Fatal(err)
	}
	if response.ExpiresIn != 3600 {
		t.Fatalf("ExpiresIn = %d, want 3600", response.ExpiresIn)
	}
}
