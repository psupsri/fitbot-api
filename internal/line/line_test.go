package line

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPushRetriesWithSameKeyAndFailsLoudly(t *testing.T) {
	var keys []string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		keys = append(keys, r.Header.Get("X-Line-Retry-Key"))
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Error("ต้องแนบ token")
		}
		if calls == 1 {
			w.WriteHeader(500)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"to":"U1"`) {
			t.Errorf("body = %s", b)
		}
	}))
	defer srv.Close()
	c := New("tok", srv.URL)
	c.Sleep = func(time.Duration) {}
	if err := c.Push(context.Background(), "U1", []string{"สวัสดี"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || keys[0] != keys[1] {
		t.Fatalf("calls=%d keys=%v", calls, keys)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer bad.Close()
	c = New("tok", bad.URL)
	if err := c.Push(context.Background(), "U1", []string{"x"}); err == nil {
		t.Fatal("ส่งไม่สำเร็จต้องคืน error (ห้ามบันทึกว่าส่งแล้ว)")
	}
	if err := New("", bad.URL).Push(context.Background(), "U1", []string{"x"}); err == nil {
		t.Fatal("ไม่มี token ต้อง error")
	}
}

func TestPushSplitsBatchesOfFive(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()
	msgs := make([]string, 7)
	for i := range msgs {
		msgs[i] = "m"
	}
	if err := New("tok", srv.URL).Push(context.Background(), "U1", msgs); err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
