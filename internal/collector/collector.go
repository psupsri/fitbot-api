// Package collector เก็บข้อมูลรายวันของทุกผู้ใช้ (งานของ M1: F3 + F4 + F5)
// ดึงข้อมูลวัน D จัดการนอน แล้วบันทึกแบบเขียนทับ วนผู้ใช้ทีละคน ผิดพลาดที่คนหนึ่งไม่หยุดคนอื่น
// การตัดสินเรื่องรายงานตามกฎ 5 ข้อเป็นงานของ Daily Orchestrator (F12) ใน M2
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"fitbot/internal/auth"
	"fitbot/internal/config"
	"fitbot/internal/dateutil"
	"fitbot/internal/googlehealth"
	"fitbot/internal/line"
	"fitbot/internal/model"
	"fitbot/internal/sleep"
	"fitbot/internal/store"
)

const minutesPerDay = 24 * 60

// Auth คือส่วนของ auth.Manager ที่ collector ใช้
type Auth interface {
	Client(ctx context.Context, userID int64) (*googlehealth.Client, error)
	Check(ctx context.Context, userID int64) (auth.TokenState, error)
	MarkLinked(ctx context.Context, userID int64) error
	MarkFailure(ctx context.Context, userID int64, err error) error
}

type Collector struct {
	Store    *store.Store
	Auth     Auth
	Notifier line.Notifier
	Cfg      *config.Config
	Now      func() time.Time
	Log      *slog.Logger
	DryRun   bool // ไม่บันทึกว่าเตือนแล้ว (ข้อความถูกพิมพ์ลงหน้าจอโดย Notifier แบบ dry-run)
}

type Options struct {
	Date   *time.Time // nil = เมื่อวานตาม timezone ของผู้ใช้ (FR18)
	UserID int64      // 0 = ทุกผู้ใช้ที่เปิดใช้งาน
}

