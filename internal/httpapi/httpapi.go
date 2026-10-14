// Package httpapi คือ HTTP API สำหรับ dashboard (fitbot-web ระยะ 1)
// ตัวเลขทุกตัวมาจาก Metrics Engine และกฎค่าผิดปกติชุดเดียวกับรายงาน LINE (D4)
// คำสั่งเดียวที่เปลี่ยนสถานะคือสั่งส่งรายงาน ซึ่งผ่าน Orchestrator และกฎ 5 ข้อเดิม
// ระยะ 1 ไม่มีระบบล็อกอิน จึงต้องฟังเฉพาะ 127.0.0.1 และกันคำขอจากเว็บอื่นด้วย guard
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"fitbot/internal/anomaly"
	"fitbot/internal/coach"
	"fitbot/internal/config"
	"fitbot/internal/dateutil"
	"fitbot/internal/metrics"
	"fitbot/internal/model"
	"fitbot/internal/orchestrator"
	"fitbot/internal/store"
)

// Runner คือ Orchestrator (แยกเป็น interface เพื่อทดสอบโดยไม่ต้องเรียก Google Health, LLM และ LINE จริง)
type Runner interface {
	Run(ctx context.Context, opts orchestrator.Options) ([]orchestrator.Result, error)
}

type Server struct {
	Store  *store.Store
	Cfg    *config.Config
	Rules  []anomaly.Rule
	Runner Runner // nil = ปิดการสั่งส่งรายงาน
	Log    *slog.Logger
	Now    func() time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/users", s.users)
	mux.HandleFunc("GET /api/users/{id}/dashboard", s.dashboard)
	mux.HandleFunc("POST /api/users/{id}/send-report", s.sendReport)
	return guard(mux)
}

// guard กันเว็บอื่นที่เปิดอยู่ในเบราว์เซอร์เรียก API นี้ (ยังไม่มีล็อกอิน)
//   - Host ต้องเป็น loopback กัน DNS rebinding อ่านข้อมูลสุขภาพ
//   - Origin ถ้ามีต้องเป็น loopback และคำขอที่เปลี่ยนสถานะต้องเป็น JSON ซึ่งเบราว์เซอร์ต้องขอ preflight
//     ก่อนส่งข้ามเว็บ (API ไม่ตอบ CORS จึงส่งไม่ได้) กันการแอบสั่งส่งรายงานด้วยฟอร์ม
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "รับเฉพาะคำขอจากเครื่องนี้ (Host)")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || !loopbackHost(u.Host) {
				writeError(w, http.StatusForbidden, "รับเฉพาะคำขอจากเครื่องนี้ (Origin)")
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "ต้องส่งเป็น application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---------- GET /api/users ----------

type userJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
}

func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context(), true)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	out := []userJSON{}
	for _, u := range users {
		out = append(out, userJSON{ID: u.ID, Name: u.DisplayName, Timezone: u.Timezone})
	}
	writeJSON(w, out)
}

// ---------- GET /api/users/{id}/dashboard?date=YYYY-MM-DD&days=N ----------

type Dashboard struct {
	User      userJSON     `json:"user"`
	Date      string       `json:"date"`       // วัน D ที่แสดง
	FirstDate *string      `json:"first_date"` // วันแรกและวันล่าสุดที่มีข้อมูล (ใช้เลื่อนวัน)
	LastDate  *string      `json:"last_date"`
	Status    *statusJSON  `json:"status"` // nil = ยังไม่เคยตรวจความครบของวันนี้
	Sleep     *sleepJSON   `json:"sleep"`  // nil = ไม่มีการนอนหลักคืนนี้
	Metrics   []metricJSON `json:"metrics"`
	Weekly    weeklyJSON   `json:"weekly"`
	Alerts    alertsJSON   `json:"alerts"`
	Report    *reportJSON  `json:"report"` // nil = ยังไม่มีรายงานหรือข้อความใดของวันนี้
	Series    []dayJSON    `json:"series"` // days วันที่สิ้นสุดที่ D เรียงเก่าไปใหม่
}

type statusJSON struct {
	Activity string `json:"activity"`
	Sleep    string `json:"sleep"`
}

type sleepJSON struct {
	Bedtime   *string `json:"bedtime"` // HH:MM ตาม timezone ของผู้ใช้
	WakeTime  *string `json:"wake_time"`
	AsleepMin *int    `json:"asleep_min"`
	DeepMin   *int    `json:"deep_min"`
	LightMin  *int    `json:"light_min"`
	RemMin    *int    `json:"rem_min"`
	AwakeMin  *int    `json:"awake_min"`
	NapMin    *int    `json:"nap_min"`
}

type metricJSON struct {
	Key     string       `json:"key"`
	Label   string       `json:"label"`
	Group   string       `json:"group"`
	Value   *float64     `json:"value"`
	Text    string       `json:"text"` // ค่าที่จัดรูปแบบแล้วเหมือนในรายงาน LINE
	Windows []windowJSON `json:"windows"`
	Trend   trendJSON    `json:"trend"`
}

