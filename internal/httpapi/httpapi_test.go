package httpapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fitbot/internal/anomaly"
	"fitbot/internal/coach"
	"fitbot/internal/completeness"
	"fitbot/internal/config"
	"fitbot/internal/httpapi"
	"fitbot/internal/model"
	"fitbot/internal/orchestrator"
	"fitbot/internal/store"
	"fitbot/internal/testutil"
)

func day(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }

// fakeRunner บันทึก Options ที่ได้รับ แทน Orchestrator จริง
type fakeRunner struct {
	got []orchestrator.Options
	err error
}

func (f *fakeRunner) Run(_ context.Context, o orchestrator.Options) ([]orchestrator.Result, error) {
	f.got = append(f.got, o)
	if f.err != nil {
		return nil, f.err
	}
	return []orchestrator.Result{{UserID: o.UserID, Date: o.Date.Format("2006-01-02"), Action: orchestrator.ActFullReport, Detail: "ส่งรายงาน 1 ข้อความ"}}, nil
}

func setup(t *testing.T) (http.Handler, *store.Store, model.User) {
	h, st, u, _ := setupRunner(t)
	return h, st, u
}

func setupRunner(t *testing.T) (http.Handler, *store.Store, model.User, *fakeRunner) {
	ctx := context.Background()
	st := store.New(testutil.DB(t, "t_httpapi"))
	u, err := st.CreateUser(ctx, model.User{DisplayName: "เอ", Timezone: "Asia/Bangkok", Status: model.UserActive,
		SleepWindowStart: 18 * 60, SleepWindowEnd: 12 * 60, ResyncGapMinutes: 120})
	if err != nil {
		t.Fatal(err)
	}
	// 10 ต.ค. สวมไม่พอ / 11 ต.ค. ไม่มีข้อมูล / 12 ต.ค. ครบพร้อมการนอน
	if err := st.SaveDay(ctx, u.ID, day(10), &model.Activity{Steps: model.Ptr(900), WearOK: model.Ptr(false)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	bed := time.Date(2026, 10, 12, 15, 55, 0, 0, time.UTC) // 22:55 เวลาไทย
	wake := bed.Add(8 * time.Hour)
	night := &model.SleepNight{Date: day(12), Bedtime: &bed, WakeTime: &wake, AsleepMin: model.Ptr(430), DeepMin: model.Ptr(60),
		LightMin: model.Ptr(280), RemMin: model.Ptr(90), AwakeMin: model.Ptr(50)}
	if err := st.SaveDay(ctx, u.ID, day(12), &model.Activity{Steps: model.Ptr(8000), WearOK: model.Ptr(true)}, nil, night); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveStatus(ctx, u.ID, day(12), completeness.Status{Activity: completeness.Complete, Sleep: completeness.Complete}); err != nil {
		t.Fatal(err)
	}
	c := &coach.Coaching{Summary: "นอนดี", Advice: []string{"เดินเพิ่ม"}, WatchExplanation: ""}
	if err := st.MarkReportSent(ctx, u.ID, day(12), bed, store.ReportSent, "รายงานเต็ม", c); err != nil {
		t.Fatal(err)
	}
	rules, _ := anomaly.LoadRules("")
	runner := &fakeRunner{}
	now := func() time.Time { return time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC) } // 10:00 วันที่ 13 เวลาไทย
	srv := &httpapi.Server{Store: st, Cfg: config.Default(), Rules: rules, Runner: runner, Log: slog.New(slog.DiscardHandler), Now: now}
	return srv.Handler(), st, u, runner
}

func get(t *testing.T, h http.Handler, url string, want int) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+url, nil))
	if rec.Code != want {
		t.Fatalf("%s: HTTP %d want %d: %s", url, rec.Code, want, rec.Body)
	}
	return rec
}

