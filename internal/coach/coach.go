// Package coach คือ LLM Coaching (F9): เปลี่ยนข้อเท็จจริงที่คำนวณแล้วเป็นสรุปและคำแนะนำภาษาไทย
// LLM ได้รับเฉพาะตัวเลขสรุปที่จัดรูปแบบแล้ว ไม่ได้รับข้อมูลดิบ (FR8) และไม่คำนวณเอง (D4)
// รายการค่าที่ควรเฝ้าระวังมาจากกฎ LLM อธิบายได้แต่เพิ่มหรือตัดไม่ได้ (FR17)
package coach

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"

	"fitbot/internal/anomaly"
	"fitbot/internal/llm"
	"fitbot/internal/metrics"
	"fitbot/internal/model"
)

// Coaching คือส่วนที่ LLM สร้าง
type Coaching struct {
	Summary          string   `json:"summary"`           // section 1
	Advice           []string `json:"advice"`            // section 5
	WatchExplanation string   `json:"watch_explanation"` // section 6
}

// Facts คือข้อเท็จจริงที่ส่งให้ LLM ทุกค่าเป็นข้อความที่จัดรูปแบบแล้ว
type Facts struct {
	Date        string       `json:"วันที่"`
	Age         string       `json:"อายุ"`
	Incomplete  string       `json:"ข้อมูลไม่ครบ,omitempty"`
	DaysTracked string       `json:"จำนวนวันที่มีข้อมูล"`
	Sleep       []MetricFact `json:"การนอนเมื่อคืน"`
	Activity    []MetricFact `json:"กิจกรรมเมื่อวาน"`
	Exercise7d  string       `json:"ออกกำลังกาย 7 วันเทียบเป้าหมาย WHO"`
	Improve     []string     `json:"ควรปรับปรุง (ระบบตัดสินแล้ว)"`
	Watch       []string     `json:"ค่าที่ควรเฝ้าระวัง (ระบบตัดสินแล้ว)"`
}

type MetricFact struct {
	Name      string `json:"ชื่อ"`
	Yesterday string `json:"เมื่อวาน"`
	Avg7      string `json:"เฉลี่ย 7 วัน,omitempty"`
	VsAvg7    string `json:"เมื่อวานเทียบเฉลี่ย 7 วัน,omitempty"`
	Avg30     string `json:"เฉลี่ย 30 วัน,omitempty"`
	Trend     string `json:"แนวโน้ม,omitempty"`
}

type Input struct {
	User    model.User
	Partial bool
	Missing []string
	Metrics *metrics.Result
	Alerts  []anomaly.Alert
}

// ตัวชี้วัดที่ส่งให้ LLM (ไม่ส่งทุกตัว เพื่อให้โมเดลเล็กโฟกัสได้)
var sleepKeys = []string{"asleep_min", "deep_min", "rem_min", "awake_min", "nap_min", "hrv_ms", "spo2_avg", "skin_temp_delta_c", "respiratory_rate"}
var activityKeys = []string{"steps", "active_zone_min", "active_min", "sedentary_min", "energy_kcal", "resting_hr"}