type windowJSON struct {
	Days     int      `json:"days"`
	WithData int      `json:"with_data"`
	Avg      *float64 `json:"avg"`
	Text     string   `json:"text"`
}

type trendJSON struct {
	Direction string   `json:"direction"` // up | down | stable | "" = ข้อมูลไม่พอ
	PctChange *float64 `json:"pct_change"`
}

type weeklyJSON struct {
	AZM                *float64 `json:"azm"`
	ModerateEquivalent *float64 `json:"moderate_equivalent"` // นับแบบ WHO ที่นาทีหนักนับ 2 เท่า
	Days               int      `json:"days"`
}

type alertJSON struct {
	RuleID    string `json:"rule_id"`
	Metric    string `json:"metric"`
	Label     string `json:"label"`
	Value     string `json:"value"`
	Criterion string `json:"criterion"`
	Reference string `json:"reference"`
}

type alertsJSON struct {
	Watch   []alertJSON `json:"watch"`
	Improve []alertJSON `json:"improve"`
}

type reportJSON struct {
	State    string          `json:"state"`
	SentAt   *time.Time      `json:"sent_at"`
	LLMUsed  *bool           `json:"llm_used"`
	Coaching *coach.Coaching `json:"coaching"` // nil = ไม่มีบทวิเคราะห์แยกส่วน ใช้ content แทน
	Content  *string         `json:"content"`  // ข้อความที่ส่งทาง LINE
}

type dayJSON struct {
	Date     string              `json:"date"`
	Values   map[string]*float64 `json:"values"` // ตามกฎเดียวกับค่าเฉลี่ย: วันที่สวมไม่พอ ค่ากิจกรรมเป็น null (FR25)
	Bedtime  *string             `json:"bedtime"`
	WakeTime *string             `json:"wake_time"`
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("id ไม่ถูกต้อง"))
		return
	}
	maxDays := s.Cfg.Analysis.MaxLookbackDays
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if days, err = strconv.Atoi(v); err != nil || days < 1 || days > maxDays {
			s.fail(w, http.StatusBadRequest, errors.New("days ต้องอยู่ระหว่าง 1 ถึง "+strconv.Itoa(maxDays)))
			return
		}
	}
	u, err := s.Store.GetUser(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, http.StatusNotFound, errors.New("ไม่พบผู้ใช้"))
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	first, err := s.Store.FirstDataDate(ctx, id)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	last, err := s.Store.LastDataDate(ctx, id)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	// วันเริ่มต้นคือวันล่าสุดที่มีข้อมูล ถ้ายังไม่มีข้อมูลเลยใช้เมื่อวาน
	loc := u.Location()
	d := dateutil.Yesterday(s.Now(), loc)
	if last != nil {
		d = *last
	}
	if v := r.URL.Query().Get("date"); v != "" {
		if d, err = dateutil.Parse(v); err != nil {
			s.fail(w, http.StatusBadRequest, errors.New("date ต้องเป็น YYYY-MM-DD"))
			return
		}
	}

	rows, err := s.Store.History(ctx, id, d, maxDays)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	res := metrics.Compute(rows, d, first, s.Cfg.Analysis)
	watch, improve := anomaly.Split(anomaly.Evaluate(res, s.Rules, s.Cfg.Analysis.BaselineMinDays))
	st, err := s.Store.GetStatus(ctx, id, d)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	rep, err := s.Store.GetReport(ctx, id, d)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	out := Dashboard{
		User:      userJSON{ID: u.ID, Name: u.DisplayName, Timezone: u.Timezone},
		Date:      dateutil.Format(d),
		FirstDate: formatDate(first),
		LastDate:  formatDate(last),
		Weekly:    weeklyJSON{AZM: res.WeeklyAZM, ModerateEquivalent: res.WeeklyModerateEquivalent, Days: res.WeeklyDays},
		Alerts:    alertsJSON{Watch: alertList(watch), Improve: alertList(improve)},
		Series:    series(rows[len(rows)-days:], loc),
	}
	if st != nil {
		out.Status = &statusJSON{Activity: st.Activity, Sleep: st.Sleep}
	}
	if n := rows[len(rows)-1].Night; n.HasMainSleep() {
		out.Sleep = &sleepJSON{Bedtime: clock(n.Bedtime, loc), WakeTime: clock(n.WakeTime, loc), AsleepMin: n.AsleepMin,
			DeepMin: n.DeepMin, LightMin: n.LightMin, RemMin: n.RemMin, AwakeMin: n.AwakeMin, NapMin: n.NapMin}
	}
	for _, m := range res.Metrics {
		mj := metricJSON{Key: m.Def.Key, Label: m.Def.Label, Group: m.Def.Group, Value: m.Today, Text: m.Def.Fmt(m.Today),
			Trend: trendJSON{Direction: m.Trend.Direction, PctChange: m.Trend.PctChange}}
		for _, win := range m.Windows {
			mj.Windows = append(mj.Windows, windowJSON{Days: win.Days, WithData: win.WithData, Avg: win.Avg, Text: m.Def.Fmt(win.Avg)})
		}
		out.Metrics = append(out.Metrics, mj)
	}
	if rep.State != store.ReportPending {
		out.Report = &reportJSON{State: rep.State, SentAt: rep.ReportSentAt, LLMUsed: rep.LLMUsed, Coaching: rep.Coaching, Content: rep.Content}
	}
	writeJSON(w, out)
}

