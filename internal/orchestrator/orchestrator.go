// Package orchestrator คือ Daily Orchestrator (F12): วนผู้ใช้ทีละคน ดึงข้อมูล ตรวจความครบ ตัดสินตามกฎ 5 ข้อ
// แล้วส่งรายงานหรือข้อความให้ sync รันกี่ครั้งก็ไม่ส่งซ้ำ ผิดพลาดที่คนหนึ่งไม่หยุดคนอื่น และมี lock กันรันซ้อน (D3)
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fitbot/internal/anomaly"
	"fitbot/internal/coach"
	"fitbot/internal/collector"
	"fitbot/internal/completeness"
	"fitbot/internal/config"
	"fitbot/internal/dateutil"
	"fitbot/internal/line"
	"fitbot/internal/llm"
	"fitbot/internal/metrics"
	"fitbot/internal/model"
	"fitbot/internal/report"
	"fitbot/internal/store"
)

type Orchestrator struct {
	Store     *store.Store
	Collector *collector.Collector
	Notifier  line.Notifier
	Cfg       *config.Config
	Rules     []anomaly.Rule
	LLM       llm.Client // nil = รายงานแบบ template อย่างเดียว
	Now       func() time.Time
	Log       *slog.Logger
	DryRun    bool // ไม่บันทึกสถานะการส่ง (Notifier แบบ dry-run พิมพ์ข้อความลงหน้าจอ)
}

type Options struct {
	Date   *time.Time // nil = เมื่อวานตาม timezone ของผู้ใช้ (FR18)
	UserID int64      // 0 = ทุกผู้ใช้ที่เปิดใช้งาน
}

