package googlehealth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ตัวอย่าง error จาก https://developers.google.com/health/troubleshooting
const notLinkedBody = `{"error":{"code":400,"message":"The account is not linked to Google Health.","status":"FAILED_PRECONDITION",
"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"ACCOUNT_NOT_LINKED","domain":"health.googleapis.com",
"metadata":{"redirect_uri":"https://fitbit.google.com/auth/signup"}}]}}`

func TestParseAccountNotLinked(t *testing.T) {
	e := ParseError(400, []byte(notLinkedBody))
	if !errors.Is(e, ErrAccountNotLinked) || e.RedirectURI != "https://fitbit.google.com/auth/signup" || e.Retryable() {
		t.Fatalf("%+v", e)
	}
}

func TestRetryOnlyTransient(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.Client())
	c.BaseURL, c.Sleep = srv.URL, func(time.Duration) {}
	resp, err := c.Do(context.Background(), "GET", "/v4/x", nil)
	if err != nil || resp.Status != 200 || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}

	calls = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(400)
		w.Write([]byte(notLinkedBody))
	}))
	defer srv2.Close()
	c.HTTP, c.BaseURL = srv2.Client(), srv2.URL
	if _, err := c.Do(context.Background(), "GET", "/v4/x", nil); !errors.Is(err, ErrAccountNotLinked) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