// BuildFacts สร้างข้อเท็จจริงจากผลของ Metrics Engine และกฎค่าผิดปกติ
func BuildFacts(in Input) Facts {
	r := in.Metrics
	f := Facts{Date: metrics.ThaiDate(r.Date), Age: "ไม่ทราบ (ใช้เกณฑ์ผู้ใหญ่ทั่วไป)", Improve: []string{}, Watch: []string{}}
	if in.User.Age != nil {
		f.Age = fmt.Sprintf("%d ปี", *in.User.Age)
	}
	if in.Partial {
		f.Incomplete = "ขาด" + strings.Join(in.Missing, " และ") + " พูดถึงเฉพาะส่วนที่มี"
	}
	days := 0
	for _, row := range r.Rows {
		if row.Activity != nil || row.Night != nil {
			days++
		}
	}
	f.DaysTracked = fmt.Sprintf("%d วัน (ถ้าน้อยกว่า 14 วัน ยังสรุปแนวโน้มระยะยาวไม่ได้)", days)
	for _, k := range sleepKeys {
		if mf, ok := fact(r.Get(k)); ok {
			f.Sleep = append(f.Sleep, mf)
		}
	}
	for _, k := range activityKeys {
		if mf, ok := fact(r.Get(k)); ok {
			f.Activity = append(f.Activity, mf)
		}
	}
	if r.WeeklyModerateEquivalent != nil {
		f.Exercise7d = fmt.Sprintf("%.0f นาที (มีข้อมูล %d จาก 7 วัน) เป้าหมาย WHO 150–300 นาที/สัปดาห์", *r.WeeklyModerateEquivalent, r.WeeklyDays)
	} else {
		f.Exercise7d = "ไม่มีข้อมูล"
	}
	watch, improve := anomaly.Split(in.Alerts)
	for _, a := range improve {
		f.Improve = append(f.Improve, fmt.Sprintf("%s %s (เกณฑ์ %s)", a.Label, a.Value, a.Criterion))
	}
	for _, a := range watch {
		f.Watch = append(f.Watch, fmt.Sprintf("%s %s (เกณฑ์ %s)", a.Label, a.Value, a.Criterion))
	}
	return f
}

func fact(s *metrics.Summary) (MetricFact, bool) {
	w7, w30 := s.Window(7), s.Window(30)
	if s.Today == nil && (w7 == nil || w7.Avg == nil) {
		return MetricFact{}, false
	}
	mf := MetricFact{Name: s.Def.Label, Yesterday: s.Def.Fmt(s.Today)}
	if w7 != nil && w7.Avg != nil {
		mf.Avg7 = s.Def.Format(*w7.Avg)
		if s.Today != nil && *w7.Avg != 0 {
			pct := (*s.Today - *w7.Avg) / math.Abs(*w7.Avg) * 100
			switch {
			case pct >= 5:
				mf.VsAvg7 = fmt.Sprintf("มากกว่าเฉลี่ย %.0f%%", pct)
			case pct <= -5:
				mf.VsAvg7 = fmt.Sprintf("น้อยกว่าเฉลี่ย %.0f%%", -pct)
			default:
				mf.VsAvg7 = "ใกล้เคียงค่าเฉลี่ย"
			}
		}
	}
	if w30 != nil && w30.Avg != nil {
		mf.Avg30 = s.Def.Format(*w30.Avg)
	}
	if t := s.Trend; t.PctChange != nil {
		switch t.Direction {
		case metrics.TrendUp:
			mf.Trend = fmt.Sprintf("7 วันล่าสุดเพิ่มขึ้น %.0f%%", *t.PctChange)
		case metrics.TrendDown:
			mf.Trend = fmt.Sprintf("7 วันล่าสุดลดลง %.0f%%", -*t.PctChange)
		default:
			mf.Trend = "ทรงตัว"
		}
	}
	return mf, true
}

