package weixin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractTextAndSplit(t *testing.T) {
	text := extractText([]MessageItem{
		{Type: itemTypeText, TextItem: &TextItem{Text: "hello"}},
		{Type: 2},
		{Type: itemTypeText, TextItem: &TextItem{Text: "world"}},
	})
	want := strings.Join([]string{"hello", "world"}, string(rune(10)))
	if text != want {
		t.Fatalf("text = %q", text)
	}
	chunks := splitText(strings.Repeat("中", 19), 8)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %#v", chunks)
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 8 {
			t.Fatalf("chunk too long: %q", chunk)
		}
	}
}

func TestStatusNeverSerializesBotToken(t *testing.T) {
	data, err := json.Marshal(Status{
		Configured: true,
		AccountID:  "bot-id",
		UserID:     "user-id",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "token") {
		t.Fatalf("status leaked token field: %s", data)
	}
}