type Result struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Date   string `json:"date"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Run เก็บข้อมูลของผู้ใช้ทุกคน (หรือคนเดียว) ภายใต้ lock และบันทึก job_runs
func (c *Collector) Run(ctx context.Context, opts Options) (results []Result, err error) {
	err = c.Store.WithRunLock(ctx, func() error {
		results, err = c.run(ctx, opts)
		return err
	})
	return results, err
}

func (c *Collector) run(ctx context.Context, opts Options) ([]Result, error) {
	users, err := ActiveUsers(ctx, c.Store, opts.UserID)
	if err != nil {
		return nil, err
	}
	jobID, err := c.Store.StartJob(ctx, map[string]any{"command": "collect"})
	if err != nil {
		return nil, err
	}
	var results []Result
	failed := 0
	for _, u := range users {
		d := dateutil.Yesterday(c.Now(), u.Location())
		if opts.Date != nil {
			d = *opts.Date
		}
		r := Result{UserID: u.ID, Name: u.DisplayName, Date: dateutil.Format(d)}
		detail, err := c.collectSafe(ctx, u, d)
		r.Detail = detail
		if err != nil {
			failed++
			r.Error = err.Error()
			c.Log.Error("เก็บข้อมูลไม่สำเร็จ", "user", u.ID, "date", r.Date, "err", err)
		}
		results = append(results, r)
	}
	if err := c.Store.FinishJob(ctx, jobID, JobStatus(failed, len(users)), map[string]any{"command": "collect", "users": results}); err != nil {
		c.Log.Error("บันทึก job_runs ไม่สำเร็จ", "err", err)
	}
	return results, nil
}

// ActiveUsers คืนผู้ใช้ที่เปิดใช้งานทั้งหมด หรือคนเดียวถ้าระบุ id (ผู้ใช้ที่ปิดไม่ถูกประมวลผล)
func ActiveUsers(ctx context.Context, st *store.Store, id int64) ([]model.User, error) {
	if id == 0 {
		return st.ListUsers(ctx, true)
	}
	u, err := st.GetUser(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ผู้ใช้ %d: %w", id, err)
	}
	if u.Status != model.UserActive {
		return nil, fmt.Errorf("ผู้ใช้ %d ถูกปิดการใช้งาน", id)
	}
	return []model.User{u}, nil
}

// JobStatus สรุปสถานะของ job_runs จากจำนวนผู้ใช้ที่ล้มเหลว
func JobStatus(failed, total int) string {
	switch {
	case failed > 0 && failed == total:
		return "failed"
	case failed > 0:
		return "partial_failure"
	}
	return "success"
}

// collectSafe กัน panic ของผู้ใช้คนหนึ่งไม่ให้หยุดคนอื่น
func (c *Collector) collectSafe(ctx context.Context, u model.User, d time.Time) (detail string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return c.Collect(ctx, u, d)
}

// Collect ดึงและบันทึกข้อมูลวัน D ของผู้ใช้หนึ่งคน
func (c *Collector) Collect(ctx context.Context, u model.User, d time.Time) (string, error) {
	ts, err := c.Auth.Check(ctx, u.ID)
	if err != nil {
		return "", err
	}
	if ts.ShouldWarn && ts.ExpiresAt != nil {
		c.warnExpiry(ctx, u, *ts.ExpiresAt)
	}
	if ts.Status == model.CredInvalid {
		c.notifyAuthProblem(ctx, u)
		return "", fmt.Errorf("%w: ให้รัน `fitbot auth --user %d`", googlehealth.ErrUnauthorized, u.ID)
	}

	gh, err := c.Auth.Client(ctx, u.ID)
	if err != nil {
		return "", c.authFailed(ctx, u, err)
	}
	c.refreshAge(ctx, gh, u)

	w := sleep.WindowFor(u, c.Cfg.Sleep.MergeGapMinutes)
	from, to := w.FetchRange(d)
	data, err := gh.FetchDay(ctx, d, u.Location(), from, to)
	if err != nil {
		return "", c.authFailed(ctx, u, err)
	}
	_ = c.Auth.MarkLinked(ctx, u.ID)

	a := data.Activity
	if a.WearMinutes != nil {
		a.WearOK = model.Ptr(float64(*a.WearMinutes) >= c.Cfg.Wear.MinRatio*minutesPerDay)
	}
	sessions, night := sleep.Attribute(d, data.Sessions, w, data.Night)
	if err := c.Store.SaveDay(ctx, u.ID, d, a, sessions, night); err != nil {
		return "", err
	}
	return Describe(a, night), nil
}

// authFailed บันทึกสถานะ token และแจ้งทาง LINE เมื่อ error เป็นเรื่องสิทธิ์ แล้วคืน error เดิม
func (c *Collector) authFailed(ctx context.Context, u model.User, err error) error {
	if errors.Is(err, googlehealth.ErrUnauthorized) || errors.Is(err, googlehealth.ErrAccountNotLinked) {
		if merr := c.Auth.MarkFailure(ctx, u.ID, err); merr != nil {
			c.Log.Error("บันทึกสถานะ token ไม่สำเร็จ", "user", u.ID, "err", merr)
		}
		c.notifyAuthProblem(ctx, u)
	}
	return err
}

// refreshAge อัปเดตอายุจาก profile อย่างน้อยทุก age_refresh_days วัน (FR22) ผิดพลาดไม่หยุดการทำงาน
func (c *Collector) refreshAge(ctx context.Context, gh *googlehealth.Client, u model.User) {
	if u.AgeUpdatedAt != nil && c.Now().Sub(*u.AgeUpdatedAt) < time.Duration(c.Cfg.Google.AgeRefreshDays)*24*time.Hour {
		return
	}
	age, err := gh.Profile(ctx)
	if err != nil || age == nil {
		c.Log.Warn("อ่านอายุจาก profile ไม่ได้ ใช้เกณฑ์ผู้ใหญ่ทั่วไป", "user", u.ID, "err", err)
		return
	}
	if err := c.Store.UpdateUserAge(ctx, u.ID, *age); err != nil {
		c.Log.Warn("บันทึกอายุไม่สำเร็จ", "user", u.ID, "err", err)
	}
}

func (c *Collector) send(ctx context.Context, u model.User, msg string) error {
	to := ""
	if u.LineUserID != nil {
		to = *u.LineUserID
	}
	return c.Notifier.Push(ctx, to, []string{msg})
}

// warnExpiry เตือนทาง LINE เมื่อ refresh token เหลือไม่เกิน warn_before_hours (D8) เตือนครั้งเดียวต่อ token
func (c *Collector) warnExpiry(ctx context.Context, u model.User, exp time.Time) {
	msg := fmt.Sprintf("🔑 แจ้งเตือนจาก Fitbot: สิทธิ์เข้าถึง Google Health จะหมดอายุ %s น.\nกรุณารัน `fitbot auth --user %d` บนเครื่องเพื่อเชื่อมต่อใหม่นะคะ ถ้าหมดอายุแล้ว ข้อมูลของวันที่ดึงไม่ได้จะหายถาวรค่ะ",
		exp.In(u.Location()).Format("2006-01-02 15:04"), u.ID)
	if err := c.send(ctx, u, msg); err != nil {
		c.Log.Warn("ส่งเตือน token ใกล้หมดอายุไม่สำเร็จ", "user", u.ID, "err", err)
		return
	}
	if !c.DryRun {
		_ = c.Store.MarkExpiryWarned(ctx, u.ID, c.Now())
	}
}

// notifyAuthProblem แจ้งทาง LINE เมื่อ token ใช้ไม่ได้หรือบัญชียังไม่เชื่อม (D7) แจ้งครั้งเดียวจนกว่าจะ re-auth
func (c *Collector) notifyAuthProblem(ctx context.Context, u model.User) {
	ts, err := c.Auth.Check(ctx, u.ID)
	if err != nil || !ts.ShouldNotify {
		return
	}
	msg := fmt.Sprintf("🔑 Fitbot เข้าถึงข้อมูล Google Health ไม่ได้ (สิทธิ์หมดอายุหรือถูกเพิกถอน)\nกรุณารัน `fitbot auth --user %d` บนเครื่องเพื่อเชื่อมต่อใหม่นะคะ", u.ID)
	if ts.Status == model.CredNotLinked {
		msg = "🔑 Fitbot เข้าถึงข้อมูลไม่ได้ เพราะบัญชี Google ยังไม่ได้เชื่อมกับแอป Google Health\nกรุณาเปิดแอปแล้วเชื่อมบัญชี จากนั้นรัน Fitbot อีกครั้งนะคะ"
	}
	if err := c.send(ctx, u, msg); err != nil {
		c.Log.Warn("แจ้งปัญหา token ไม่สำเร็จ", "user", u.ID, "err", err)
		return
	}
	if !c.DryRun {
		_ = c.Store.MarkInvalidNotified(ctx, u.ID, c.Now())
	}
}

// Describe สรุปสั้นๆ ของสิ่งที่บันทึก
func Describe(a *model.Activity, n *model.SleepNight) string {
	f := func(p *int, unit string) string {
		if p == nil {
			return "ไม่มีข้อมูล"
		}
		return fmt.Sprintf("%d%s", *p, unit)
	}
	wear := "ไม่มีข้อมูล"
	if a != nil && a.WearMinutes != nil {
		wear = fmt.Sprintf("%.0f%%", float64(*a.WearMinutes)*100/minutesPerDay)
	}
	var steps, asleep *int
	if a != nil {
		steps = a.Steps
	}
	if n != nil && n.HasMainSleep() {
		asleep = n.AsleepMin
	}
	return fmt.Sprintf("ก้าว %s · นอน %s · สวม %s", f(steps, ""), f(asleep, " นาที"), wear)
}