const systemPrompt = `คุณคือ "Fitbot" โค้ชสุขภาพส่วนตัว พูดภาษาไทยแบบเป็นกันเอง อบอุ่น ให้กำลังใจ ไม่ดุ ไม่ตำหนิ ไม่ใช้คำสั่งห้วนๆ
หน้าที่: อ่านข้อเท็จจริงที่ระบบคำนวณไว้แล้ว เชื่อมโยงการนอน กิจกรรม และค่าร่างกายเข้าด้วยกัน แล้วแนะนำสิ่งที่ทำได้จริงในวันนี้

กติกา:
- ใช้เฉพาะตัวเลขในข้อเท็จจริง เมื่อพูดถึงตัวเลขให้คัดลอกพร้อมหน่วยตามที่ให้ไว้ทุกตัวอักษร (เช่น "7 ชม. 22 นาที") ห้ามปัดเศษ ห้ามแปลงหน่วย ห้ามคำนวณใหม่
- ห้ามอ้างเกณฑ์หรือเป้าหมายที่ไม่มีในข้อเท็จจริง (เช่น 10,000 ก้าว หรือจำนวนชั่วโมงนั่งที่ควรเป็น) เป้าหมายเดียวที่อ้างได้คือเป้าหมาย WHO ที่ให้ไว้
- เทียบกับค่าเฉลี่ยของผู้ใช้เองเมื่อมีให้ แทนการเทียบกับเกณฑ์ทั่วไป
- ห้ามบอกว่า HRV ชีพจร SpO2 อุณหภูมิผิวหนัง หรืออัตราการหายใจ "ปกติ" "ดี" หรือ "ผิดปกติ" เอง ระบบตัดสินเรื่องนี้แล้วผ่านรายการ "ค่าที่ควรเฝ้าระวัง" ถ้าไม่อยู่ในรายการก็ไม่ต้องตีความ
- ค่าใดเป็น "ไม่มีข้อมูล" หรือไม่มีในรายการ ให้ข้าม ห้ามเดา
- ถ้าจำนวนวันที่มีข้อมูลน้อย อย่าสรุปว่าเป็นนิสัยหรือแนวโน้ม ให้พูดถึงเมื่อวานเป็นหลัก
- ไม่วินิจฉัยโรค ไม่แนะนำยา
- รายการ "ค่าที่ควรเฝ้าระวัง" ระบบตัดสินแล้ว ห้ามเพิ่มหรือตัด อธิบายได้อย่างเดียว ถ้ารายการว่างให้ watch_explanation เป็น ""
- ถ้ามีรายการ "ควรปรับปรุง" ต้องมีคำแนะนำที่ตอบเรื่องนั้นอย่างน้อยหนึ่งข้อ
- คำแนะนำต้องเจาะจง ทำได้วันนี้ เช่น เวลา ระยะเวลา หรือจำนวน ไม่ใช่คำแนะนำกว้างๆ
- เขียนเป็นภาษาไทยที่เป็นธรรมชาติ อ่านเข้าใจง่าย ห้ามใช้ชื่อ field หรือรูปแบบ JSON ในข้อความ
- ใช้คำลงท้ายผู้หญิง "ค่ะ" หรือ "นะคะ" เท่านั้น ห้ามใช้ "ครับ"

ตอบเป็น JSON อย่างเดียว:
{"summary": "สรุปเมื่อวาน 2–3 ประโยค บอกจุดที่ดีและจุดที่ควรสังเกต", "advice": ["คำแนะนำวันนี้ ข้อละ 1–2 ประโยค", "รวม 2–3 ข้อ"], "watch_explanation": "อธิบายค่าที่ควรเฝ้าระวังแบบเข้าใจง่าย 1–2 ประโยค หรือ \"\""}`

// MaxAttempts คือจำนวนครั้งที่ขอคำตอบใหม่เมื่อคำตอบไม่ผ่านการตรวจ
const MaxAttempts = 2

// Coach เรียก LLM แล้วตรวจคำตอบ (RK7) ไม่ผ่านขอใหม่ได้ MaxAttempts ครั้ง
// ผิดพลาด timeout หรือไม่ผ่านการตรวจ คืน error เพื่อให้ผู้เรียกใช้รายงาน template แทน (D5)
func Coach(ctx context.Context, client llm.Client, facts Facts) (*Coaching, error) {
	b, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		return nil, err
	}
	var lastErr error
	for range MaxAttempts {
		out, err := client.Chat(ctx, []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: "ข้อเท็จจริง:\n" + string(b)},
		})
		if err != nil {
			return nil, err // เรียกไม่ได้ (เช่น Ollama ไม่ทำงาน) ไม่ต้องลองซ้ำ
		}
		c, err := Parse(out, len(facts.Watch) > 0)
		if err == nil {
			err = Validate(c, string(b))
		}
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("คำตอบของ AI ไม่ผ่านการตรวจ %d ครั้ง: %w", MaxAttempts, lastErr)
}

