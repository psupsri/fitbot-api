package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"fitbot/internal/anomaly"
	"fitbot/internal/collector"
	"fitbot/internal/config"
	"fitbot/internal/model"
	"fitbot/internal/store"
	"fitbot/internal/testutil"
)

func TestDecideFiveRules(t *testing.T) {
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	cases := []struct {
		name     string
		r        store.Report
		complete bool
		want     Action
	}{
		{"1 ส่งรายงานแล้ว", store.Report{State: store.ReportSent}, true, ActSkip},
		{"1 ส่งรายงานบางส่วนแล้ว", store.Report{State: store.ReportPartialSent}, false, ActSkip},
		{"2 ครบรอบแรก", store.Report{State: store.ReportPending}, true, ActFullReport},
		{"2 ครบหลังแจ้ง sync", store.Report{State: store.ReportSyncNoticeSent, SyncNoticeSentAt: ago(10 * time.Minute)}, true, ActFullReport},
		{"3 ยังไม่เคยแจ้ง", store.Report{State: store.ReportPending}, false, ActSyncNotice},
		{"4 แจ้งแล้ว ไม่ถึง 2 ชม.", store.Report{State: store.ReportSyncNoticeSent, SyncNoticeSentAt: ago(119 * time.Minute)}, false, ActWait},
		{"5 แจ้งแล้ว ครบ 2 ชม.", store.Report{State: store.ReportSyncNoticeSent, SyncNoticeSentAt: ago(2 * time.Hour)}, false, ActPartialReport},
	}
	for _, c := range cases {
		if got := Decide(c.r, c.complete, now, 2*time.Hour); got.Action != c.want {
			t.Errorf("%s: got %s want %s", c.name, got.Action, c.want)
		}
	}
	sent := now.Add(-30 * time.Minute)
	if got := Decide(store.Report{State: store.ReportSyncNoticeSent, SyncNoticeSentAt: &sent}, false, now, 2*time.Hour); !got.RetryAt.Equal(sent.Add(2 * time.Hour)) {
		t.Fatalf("retry at %v", got.RetryAt)
	}
}

type env struct {
	o      *Orchestrator
	health *testutil.FakeHealth
	line   *testutil.Recorder
	clock  time.Time
	st     *store.Store
	user   model.User
}

func setup(t *testing.T, schema string) *env {
	ctx := context.Background()
	st := store.New(testutil.DB(t, schema))
	lineID := "U1"
	u, err := st.CreateUser(ctx, model.User{DisplayName: "เอ", Timezone: "Asia/Bangkok", Status: model.UserActive, LineUserID: &lineID,
		SleepWindowStart: 18 * 60, SleepWindowEnd: 12 * 60, ResyncGapMinutes: 120})
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := anomaly.LoadRules("")
	e := &env{health: testutil.NewFakeHealth(t), line: &testutil.Recorder{}, st: st, user: u,
		clock: time.Date(2026, 10, 13, 0, 30, 0, 0, time.UTC)} // 07:30 เวลาไทย
	cfg := config.Default()
	log := slog.New(slog.DiscardHandler)
	now := func() time.Time { return e.clock }
	c := &collector.Collector{Store: st, Auth: testutil.FakeAuth{Health: e.health}, Notifier: e.line, Cfg: cfg, Now: now, Log: log}
	e.o = &Orchestrator{Store: st, Collector: c, Notifier: e.line, Cfg: cfg, Rules: rules, Now: now, Log: log}
	return e
}

func (e *env) run(t *testing.T) Result {
	t.Helper()
	res, err := e.o.Run(context.Background(), Options{})
	if err != nil || len(res) != 1 {
		t.Fatalf("res=%v err=%v", res, err)
	}
	return res[0]
}

