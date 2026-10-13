package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fitbot/internal/auth"
	"fitbot/internal/config"
	"fitbot/internal/googlehealth"
	"fitbot/internal/model"
	"fitbot/internal/store"
	"fitbot/internal/testutil"
)

// fakeAPI ตอบเหมือน Google Health API แบบย่อ; steps เปลี่ยนได้เพื่อทดสอบการเขียนทับ
type fakeAPI struct{ steps int }

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.Contains(p, "/steps/") && strings.HasSuffix(p, ":dailyRollUp"):
		fmt.Fprintf(w, `{"rollupDataPoints":[{"steps":{"countSum":"%d"}}]}`, f.steps)
	case strings.HasSuffix(p, ":dailyRollUp"):
		io.WriteString(w, `{}`) // ไม่มีข้อมูล → ต้องเป็น NULL
	case strings.Contains(p, "/heart-rate/"):
		io.WriteString(w, `{"dataPoints":[{"heartRate":{"sampleTime":{"physicalTime":"2026-10-12T01:00:00Z"}}}]}`)
	case strings.Contains(p, "/sleep/"):
		io.WriteString(w, `{"dataPoints":[{"name":"s1","sleep":{"interval":{"startTime":"2026-10-12T16:10:00Z","endTime":"2026-10-12T23:40:00Z"},"summary":{"minutesAsleep":"442"}}}]}`)
	case strings.HasSuffix(p, "/profile"):
		io.WriteString(w, `{"age":40}`)
	default:
		io.WriteString(w, `{"dataPoints":[]}`)
	}
}

// fakeAuth: ผู้ใช้ที่อยู่ใน broken ใช้ token ไม่ได้
type fakeAuth struct {
	srv    *httptest.Server
	broken map[int64]bool
	failed []int64
}

func (a *fakeAuth) Client(_ context.Context, id int64) (*googlehealth.Client, error) {
	if a.broken[id] {
		return nil, googlehealth.ErrUnauthorized
	}
	c := googlehealth.New(a.srv.Client())
	c.BaseURL = a.srv.URL
	return c, nil
}
func (a *fakeAuth) Check(_ context.Context, id int64) (auth.TokenState, error) {
	if a.broken[id] {
		return auth.TokenState{Status: model.CredNotLinked, ShouldNotify: true}, nil
	}
	return auth.TokenState{Status: model.CredValid}, nil
}
func (a *fakeAuth) MarkLinked(context.Context, int64) error { return nil }
func (a *fakeAuth) MarkFailure(_ context.Context, id int64, _ error) error {
	a.failed = append(a.failed, id)
	return nil
}

type recorder struct{ to []string }

func (r *recorder) Push(_ context.Context, to string, _ []string) error {
	r.to = append(r.to, to)
	return nil
}

func TestCollect(t *testing.T) {
	ctx := context.Background()
	st := store.New(testutil.DB(t, "t_collector"))
	api := &fakeAPI{steps: 5000}
	srv := httptest.NewServer(api)
	defer srv.Close()

	mk := func(name string, status string) model.User {
		lineID := "U-" + name
		u, err := st.CreateUser(ctx, model.User{DisplayName: name, Timezone: "Asia/Bangkok", Status: status, LineUserID: &lineID,
			SleepWindowStart: 18 * 60, SleepWindowEnd: 12 * 60, ResyncGapMinutes: 120})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	ok, broken, disabled := mk("ok", model.UserActive), mk("broken", model.UserActive), mk("off", model.UserDisabled)

	fa := &fakeAuth{srv: srv, broken: map[int64]bool{broken.ID: true}}
	rec := &recorder{}
	c := &Collector{Store: st, Auth: fa, Notifier: rec, Cfg: config.Default(), Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC) }}

	res, err := c.Run(ctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// AC F2: ผู้ใช้ที่ปิดไม่ถูกประมวลผล / AC F3: token ใช้ไม่ได้ไม่กระทบผู้ใช้อื่น
	if len(res) != 2 || res[0].Error != "" || res[1].Error == "" {
		t.Fatalf("results = %+v", res)
	}
	for _, r := range res {
		if r.UserID == disabled.ID {
			t.Fatal("ผู้ใช้ที่ปิดต้องไม่ถูกประมวลผล")
		}
	}
	if len(rec.to) != 1 || rec.to[0] != "U-broken" || len(fa.failed) != 1 {
		t.Fatalf("ต้องแจ้งทาง LINE ผู้ใช้ที่ token ใช้ไม่ได้: %v", rec.to)
	}

	d := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) // เมื่อวานของ 13 ต.ค. เวลาไทย
	a, n, err := st.GetDay(ctx, ok.ID, d)
	if err != nil {
		t.Fatal(err)
	}
	// AC F4: ไม่มีค่าเป็น NULL ไม่ใช่ 0
	if *a.Steps != 5000 || a.EnergyKcal != nil || a.RestingHR != nil || *a.WearMinutes != 1 || *a.WearOK {
		t.Fatalf("activity = %+v", a)
	}
	if *n.AsleepMin != 442 || n.HRVms != nil {
		t.Fatalf("night = %+v", n)
	}
	if u, _ := st.GetUser(ctx, ok.ID); u.Age == nil || *u.Age != 40 {
		t.Fatal("อายุต้องถูกอัปเดตจาก profile (FR22)")
	}

	// AC F4: ดึงซ้ำเขียนทับ ไม่ซ้ำ
	api.steps = 7350
	if _, err := c.Run(ctx, Options{UserID: ok.ID, Date: &d}); err != nil {
		t.Fatal(err)
	}
	a, _, _ = st.GetDay(ctx, ok.ID, d)
	var rows, sessions int
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM daily_activity WHERE user_id = $1`, ok.ID).Scan(&rows)
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM sleep_sessions WHERE user_id = $1`, ok.ID).Scan(&sessions)
	if *a.Steps != 7350 || rows != 1 || sessions != 1 {
		t.Fatalf("steps=%d rows=%d sessions=%d", *a.Steps, rows, sessions)
	}
	var jobs int
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM job_runs WHERE finished_at IS NOT NULL`).Scan(&jobs)
	if jobs != 2 {
		t.Fatalf("job_runs = %d", jobs)
	}
}

// AC D3: lock กันรันซ้อน
func TestLockPreventsConcurrentRun(t *testing.T) {
	ctx := context.Background()
	st := store.New(testutil.DB(t, "t_collector_lock"))
	c := &Collector{Store: st, Cfg: config.Default(), Log: slog.New(slog.DiscardHandler), Now: time.Now}
	err := st.WithRunLock(ctx, func() error {
		_, err := c.Run(ctx, Options{})
		return err
	})
	if err != store.ErrAlreadyRunning {
		t.Fatalf("err = %v", err)
	}
}