func TestDashboard(t *testing.T) {
	h, _, u := setup(t)
	var d httpapi.Dashboard
	// ไม่ระบุวัน = วันล่าสุดที่มีข้อมูล
	if err := json.Unmarshal(get(t, h, "/api/users/1/dashboard?days=3", 200).Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.User.ID != u.ID || d.Date != "2026-10-12" || *d.FirstDate != "2026-10-10" || *d.LastDate != "2026-10-12" {
		t.Fatalf("user/date: %+v %s %s %s", d.User, d.Date, *d.FirstDate, *d.LastDate)
	}
	if d.Sleep == nil || *d.Sleep.Bedtime != "22:55" || *d.Sleep.WakeTime != "06:55" || *d.Sleep.AsleepMin != 430 {
		t.Fatalf("sleep: %+v", d.Sleep)
	}
	if d.Status == nil || d.Status.Activity != completeness.Complete {
		t.Fatalf("status: %+v", d.Status)
	}
	if d.Report == nil || d.Report.Coaching == nil || d.Report.Coaching.Summary != "นอนดี" || !*d.Report.LLMUsed {
		t.Fatalf("report: %+v", d.Report)
	}
	if len(d.Series) != 3 || d.Series[0].Date != "2026-10-10" || d.Series[2].Date != "2026-10-12" {
		t.Fatalf("series: %+v", d.Series)
	}
	// FR25: วันที่สวมไม่พอ ค่ากิจกรรมเป็น null / FR4: วันที่ไม่มีข้อมูลเป็น null ไม่ใช่ 0
	if d.Series[0].Values["steps"] != nil || d.Series[1].Values["steps"] != nil || *d.Series[2].Values["steps"] != 8000 {
		t.Fatalf("steps: %v %v %v", d.Series[0].Values["steps"], d.Series[1].Values["steps"], d.Series[2].Values["steps"])
	}
	if d.Series[1].Bedtime != nil || *d.Series[2].Bedtime != "22:55" {
		t.Fatal("bedtime ใน series ไม่ถูกต้อง")
	}
	found := false
	for _, m := range d.Metrics {
		if m.Key == "steps" {
			found = m.Text == "8,000 ก้าว" && len(m.Windows) == 4
		}
	}
	if !found {
		t.Fatal("ไม่พบ steps ใน metrics หรือรูปแบบไม่ตรงกับรายงาน")
	}
}

func TestDashboardOtherDayAndErrors(t *testing.T) {
	h, _, _ := setup(t)
	var d httpapi.Dashboard
	if err := json.Unmarshal(get(t, h, "/api/users/1/dashboard?date=2026-10-11", 200).Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Sleep != nil || d.Report != nil || d.Status != nil || len(d.Series) != 30 {
		t.Fatalf("วันที่ไม่มีข้อมูลต้องว่าง: sleep=%v report=%v status=%v series=%d", d.Sleep, d.Report, d.Status, len(d.Series))
	}
	get(t, h, "/api/users/99/dashboard", 404)
	get(t, h, "/api/users/1/dashboard?date=12-10-2026", 400)
	get(t, h, "/api/users/1/dashboard?days=91", 400)

	var users []map[string]any
	if err := json.Unmarshal(get(t, h, "/api/users", 200).Body.Bytes(), &users); err != nil || len(users) != 1 {
		t.Fatalf("users: %v %v", users, err)
	}
}

func post(t *testing.T, h http.Handler, url, body string, hdr map[string]string, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787"+url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("POST %s: HTTP %d want %d: %s", url, rec.Code, want, rec.Body)
	}
	return rec
}

func TestSendReport(t *testing.T) {
	h, _, u, runner := setupRunner(t)
	var res orchestrator.Result
	rec := post(t, h, "/api/users/1/send-report", `{"date":"2026-10-12","partial":true}`, map[string]string{"Origin": "http://127.0.0.1:5173"}, 200)
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.Action != orchestrator.ActFullReport {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(runner.got) != 1 || runner.got[0].UserID != u.ID || !runner.got[0].Date.Equal(day(12)) || !runner.got[0].SendPartialNow {
		t.Fatalf("options: %+v", runner.got)
	}

	// วันนี้ (13 ต.ค. เวลาไทย) ยังไม่จบ ส่งไม่ได้
	post(t, h, "/api/users/1/send-report", `{"date":"2026-10-13"}`, nil, 400)
	post(t, h, "/api/users/1/send-report", `{"date":"13/10/2026"}`, nil, 400)
	post(t, h, "/api/users/99/send-report", `{"date":"2026-10-12"}`, nil, 404)

	runner.err = store.ErrAlreadyRunning
	post(t, h, "/api/users/1/send-report", `{"date":"2026-10-12"}`, nil, 409)
	if len(runner.got) != 2 {
		t.Fatalf("คำขอที่ไม่ผ่านการตรวจต้องไม่ถึง Orchestrator: %d", len(runner.got))
	}
}

// เว็บอื่นในเบราว์เซอร์ต้องสั่งส่งรายงานหรืออ่านข้อมูลไม่ได้
func TestGuard(t *testing.T) {
	h, _, _, runner := setupRunner(t)
	post(t, h, "/api/users/1/send-report", `{"date":"2026-10-12"}`, map[string]string{"Origin": "https://evil.example"}, 403)
	post(t, h, "/api/users/1/send-report", `{"date":"2026-10-12"}`, map[string]string{"Content-Type": "text/plain"}, 415)
	post(t, h, "/api/users/1/send-report", `{"date":"2026-10-12"}`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415)

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Host = "evil.example:8787" // DNS rebinding
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("Host ภายนอกต้องถูกปฏิเสธ: %d", rec.Code)
	}
	if len(runner.got) != 0 {
		t.Fatal("คำขอที่ถูกปฏิเสธต้องไม่ถึง Orchestrator")
	}
	get(t, h, "/api/users", 200)
}