var numberRe = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?`)

// Validate ตรวจคำตอบของ LLM ก่อนใช้ (RK7):
//   - ทุกข้อความต้องเป็นอักษรไทย ละติน ตัวเลข หรือเครื่องหมายทั่วไป (กันโทเคนภาษาอื่นหลุดมา)
//   - ตัวเลขทุกตัวในสรุปต้องมีอยู่ในข้อเท็จจริง (กันการปัดเศษหรือแต่งตัวเลข) คำแนะนำมีตัวเลขเชิงแนะนำได้ เช่น "เดิน 15 นาที"
func Validate(c *Coaching, facts string) error {
	texts := append([]string{c.Summary, c.WatchExplanation}, c.Advice...)
	for _, t := range texts {
		for _, r := range t {
			if !allowedRune(r) {
				return fmt.Errorf("มีอักษรที่ไม่ใช่ภาษาไทยหรืออังกฤษ %q", string(r))
			}
		}
	}
	for _, t := range texts {
		if strings.Contains(t, "ครับ") {
			return errors.New("ใช้คำลงท้าย \"ครับ\" แทน \"ค่ะ\"")
		}
		if strings.Contains(t, "_") {
			return fmt.Errorf("มีชื่อ field หลุดมาในข้อความ: %q", t)
		}
	}
	for _, n := range numberRe.FindAllString(c.Summary, -1) {
		if !strings.Contains(facts, n) {
			return fmt.Errorf("ตัวเลข %s ในสรุปไม่มีในข้อเท็จจริง", n)
		}
	}
	return nil
}

func allowedRune(r rune) bool {
	switch {
	case r >= 0x0E00 && r <= 0x0E7F: // ไทย
		return true
	case r < 0x80: // ASCII
		return true
	case unicode.IsSpace(r):
		return true
	}
	return strings.ContainsRune("•–—…“”‘’°×±·%", r)
}

const (
	maxSummaryRunes = 500
	maxAdviceItems  = 4
	maxAdviceRunes  = 250
)

// Parse แปลงและตรวจคำตอบของ LLM
func Parse(s string, needWatch bool) (*Coaching, error) {
	s = llm.StripThinking(s)
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		s = s[i : j+1]
	}
	var raw struct {
		Summary          string          `json:"summary"`
		Advice           json.RawMessage `json:"advice"`
		WatchExplanation string          `json:"watch_explanation"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("แปลงคำตอบของ LLM ไม่ได้: %w", err)
	}
	var advice []string
	if json.Unmarshal(raw.Advice, &advice) != nil {
		var one string
		if json.Unmarshal(raw.Advice, &one) != nil {
			return nil, errors.New("advice ต้องเป็นรายการข้อความ")
		}
		advice = []string{one}
	}
	c := &Coaching{Summary: strings.TrimSpace(raw.Summary)}
	for _, a := range advice {
		a = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(a), "-•*0123456789. "))
		if a != "" && runes(a) <= maxAdviceRunes && len(c.Advice) < maxAdviceItems {
			c.Advice = append(c.Advice, a)
		}
	}
	if needWatch {
		c.WatchExplanation = strings.TrimSpace(raw.WatchExplanation)
	}
	if c.Summary == "" || len(c.Advice) == 0 {
		return nil, errors.New("คำตอบของ LLM ขาดสรุปหรือคำแนะนำ")
	}
	if runes(c.Summary) > maxSummaryRunes {
		return nil, errors.New("สรุปจาก LLM ยาวเกิน")
	}
	return c, nil
}

func runes(s string) int { return len([]rune(s)) }
