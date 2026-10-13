// Package metrics คือ Metrics Engine (F7): ค่าเฉลี่ยที่ข้ามวันที่ไม่มีข้อมูล จำนวนวันที่มีข้อมูล และแนวโน้ม
// ตัวเลขทุกตัวคำนวณด้วยโค้ด ไม่ใช้ LLM ผลคงที่กับข้อมูลชุดเดิม
package metrics

import (
	"fmt"
	"math"
	"time"

	"fitbot/internal/model"
)

const (
	GroupSleep    = "sleep"
	GroupActivity = "activity"

	TrendUp     = "up"
	TrendDown   = "down"
	TrendStable = "stable"
)

// Config คือค่าที่ปรับได้ในไฟล์ตั้งค่า (ค่าตั้งต้นตาม Q12–Q14: 3 / 14 / 14 วัน)
type Config struct {
	Windows           []int   `yaml:"windows"`
	MaxLookbackDays   int     `yaml:"max_lookback_days"`
	MinDaysForAverage int     `yaml:"min_days_for_average"`
	TrendMinDays      int     `yaml:"trend_min_days"`
	TrendThresholdPct float64 `yaml:"trend_threshold_pct"`
	BaselineMinDays   int     `yaml:"baseline_min_days"`
}

func DefaultConfig() Config {
	return Config{Windows: []int{7, 30, 60, 90}, MaxLookbackDays: 90, MinDaysForAverage: 3, TrendMinDays: 14, TrendThresholdPct: 5, BaselineMinDays: 14}
}

// DayRow คือข้อมูลหนึ่งวันจากฐานข้อมูล (nil = ไม่มีแถว)
type DayRow struct {
	Date     time.Time
	Activity *model.Activity
	Night    *model.SleepNight
}

// Def คือนิยามตัวชี้วัดหนึ่งตัว
type Def struct {
	Key    string
	Label  string
	Group  string
	Format func(float64) string
	get    func(DayRow) *float64
}

// Fmt แสดงค่าหรือ "ไม่มีข้อมูล" (ห้ามแสดง 0 แทนค่าที่ไม่มี)
func (d Def) Fmt(v *float64) string {
	if v == nil {
		return "ไม่มีข้อมูล"
	}
	return d.Format(*v)
}

func activity(f func(*model.Activity) *float64) func(DayRow) *float64 {
	return func(r DayRow) *float64 {
		// วันที่สวมไม่ถึงเกณฑ์หรือไม่รู้เวลาสวม ค่ากิจกรรมเป็น "ไม่มีข้อมูล" (FR25)
		if r.Activity == nil || r.Activity.WearOK == nil || !*r.Activity.WearOK {
			return nil
		}
		return f(r.Activity)
	}
}

func night(f func(*model.SleepNight) *float64) func(DayRow) *float64 {
	return func(r DayRow) *float64 {
		if r.Night == nil {
			return nil
		}
		return f(r.Night)
	}
}

// mainSleep: ค่าการนอนหลัก ถ้าคืนนั้นมีแต่การงีบถือว่าไม่มีข้อมูล
func mainSleep(f func(*model.SleepNight) *float64) func(DayRow) *float64 {
	return night(func(n *model.SleepNight) *float64 {
		if !n.HasMainSleep() {
			return nil
		}
		return f(n)
	})
}

func fi(p *int) *float64 {
	if p == nil {
		return nil
	}
	return model.Ptr(float64(*p))
}

func sumPtr(ps ...*int) *float64 {
	var s float64
	found := false
	for _, p := range ps {
		if p != nil {
			s += float64(*p)
			found = true
		}
	}
	if !found {
		return nil
	}
	return &s
}