// ทดลองตาม M2: รันก่อน sync → รันซ้ำทันที → รันหลัง 2 ชม. → รันอีก
func TestRunBeforeSyncThenPartial(t *testing.T) {
	e := setup(t, "t_orch_partial")
	e.health.Set(func(f *testutil.FakeHealth) { f.HasSleep = false })

	if r := e.run(t); r.Action != ActSyncNotice || r.RetryAt != "2026-10-13 09:30" || len(e.line.Sent) != 1 {
		t.Fatalf("รอบแรกต้องส่งข้อความให้ sync: %+v", r)
	}
	if !strings.Contains(e.line.Sent[0][0], "ขาดข้อมูลการนอน") {
		t.Fatalf("ต้องระบุว่าขาดอะไร: %s", e.line.Sent[0][0])
	}

	e.clock = e.clock.Add(30 * time.Minute)
	if r := e.run(t); r.Action != ActWait || r.RetryAt != "2026-10-13 09:30" || len(e.line.Sent) != 1 {
		t.Fatalf("รันซ้ำทันทีต้องไม่ส่งและบอกเวลาที่ควรรันใหม่: %+v", r)
	}

	e.clock = e.clock.Add(90 * time.Minute)
	if r := e.run(t); r.Action != ActPartialReport || len(e.line.Sent) != 2 {
		t.Fatalf("ครบ 2 ชม. ต้องส่งรายงานบางส่วน: %+v", r)
	}
	if !strings.Contains(strings.Join(e.line.Sent[1], ""), "รายงานนี้ยังไม่ครบ") {
		t.Fatal("รายงานบางส่วนต้องบอกว่าข้อมูลไม่ครบ")
	}

	e.clock = e.clock.Add(3 * time.Hour)
	e.health.Set(func(f *testutil.FakeHealth) { f.HasSleep = true })
	if r := e.run(t); r.Action != ActSkip || len(e.line.Sent) != 2 {
		t.Fatalf("รายงานต้องไม่เกิน 1 ฉบับต่อวัน: %+v", r)
	}
}

func TestRunCompleteSendsFullReportOnce(t *testing.T) {
	e := setup(t, "t_orch_full")
	if r := e.run(t); r.Action != ActFullReport || len(e.line.Sent) != 1 {
		t.Fatalf("%+v", r)
	}
	text := strings.Join(e.line.Sent[0], "\n")
	if !strings.Contains(text, "เดิน 7,350 ก้าว") || !strings.Contains(text, "หลับจริงรวม 7 ชม. 22 นาที") || strings.Contains(text, "ยังไม่ครบ:") || strings.Contains(text, "ไม่มีข้อมูล)") {
		t.Fatalf("รายงาน:\n%s", text)
	}
	if r := e.run(t); r.Action != ActSkip || len(e.line.Sent) != 1 {
		t.Fatal("รันซ้ำต้องไม่ส่งซ้ำ")
	}
}

// FR25: สวมไม่ถึงครึ่งวันถือว่าไม่ครบ
func TestInsufficientWearIsIncomplete(t *testing.T) {
	e := setup(t, "t_orch_wear")
	e.health.Set(func(f *testutil.FakeHealth) { f.WearMinutes = 600 })
	if r := e.run(t); r.Action != ActSyncNotice || !strings.Contains(e.line.Sent[0][0], "สวมอุปกรณ์ไม่ถึงครึ่งวัน") {
		t.Fatalf("%+v %v", r, e.line.Sent)
	}
}

// AC F11: ส่งไม่สำเร็จไม่ถูกบันทึกว่าส่งแล้ว
func TestLineFailureNotMarkedSent(t *testing.T) {
	e := setup(t, "t_orch_linefail")
	e.line.Fail = true
	if r := e.run(t); r.Error == "" {
		t.Fatal("ต้องคืน error")
	}
	if rep, _ := e.st.GetReport(context.Background(), e.user.ID, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)); rep.State != store.ReportPending {
		t.Fatalf("state = %s", rep.State)
	}
	e.line.Fail = false
	if r := e.run(t); r.Action != ActFullReport || len(e.line.Sent) != 1 {
		t.Fatalf("รอบถัดไปต้องส่งได้: %+v", r)
	}
}

func TestDryRunDoesNotChangeState(t *testing.T) {
	e := setup(t, "t_orch_dry")
	e.o.DryRun = true
	e.run(t)
	if rep, _ := e.st.GetReport(context.Background(), e.user.ID, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)); rep.State != store.ReportPending {
		t.Fatal("dry-run ต้องไม่เปลี่ยนสถานะ")
	}
}
