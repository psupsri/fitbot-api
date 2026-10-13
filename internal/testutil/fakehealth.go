package testutil

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fitbot/internal/auth"
	"fitbot/internal/googlehealth"
	"fitbot/internal/model"
)

// FakeHealth จำลอง Google Health API แบบย่อ ตามโครงสร้าง response จริงที่พบใน M0
type FakeHealth struct {
	mu          sync.Mutex
	Steps       int
	WearMinutes int  // จำนวนนาทีที่มีค่าชีพจร
	HasSleep    bool // มีการนอนหลักคืนวันที่ 12 ต.ค. 2026 หรือไม่
	Srv         *httptest.Server
}

func NewFakeHealth(t *testing.T) *FakeHealth {
	f := &FakeHealth{Steps: 7350, WearMinutes: 1398, HasSleep: true}
	f.Srv = httptest.NewServer(f)
	t.Cleanup(f.Srv.Close)
	return f
}

func (f *FakeHealth) Set(fn func(*FakeHealth)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *FakeHealth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case strings.Contains(p, "/steps/"):
		fmt.Fprintf(w, `{"rollupDataPoints":[{"steps":{"countSum":"%d"}}]}`, f.Steps)
	case strings.HasSuffix(p, ":dailyRollUp"):
		io.WriteString(w, `{}`)
	case strings.Contains(p, "/heart-rate/"):
		var pts []string // หนึ่ง sample ต่อนาที
		for i := 0; i < f.WearMinutes; i++ {
			h, m := i/60, i%60
			pts = append(pts, fmt.Sprintf(`{"heartRate":{"sampleTime":{"physicalTime":"2026-10-12T%02d:%02d:00Z"}}}`, h%24, m))
		}
		io.WriteString(w, `{"dataPoints":[`+strings.Join(pts, ",")+`]}`)
	case strings.Contains(p, "/sleep/"):
		if !f.HasSleep {
			io.WriteString(w, `{"dataPoints":[]}`)
			return
		}
		io.WriteString(w, `{"dataPoints":[{"name":"s-12","sleep":{"interval":{"startTime":"2026-10-12T16:10:00Z","endTime":"2026-10-12T23:40:00Z"},"summary":{"minutesAsleep":"442"}}}]}`)
	case strings.HasSuffix(p, "/profile"):
		io.WriteString(w, `{"age":40}`)
	default:
		io.WriteString(w, `{"dataPoints":[]}`)
	}
}

// FakeAuth ให้ client ที่ชี้ไป FakeHealth โดยไม่ต้องใช้ OAuth จริง
type FakeAuth struct{ Health *FakeHealth }

func (a FakeAuth) Client(context.Context, int64) (*googlehealth.Client, error) {
	c := googlehealth.New(a.Health.Srv.Client())
	c.BaseURL = a.Health.Srv.URL
	return c, nil
}
func (FakeAuth) Check(context.Context, int64) (auth.TokenState, error) {
	return auth.TokenState{Status: model.CredValid}, nil
}
func (FakeAuth) MarkLinked(context.Context, int64) error         { return nil }
func (FakeAuth) MarkFailure(context.Context, int64, error) error { return nil }

// Recorder เก็บข้อความ LINE ที่ส่ง ตั้ง Fail เพื่อจำลอง LINE ล่ม
type Recorder struct {
	Sent [][]string
	Fail bool
}

func (r *Recorder) Push(_ context.Context, _ string, msgs []string) error {
	if r.Fail {
		return fmt.Errorf("LINE ล่ม")
	}
	r.Sent = append(r.Sent, msgs)
	return nil
}