// ---------- POST /api/users/{id}/send-report {"date": "YYYY-MM-DD", "partial": bool} ----------

type sendRequest struct {
	Date    string `json:"date"`
	Partial bool   `json:"partial"` // ข้อมูลไม่ครบก็ส่งรายงานบางส่วนเลย
}

// sendReport สั่ง Orchestrator ทำงานแบบเดียวกับรอบอัตโนมัติ: ดึงข้อมูลล่าสุด ตรวจความครบ แล้วตัดสินตามกฎ 5 ข้อ
// partial=false และข้อมูลไม่ครบ → ส่งข้อความให้ sync หรือรอ ตามกฎเดิม
// ส่งแล้วไม่ส่งซ้ำ (กฎ 1) ใช้ lock เดียวกับ launchd จึงไม่รันซ้อนกัน
func (s *Server) sendReport(w http.ResponseWriter, r *http.Request) {
	if s.Runner == nil {
		s.fail(w, http.StatusNotImplemented, errors.New("การสั่งส่งรายงานถูกปิดอยู่"))
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("id ไม่ถูกต้อง"))
		return
	}
	var req sendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("body ต้องเป็น JSON {\"date\": \"YYYY-MM-DD\"}"))
		return
	}
	d, err := dateutil.Parse(req.Date)
	if err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("date ต้องเป็น YYYY-MM-DD"))
		return
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, http.StatusNotFound, errors.New("ไม่พบผู้ใช้"))
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if u.Status != model.UserActive {
		s.fail(w, http.StatusConflict, errors.New("ผู้ใช้นี้ถูกปิดใช้งานอยู่"))
		return
	}
	// วันนี้ยังไม่จบ ข้อมูลยังไม่ครบแน่นอน
	if !d.Before(dateutil.Yesterday(s.Now(), u.Location()).AddDate(0, 0, 1)) {
		s.fail(w, http.StatusBadRequest, errors.New("ส่งรายงานได้เฉพาะวันที่ผ่านมาแล้ว"))
		return
	}
	// ปิดหน้าเว็บกลางทางต้องไม่ตัดการทำงานระหว่างส่ง LINE กับบันทึกสถานะ (กันส่งซ้ำ)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	results, err := s.Runner.Run(ctx, orchestrator.Options{Date: &d, UserID: id, SendPartialNow: req.Partial})
	if errors.Is(err, store.ErrAlreadyRunning) {
		s.fail(w, http.StatusConflict, errors.New("ระบบกำลังทำงานรอบอัตโนมัติอยู่ ลองใหม่อีกสักครู่"))
		return
	}
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	if len(results) != 1 {
		s.fail(w, http.StatusInternalServerError, errors.New("ไม่พบผลของผู้ใช้นี้"))
		return
	}
	if results[0].Error != "" {
		s.fail(w, http.StatusBadGateway, errors.New(results[0].Error))
		return
	}
	writeJSON(w, results[0])
}

func series(rows []metrics.DayRow, loc *time.Location) []dayJSON {
	out := make([]dayJSON, len(rows))
	for i, row := range rows {
		out[i] = dayJSON{Date: dateutil.Format(row.Date), Values: map[string]*float64{}}
		if row.Night.HasMainSleep() {
			out[i].Bedtime, out[i].WakeTime = clock(row.Night.Bedtime, loc), clock(row.Night.WakeTime, loc)
		}
	}
	for _, def := range metrics.Defs {
		for i, v := range metrics.Values(rows, def.Key) {
			out[i].Values[def.Key] = v
		}
	}
	return out
}

func alertList(alerts []anomaly.Alert) []alertJSON {
	out := []alertJSON{}
	for _, a := range alerts {
		out = append(out, alertJSON{RuleID: a.RuleID, Metric: a.Metric, Label: a.Label, Value: a.Value, Criterion: a.Criterion, Reference: a.Reference})
	}
	return out
}

func clock(t *time.Time, loc *time.Location) *string {
	if t == nil {
		return nil
	}
	return model.Ptr(t.In(loc).Format("15:04"))
}

func formatDate(d *time.Time) *string {
	if d == nil {
		return nil
	}
	return model.Ptr(dateutil.Format(*d))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, code int, err error) {
	if code >= 500 {
		s.Log.Error("API ผิดพลาด", "err", err)
	}
	writeError(w, code, err.Error())
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