var Defs = []Def{
	{Key: "asleep_min", Label: "เวลานอนหลับ", Group: GroupSleep, Format: FormatDuration, get: mainSleep(func(n *model.SleepNight) *float64 { return fi(n.AsleepMin) })},
	{Key: "deep_min", Label: "หลับลึก", Group: GroupSleep, Format: FormatDuration, get: mainSleep(func(n *model.SleepNight) *float64 { return fi(n.DeepMin) })},
	{Key: "light_min", Label: "หลับตื้น", Group: GroupSleep, Format: FormatDuration, get: mainSleep(func(n *model.SleepNight) *float64 { return fi(n.LightMin) })},
	{Key: "rem_min", Label: "REM", Group: GroupSleep, Format: FormatDuration, get: mainSleep(func(n *model.SleepNight) *float64 { return fi(n.RemMin) })},
	{Key: "awake_min", Label: "ตื่นระหว่างคืน", Group: GroupSleep, Format: FormatDuration, get: mainSleep(func(n *model.SleepNight) *float64 { return fi(n.AwakeMin) })},
	{Key: "nap_min", Label: "งีบ", Group: GroupSleep, Format: FormatDuration, get: night(func(n *model.SleepNight) *float64 { return fi(n.NapMin) })},
	{Key: "hrv_ms", Label: "HRV", Group: GroupSleep, Format: unit("%.0f", " ms"), get: night(func(n *model.SleepNight) *float64 { return n.HRVms })},
	{Key: "spo2_avg", Label: "SpO2 ขณะนอน", Group: GroupSleep, Format: unit("%.1f", "%"), get: night(func(n *model.SleepNight) *float64 { return n.SpO2Avg })},
	{Key: "skin_temp_delta_c", Label: "อุณหภูมิผิวหนังเทียบ baseline", Group: GroupSleep, Format: unit("%+.1f", " °C"), get: night(func(n *model.SleepNight) *float64 { return n.SkinTempDeltaC })},
	{Key: "respiratory_rate", Label: "อัตราการหายใจขณะนอน", Group: GroupSleep, Format: unit("%.1f", " ครั้ง/นาที"), get: night(func(n *model.SleepNight) *float64 { return n.RespiratoryRate })},

	{Key: "steps", Label: "ก้าวเดิน", Group: GroupActivity, Format: FormatInt(" ก้าว"), get: activity(func(a *model.Activity) *float64 { return fi(a.Steps) })},
	{Key: "distance_km", Label: "ระยะทาง", Group: GroupActivity, Format: unit("%.2f", " กม."), get: activity(func(a *model.Activity) *float64 {
		if a.DistanceM == nil {
			return nil
		}
		return model.Ptr(*a.DistanceM / 1000)
	})},
	{Key: "energy_kcal", Label: "พลังงานที่ใช้ทั้งวัน", Group: GroupActivity, Format: FormatInt(" kcal"), get: activity(func(a *model.Activity) *float64 { return a.EnergyKcal })},
	{Key: "active_energy_kcal", Label: "พลังงานจากกิจกรรม", Group: GroupActivity, Format: FormatInt(" kcal"), get: activity(func(a *model.Activity) *float64 { return a.ActiveEnergyKcal })},
	{Key: "active_zone_min", Label: "Active Zone Minutes", Group: GroupActivity, Format: FormatInt(" นาที"), get: activity(func(a *model.Activity) *float64 { return fi(a.ActiveZoneMin) })},
	{Key: "active_min", Label: "นาทีที่เคลื่อนไหว", Group: GroupActivity, Format: FormatInt(" นาที"), get: activity(func(a *model.Activity) *float64 {
		return sumPtr(a.ActiveLightMin, a.ActiveModerateMin, a.ActiveVigorousMin)
	})},
	{Key: "sedentary_min", Label: "เวลานั่งนิ่ง", Group: GroupActivity, Format: FormatDuration, get: activity(func(a *model.Activity) *float64 { return fi(a.SedentaryMin) })},
	{Key: "resting_hr", Label: "ชีพจรขณะพัก", Group: GroupActivity, Format: unit("%.0f", " bpm"), get: activity(func(a *model.Activity) *float64 { return a.RestingHR })},
}

