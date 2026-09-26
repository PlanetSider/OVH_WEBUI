package logger

import "testing"

func TestRedactMessageProtectsStructuredSecrets(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "json quoted key",
			in:   `{"api_key":"top-secret","user":"alice"}`,
			want: `{"api_key":[REDACTED],"user":"alice"}`,
		},
		{
			name: "json nested token",
			in:   `{"settings":{"token":"top-secret"}}`,
			want: `{"settings":{"token":[REDACTED]}}`,
		},
		{
			name: "assignment",
			in:   `api_key=top-secret`,
			want: `api_key=[REDACTED]`,
		},
		{
			name: "quoted assignment",
			in:   `'encrypt_key' = 'top-secret'`,
			want: `'encrypt_key' = [REDACTED]`,
		},
		{
			name: "word boundary",
			in:   `not_token=keep-this-value`,
			want: `not_token=keep-this-value`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := redactMessage(test.in); got != test.want {
				t.Fatalf("redactMessage(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestRedactMessageRemovesBearerToken(t *testing.T) {
	got := redactMessage(`Authorization: Bearer top-secret`)
	if got != `Authorization: [REDACTED] [REDACTED]` {
		t.Fatalf("redactMessage bearer = %q", got)
	}
}
