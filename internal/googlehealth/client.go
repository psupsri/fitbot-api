// Package googlehealth เรียก Google Health API (https://health.googleapis.com) ระดับ HTTP
// ใช้ใน M0 (F0 API Spike) และจะใช้ต่อใน F4 Fetcher หลังผ่าน Gate
package googlehealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://health.googleapis.com"

// Scopes ที่ Fitbot ต้องใช้ (อ่านอย่างเดียว) ตาม https://developers.google.com/health/scopes
var Scopes = []string{
	"https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly",
	"https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly",
	"https://www.googleapis.com/auth/googlehealth.sleep.readonly",
	"https://www.googleapis.com/auth/googlehealth.profile.readonly",
}

// ErrAccountNotLinked: บัญชี Google ยังไม่มีโปรไฟล์ Google Health หรือยังไม่ย้ายบัญชี Fitbit (HTTP 400 FAILED_PRECONDITION)
var ErrAccountNotLinked = errors.New("ACCOUNT_NOT_LINKED")

// ErrUnauthorized: token ใช้ไม่ได้ (HTTP 401 หรือ refresh token หมดอายุ/ถูกเพิกถอน) ต้อง re-auth
var ErrUnauthorized = errors.New("token ของ Google Health ใช้ไม่ได้")

// APIError คือ error body มาตรฐานของ Google API
type APIError struct {
	HTTPStatus  int
	Code        int    `json:"code"`
	Message     string `json:"message"`
	Status      string `json:"status"`
	Reason      string // จาก details[].reason ของ google.rpc.ErrorInfo
	RedirectURI string // จาก details[].metadata.redirect_uri (กรณี ACCOUNT_NOT_LINKED)
	Body        string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("HTTP %d %s: %s", e.HTTPStatus, e.Status, e.Message)
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

func (e *APIError) Is(target error) bool {
	switch target {
	case ErrAccountNotLinked:
		return e.Reason == "ACCOUNT_NOT_LINKED"
	case ErrUnauthorized:
		return e.HTTPStatus == http.StatusUnauthorized
	}
	return false
}

// Retryable: ลองซ้ำเฉพาะข้อผิดพลาดชั่วคราว
func (e *APIError) Retryable() bool {
	return e.HTTPStatus == http.StatusTooManyRequests || e.HTTPStatus >= 500
}

func ParseError(status int, body []byte) *APIError {
	e := &APIError{HTTPStatus: status, Body: string(body)}
	var env struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Reason   string            `json:"reason"`
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil {
		e.Code, e.Message, e.Status = env.Error.Code, env.Error.Message, env.Error.Status
		for _, d := range env.Error.Details {
			if d.Reason != "" {
				e.Reason = d.Reason
				e.RedirectURI = d.Metadata["redirect_uri"]
				break
			}
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(truncate(string(body), 300))
	}
	return e
}

type Client struct {
	HTTP       *http.Client // แนบ OAuth token แล้ว
	BaseURL    string
	MaxRetries int
	Sleep      func(time.Duration)
}

func New(h *http.Client) *Client {
	return &Client{HTTP: h, BaseURL: DefaultBaseURL, MaxRetries: 3, Sleep: time.Sleep}
}

// Response คือผลการเรียกหนึ่งครั้ง (เก็บ body ดิบไว้เสมอ แม้เป็น error)
type Response struct {
	Status   int
	Body     []byte
	Duration time.Duration
}

// Do เรียก API: method GET หรือ POST, path เริ่มด้วย /v4/..., body เป็น JSON หรือ nil
// ลองซ้ำเมื่อ 429/5xx/network error แบบ exponential backoff
func (c *Client) Do(ctx context.Context, method, path string, body []byte) (*Response, error) {
	var lastErr error
	var last *Response
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			c.Sleep(time.Duration(1<<(attempt-1)) * time.Second)
		}
		resp, err := c.once(ctx, method, path, body)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || errors.Is(err, ErrUnauthorized) {
				return nil, err
			}
			continue
		}
		last = resp
		if resp.Status == http.StatusOK {
			return resp, nil
		}
		apiErr := ParseError(resp.Status, resp.Body)
		lastErr = apiErr
		if !apiErr.Retryable() {
			return resp, apiErr
		}
	}
	return last, lastErr
}

func (c *Client) once(ctx context.Context, method, path string, body []byte) (*Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Body: b, Duration: time.Since(start)}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