type Result struct {
	UserID  int64  `json:"user_id"`
	Name    string `json:"name"`
	Date    string `json:"date"`
	Action  Action `json:"action,omitempty"`
	Detail  string `json:"detail,omitempty"`
	RetryAt string `json:"retry_at,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (o *Orchestrator) Run(ctx context.Context, opts Options) (results []Result, err error) {
	err = o.Store.WithRunLock(ctx, func() error {
		results, err = o.run(ctx, opts)
		return err
	})
	return results, err
}

func (o *Orchestrator) run(ctx context.Context, opts Options) ([]Result, error) {
	users, err := collector.ActiveUsers(ctx, o.Store, opts.UserID)
	if err != nil {
		return nil, err
	}
	jobID, err := o.Store.StartJob(ctx, map[string]any{"command": "run", "dry_run": o.DryRun})
	if err != nil {
		return nil, err
	}
	var results []Result
	failed := 0
	for _, u := range users {
		r := o.processSafe(ctx, u, opts)
		if r.Error != "" {
			failed++
			o.Log.Error("ประมวลผลผู้ใช้ไม่สำเร็จ", "user", u.ID, "err", r.Error)
		}
		results = append(results, r)
	}
	if err := o.Store.FinishJob(ctx, jobID, collector.JobStatus(failed, len(users)),
		map[string]any{"command": "run", "dry_run": o.DryRun, "users": results}); err != nil {
		o.Log.Error("บันทึก job_runs ไม่สำเร็จ", "err", err)
	}
	return results, nil
}

func (o *Orchestrator) processSafe(ctx context.Context, u model.User, opts Options) (r Result) {
	defer func() {
		if p := recover(); p != nil {
			r.Error = fmt.Sprintf("panic: %v", p)
		}
	}()
	return o.process(ctx, u, opts)
}

func (o *Orchestrator) process(ctx context.Context, u model.User, opts Options) Result {
	now := o.Now()
	loc := u.Location()
	d := dateutil.Yesterday(now, loc)
	if opts.Date != nil {
		d = *opts.Date
	}
	res := Result{UserID: u.ID, Name: u.DisplayName, Date: dateutil.Format(d)}
	fail := func(err error) Result { res.Error = err.Error(); return res }

	// 1. อ่านสถานะรายงานของวัน D ถ้าส่งแล้วจบ
	rep, err := o.Store.GetReport(ctx, u.ID, d)
	if err != nil {
		return fail(err)
	}
	if rep.State == store.ReportSent || rep.State == store.ReportPartialSent {
		res.Action, res.Detail = ActSkip, "ส่งรายงานของวันนี้แล้ว"
		return res
	}

	// 2. ดึงกิจกรรมและการนอน จัดการนอน บันทึกแบบเขียนทับ (F3–F5)
	if _, err := o.Collector.Collect(ctx, u, d); err != nil {
		_ = o.Store.SaveFetchError(ctx, u.ID, d, err.Error())
		return fail(err)
	}

	// 3. ตรวจความครบและเวลาที่สวมอุปกรณ์ (F6)
	act, night, err := o.Store.GetDay(ctx, u.ID, d)
	if err != nil {
		return fail(err)
	}
	st := completeness.Evaluate(act, night)
	if err := o.Store.SaveStatus(ctx, u.ID, d, st); err != nil {
		return fail(err)
	}
	missing := completeness.MissingParts(st)

	// 4. ตัดสินตามกฎ 5 ข้อ
	gap := time.Duration(u.ResyncGapMinutes) * time.Minute
	dec := Decide(rep, st.Complete(), now, gap)
	res.Action = dec.Action

	switch dec.Action {
	case ActSyncNotice:
		retryAt := now.Add(gap)
		msg := fmt.Sprintf("📲 ยังไม่พบข้อมูลครบของวันที่ %s (ขาด%s)\nลองเปิดแอป Google Health บนมือถือเพื่อ sync ข้อมูลนะคะ แล้ว Fitbot จะส่งรายงานให้ในรอบถัดไปหลัง %s น. ค่ะ",
			metrics.ThaiDate(d), strings.Join(missing, " และ"), retryAt.In(loc).Format("15:04"))
		if err := o.send(ctx, u, []string{msg}); err != nil {
			return fail(err)
		}
		if !o.DryRun {
			if err := o.Store.MarkSyncNoticeSent(ctx, u.ID, d, now, msg); err != nil {
				return fail(err)
			}
		}
		res.Detail = "ส่งข้อความให้ sync แล้ว ขาด" + strings.Join(missing, " และ")
		res.RetryAt = retryAt.In(loc).Format("2006-01-02 15:04")
	case ActWait:
		res.Detail = "ข้อมูลยังไม่ครบ ขาด" + strings.Join(missing, " และ") + " ยังไม่ถึงเวลาส่งรายงานบางส่วน"
		res.RetryAt = dec.RetryAt.In(loc).Format("2006-01-02 15:04")
	case ActFullReport, ActPartialReport:
		// 5–6. คำนวณ ประกอบรายงาน ส่ง แล้วอัปเดตสถานะ
		partial := dec.Action == ActPartialReport
		out, llmUsed, err := o.BuildReport(ctx, u, d, partial, missing)
		if err != nil {
			return fail(err)
		}
		if err := o.send(ctx, u, out.Messages); err != nil {
			return fail(err) // ส่งไม่สำเร็จไม่ถูกบันทึกว่าส่งแล้ว
		}
		state := store.ReportSent
		if partial {
			state = store.ReportPartialSent
		}
		if !o.DryRun {
			if err := o.Store.MarkReportSent(ctx, u.ID, d, o.Now(), state, out.Text, llmUsed); err != nil {
				return fail(err)
			}
		}
		res.Detail = fmt.Sprintf("ส่งรายงาน %d ข้อความ (AI: %v)", len(out.Messages), llmUsed)
	}
	return res
}

// BuildReport อ่านข้อมูลย้อนหลังสูงสุด 90 วันจากฐานข้อมูล คำนวณ ประเมินกฎ ขอคำแนะนำจาก AI แล้วประกอบรายงาน
// คืน llmUsed = ส่วนจาก AI ถูกใช้ในรายงานหรือไม่
func (o *Orchestrator) BuildReport(ctx context.Context, u model.User, d time.Time, partial bool, missing []string) (report.Output, bool, error) {
	rows, err := o.Store.History(ctx, u.ID, d, o.Cfg.Analysis.MaxLookbackDays)
	if err != nil {
		return report.Output{}, false, err
	}
	first, err := o.Store.FirstDataDate(ctx, u.ID)
	if err != nil {
		return report.Output{}, false, err
	}
	if fresh, err := o.Store.GetUser(ctx, u.ID); err == nil {
		u = fresh // อายุอาจเพิ่งถูกอัปเดตระหว่างดึงข้อมูล
	}
	res := metrics.Compute(rows, d, first, o.Cfg.Analysis)
	alerts := anomaly.Evaluate(res, o.Rules, o.Cfg.Analysis.BaselineMinDays)
	in := report.Input{User: u, Date: d, Partial: partial, Missing: missing, Metrics: res, Alerts: alerts, MinDays: o.Cfg.Analysis.MinDaysForAverage}
	if o.LLM != nil {
		c, err := coach.Coach(ctx, o.LLM, coach.BuildFacts(coach.Input{User: u, Partial: partial, Missing: missing, Metrics: res, Alerts: alerts}))
		if err != nil {
			o.Log.Warn("AI ใช้ไม่ได้ ใช้รายงานแบบ template แทน", "user", u.ID, "err", err)
			in.LLMFailed = true
		}
		in.Coaching = c
	}
	return report.Compose(in, o.Cfg.Report.MaxMessageRunes), in.Coaching != nil, nil
}

func (o *Orchestrator) send(ctx context.Context, u model.User, msgs []string) error {
	to := ""
	if u.LineUserID != nil {
		to = *u.LineUserID
	}
	if err := o.Notifier.Push(ctx, to, msgs); err != nil {
		return fmt.Errorf("ส่ง LINE ไม่สำเร็จ: %w", err)
	}
	return nil
}
