package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStripThinking(t *testing.T) {
	cases := map[string]string{
		"<think>a\nb</think>\nคำตอบ": "คำตอบ",
		"เหตุผล</think>คำตอบ":        "คำตอบ",
		"คำตอบ<think>ค้าง":           "คำตอบ",
		"คำตอบ":                      "คำตอบ",
	}
	for in, want := range cases {
		if got := StripThinking(in); got != want {
			t.Errorf("%q → %q want %q", in, got, want)
		}
	}
}

func TestChatSendsReasoningNoneAndStripsThinking(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"<think>x</think>{\"a\":1}"}}]}`)
	}))
	defer srv.Close()
	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	out, err := New(cfg).Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil || out != `{"a":1}` {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if got["reasoning_effort"] != "none" || got["model"] != "qwen3.5:9b" || got["response_format"] == nil {
		t.Fatalf("request = %v", got)
	}
}

func TestChatErrors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseURL = "http://127.0.0.1:1" // ไม่มี server
	if _, err := New(cfg).Chat(context.Background(), nil); err == nil {
		t.Fatal("ต้อง error เมื่อ Ollama ไม่ทำงาน")
	}
}