func DefByKey(key string) (Def, bool) {
	for _, d := range Defs {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

// Values คืนค่าของตัวชี้วัดในแต่ละวัน เรียงตาม rows (nil = ไม่มีข้อมูล)
func Values(rows []DayRow, key string) []*float64 {
	out := make([]*float64, len(rows))
	def, ok := DefByKey(key)
	if !ok {
		return out
	}
	for i, r := range rows {
		out[i] = def.get(r)
	}
	return out
}

type WindowStat struct {
	Days       int
	WithData   int
	Avg        *float64
	Sufficient bool
	Note       string // คิดจากข้อมูลกี่วันและเพราะอะไร (FR7)
}

type Trend struct {
	Direction string // up | down | stable | "" = ข้อมูลไม่พอ
	PctChange *float64
	Note      string
}

type Summary struct {
	Def     Def
	Today   *float64
	Windows []WindowStat
	Trend   Trend
}

func (s *Summary) Window(days int) *WindowStat {
	for i := range s.Windows {
		if s.Windows[i].Days == days {
			return &s.Windows[i]
		}
	}
	return nil
}

type Result struct {
	Date          time.Time
	FirstDataDate *time.Time
	Rows          []DayRow
	Metrics       []*Summary
	byKey         map[string]*Summary
	// การออกกำลังกาย 7 วันล่าสุดเทียบเป้าหมาย WHO 150–300 นาที/สัปดาห์ (ใช้ในคำแนะนำ ไม่ใช่การเตือน)
	WeeklyAZM                *float64 // ผลรวม Active Zone Minutes แบบที่แอปแสดง
	WeeklyModerateEquivalent *float64 // fat burn + 2×(cardio + peak) ตามหลัก WHO ที่กิจกรรมหนัก 1 นาที = ปานกลาง 2 นาที
	WeeklyDays               int
}

func (r *Result) Get(key string) *Summary { return r.byKey[key] }

// Compute คำนวณสถิติของวัน d จาก rows (เรียงเก่าไปใหม่ แถวสุดท้ายคือวัน d) อ่านไม่เกิน max_lookback_days
func Compute(rows []DayRow, d time.Time, firstData *time.Time, cfg Config) *Result {
	if len(rows) > cfg.MaxLookbackDays {
		rows = rows[len(rows)-cfg.MaxLookbackDays:]
	}
	res := &Result{Date: d, FirstDataDate: firstData, Rows: rows, byKey: map[string]*Summary{}}
	for _, def := range Defs {
		vals := Values(rows, def.Key)
		s := &Summary{Def: def}
		if n := len(rows); n > 0 && rows[n-1].Date.Equal(d) {
			s.Today = vals[n-1]
		}
		for _, w := range cfg.Windows {
			s.Windows = append(s.Windows, window(vals, w, d, firstData, cfg.MinDaysForAverage))
		}
		s.Trend = trend(vals, cfg)
		res.Metrics = append(res.Metrics, s)
		res.byKey[def.Key] = s
	}
	res.weekly(last(rows, 7))
	return res
}

func (r *Result) weekly(rows []DayRow) {
	var azm, eq float64
	for _, row := range rows {
		a := row.Activity
		if a == nil || a.WearOK == nil || !*a.WearOK || a.ActiveZoneMin == nil {
			continue
		}
		r.WeeklyDays++
		azm += float64(*a.ActiveZoneMin)
		v := func(p *int) float64 {
			if p == nil {
				return 0
			}
			return float64(*p)
		}
		eq += v(a.AZMFatBurnMin) + 2*(v(a.AZMCardioMin)+v(a.AZMPeakMin))
	}
	if r.WeeklyDays > 0 {
		r.WeeklyAZM, r.WeeklyModerateEquivalent = &azm, &eq
	}
}

func last[T any](vals []T, n int) []T {
	if len(vals) <= n {
		return vals
	}
	return vals[len(vals)-n:]
}

// MeanSD คืนค่าเฉลี่ย ส่วนเบี่ยงเบนมาตรฐาน (sample) และจำนวนค่าที่มี โดยข้าม nil
func MeanSD(vals []*float64) (mean, sd float64, n int) {
	var sum float64
	for _, v := range vals {
		if v != nil {
			sum += *v
			n++
		}
	}
	if n == 0 {
		return 0, 0, 0
	}
	mean = sum / float64(n)
	if n < 2 {
		return mean, 0, n
	}
	var ss float64
	for _, v := range vals {
		if v != nil {
			ss += (*v - mean) * (*v - mean)
		}
	}
	return mean, math.Sqrt(ss / float64(n-1)), n
}

func window(vals []*float64, days int, d time.Time, firstData *time.Time, minDays int) WindowStat {
	ws := WindowStat{Days: days}
	mean, _, n := MeanSD(last(vals, days))
	ws.WithData = n
	windowStart := d.AddDate(0, 0, -(days - 1))

	var why string
	switch {
	case n == days:
		why = fmt.Sprintf("ครบ %d วัน", days)
	case firstData != nil && firstData.After(windowStart):
		why = fmt.Sprintf("มีข้อมูล %d วัน เริ่มเก็บเมื่อ %s", n, ThaiShort(*firstData))
	default:
		why = fmt.Sprintf("มีข้อมูล %d จาก %d วัน วันที่เหลือไม่มีข้อมูลหรือสวมไม่ถึงครึ่งวัน", n, days)
	}
	if n < minDays {
		ws.Note = fmt.Sprintf("%s ยังไม่พอคำนวณ (ต้องมีอย่างน้อย %d วัน)", why, minDays)
		return ws
	}
	ws.Avg, ws.Sufficient, ws.Note = &mean, true, why
	return ws
}

// trend เทียบค่าเฉลี่ย 7 วันล่าสุดกับวันก่อนหน้าในช่วง 30 วัน ไม่คำนวณเมื่อข้อมูล 30 วันน้อยกว่า trend_min_days
func trend(vals []*float64, cfg Config) Trend {
	w30 := last(vals, 30)
	_, _, total := MeanSD(w30)
	if total < cfg.TrendMinDays {
		return Trend{Note: fmt.Sprintf("ข้อมูลยังไม่พอดูแนวโน้ม (มี %d วัน ต้องการ %d วัน)", total, cfg.TrendMinDays)}
	}
	split := max(0, len(w30)-7)
	recent, _, nr := MeanSD(w30[split:])
	prior, _, np := MeanSD(w30[:split])
	if nr < cfg.MinDaysForAverage || np < cfg.MinDaysForAverage || prior == 0 {
		return Trend{Note: "ข้อมูลช่วงล่าสุดหรือช่วงก่อนหน้าไม่พอดูแนวโน้ม"}
	}
	pct := (recent - prior) / math.Abs(prior) * 100
	t := Trend{PctChange: &pct, Direction: TrendStable}
	switch {
	case pct >= cfg.TrendThresholdPct:
		t.Direction = TrendUp
	case pct <= -cfg.TrendThresholdPct:
		t.Direction = TrendDown
	}
	return t
}

// ---------- รูปแบบตัวเลข ----------

func unit(f, u string) func(float64) string {
	return func(v float64) string { return fmt.Sprintf(f, v) + u }
}

// FormatInt แสดงจำนวนเต็มพร้อมตัวคั่นหลักพัน
func FormatInt(u string) func(float64) string {
	return func(v float64) string { return Commas(int64(math.Round(v))) + u }
}

func Commas(n int64) string {
	s := fmt.Sprint(n)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// FormatDuration แสดงนาทีเป็น "7 ชม. 22 นาที"
func FormatDuration(min float64) string {
	m := int(math.Round(min))
	switch {
	case m < 60:
		return fmt.Sprintf("%d นาที", m)
	case m%60 == 0:
		return fmt.Sprintf("%d ชม.", m/60)
	default:
		return fmt.Sprintf("%d ชม. %d นาที", m/60, m%60)
	}
}

var thaiMonths = [...]string{"ม.ค.", "ก.พ.", "มี.ค.", "เม.ย.", "พ.ค.", "มิ.ย.", "ก.ค.", "ส.ค.", "ก.ย.", "ต.ค.", "พ.ย.", "ธ.ค."}

// ThaiShort แสดงวันที่แบบ "12 ต.ค."
func ThaiShort(d time.Time) string { return fmt.Sprintf("%d %s", d.Day(), thaiMonths[d.Month()-1]) }

// ThaiDate แสดงวันที่แบบ "12 ต.ค. 2569"
func ThaiDate(d time.Time) string { return fmt.Sprintf("%s %d", ThaiShort(d), d.Year()+543) }
