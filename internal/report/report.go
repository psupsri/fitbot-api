// Package report คือ Report Composer (F10): ประกอบรายงานรายวันจากผลของ Metrics Engine และกฎค่าผิดปกติ
// ตัวเลขทุกตัวมาจาก template (D4) โมดูลนี้ไม่คำนวณสถิติเอง
// สรุปเมื่อวาน (section 1) และคำแนะนำวันนี้ (section 5) มาจาก LLM (F9) ถ้าใช้ไม่ได้รายงานยังส่งได้แบบ template (D5)
package report

import (
	"fmt"
	"strings"
	"time"

	"fitbot/internal/anomaly"
	"fitbot/internal/coach"
	"fitbot/internal/metrics"
	"fitbot/internal/model"
)

// Disclaimer ข้อความตายตัวของ section ค่าที่ควรเฝ้าระวัง (FR15, RK10)
const Disclaimer = "ℹ️ ข้อมูลนี้เป็นคำแนะนำจาก AI และระบบอัตโนมัติ ไม่ใช่คำวินิจฉัยของแพทย์ และ Fitbot ไม่ใช่ระบบแจ้งเหตุฉุกเฉินนะคะ ถ้ามีอาการผิดปกติหรือไม่แน่ใจ ควรปรึกษาแพทย์ค่ะ"

// FallbackNote แสดงเมื่อเรียก AI ไม่สำเร็จ (D5)
const FallbackNote = "ℹ️ วันนี้ Fitbot สร้างสรุปและคำแนะนำจาก AI ไม่สำเร็จ รายงานนี้จึงมีเฉพาะตัวเลขค่ะ"

type Input struct {
	User    model.User
	Date    time.Time
	Partial bool
	Missing []string
	Metrics *metrics.Result
	Alerts  []anomaly.Alert
	MinDays int // ค่าเฉลี่ยต้องมีข้อมูลอย่างน้อยกี่วัน (ใช้อธิบายในรายงาน)

	Coaching  *coach.Coaching // nil = ไม่มีส่วนจาก AI
	LLMFailed bool            // เรียก AI แล้วไม่สำเร็จ → แสดงหมายเหตุ fallback
}

type Output struct {
	Text     string
	Messages []string
}

// Compose ประกอบรายงานแล้วแบ่งเป็นข้อความที่ยาวไม่เกิน maxRunes
// เน้นอ่านเข้าใจง่ายมากกว่าสั้น (Gate M2: OQ9)
func Compose(in Input, maxRunes int) Output {
	watch, improve := anomaly.Split(in.Alerts)
	var night *model.SleepNight
	var act *model.Activity
	if n := len(in.Metrics.Rows); n > 0 {
		night, act = in.Metrics.Rows[n-1].Night, in.Metrics.Rows[n-1].Activity
	}
	day := metrics.ThaiShort(in.Date)

	header := fmt.Sprintf("📊 รายงานสุขภาพของ%s\nประจำวันที่ %s", in.User.DisplayName, metrics.ThaiDate(in.Date))
	if in.Partial {
		header += "\n\n⚠️ รายงานนี้ยังไม่ครบ: ขาด" + strings.Join(in.Missing, " และ") + " ตัวเลขด้านล่างคิดจากข้อมูลที่มีเท่านั้นค่ะ"
	}
	sections := []string{header}
	if in.Coaching != nil {
		sections = append(sections, "💬 สรุปเมื่อวาน\n"+in.Coaching.Summary)
	}
	sections = append(sections,
		"😴 การนอนคืนวันที่ "+day+"\n"+sleepSection(in, night),
		"🚶 กิจกรรมวันที่ "+day+"\n"+activitySection(in, act),
		"📈 เทียบกับค่าเฉลี่ยของคุณ\n"+averagesSection(in.Metrics, in.MinDays),
	)

	var tips []string
	if t := trendLines(in.Metrics); len(t) > 0 {
		tips = append(tips, "แนวโน้ม 7 วันล่าสุด (เทียบกับช่วงก่อนหน้าภายใน 30 วัน)\n"+strings.Join(t, "\n"))
	}
	if len(improve) > 0 {
		var lines []string
		for _, a := range improve {
			lines = append(lines, fmt.Sprintf("• %s %s ซึ่งต่ำกว่าเกณฑ์ (%s)", a.Label, a.Value, a.Criterion))
		}
		tips = append(tips, "สิ่งที่ควรปรับปรุง\n"+strings.Join(lines, "\n"))
	}
	if in.Coaching != nil {
		var lines []string
		for _, a := range in.Coaching.Advice {
			lines = append(lines, "• "+a)
		}
		tips = append(tips, "คำแนะนำวันนี้\n"+strings.Join(lines, "\n"))
	}
	if len(tips) > 0 {
		sections = append(sections, "💡 แนวโน้มและคำแนะนำวันนี้\n"+strings.Join(tips, "\n\n"))
	}

	if len(watch) > 0 {
		var lines []string
		for _, a := range watch {
			lines = append(lines, fmt.Sprintf("• %s: %s\n  เกณฑ์: %s\n  อ้างอิง: %s", a.Label, a.Value, a.Criterion, a.Reference))
		}
		body := strings.Join(lines, "\n")
		if in.Coaching != nil && in.Coaching.WatchExplanation != "" {
			body += "\n\n" + in.Coaching.WatchExplanation
		}
		sections = append(sections, "🩺 ค่าที่ควรเฝ้าระวัง\n"+body+"\n\n"+Disclaimer)
	}
	if in.LLMFailed {
		sections = append(sections, FallbackNote)
	}
	return Output{Text: strings.Join(sections, "\n\n"), Messages: Split(sections, maxRunes)}
}

func get(r *metrics.Result, key string) string {
	s := r.Get(key)
	return s.Def.Fmt(s.Today)
}

func clock(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "ไม่มีข้อมูล"
	}
	return t.In(loc).Format("15:04") + " น."
}

func sleepSection(in Input, n *model.SleepNight) string {
	r := in.Metrics
	var lines []string
	if !n.HasMainSleep() {
		lines = append(lines, "• ไม่มีข้อมูลการนอนหลักของคืนนี้")
	} else {
		loc := in.User.Location()
		lines = append(lines,
			fmt.Sprintf("• เข้านอน %s ตื่น %s", clock(n.Bedtime, loc), clock(n.WakeTime, loc)),
			"• หลับจริงรวม "+get(r, "asleep_min"))
		// แสดงช่วงการนอนเฉพาะที่มีข้อมูล (API อาจไม่มี stages ในบางคืน)
		for _, st := range []struct{ key, label string }{{"deep_min", "หลับลึก"}, {"light_min", "หลับตื้น"}, {"rem_min", "หลับฝัน (REM)"}, {"awake_min", "ตื่นระหว่างคืน"}} {
			if s := r.Get(st.key); s.Today != nil {
				lines = append(lines, "   - "+st.label+" "+s.Def.Fmt(s.Today))
			}
		}
	}
	if s := r.Get("nap_min"); s.Today != nil {
		lines = append(lines, "• งีบระหว่างวัน "+s.Def.Fmt(s.Today))
	}
	// ค่าที่วัดขณะนอน แสดงเมื่อมีค่าอย่างน้อยหนึ่งตัว
	measured := []struct{ key, label, hint string }{
		{"hrv_ms", "HRV (ความแปรปรวนของจังหวะหัวใจ)", ""},
		{"respiratory_rate", "อัตราการหายใจ", ""},
		{"skin_temp_delta_c", "อุณหภูมิผิวหนัง", " เทียบกับค่าปกติของคุณ"},
		{"spo2_avg", "ออกซิเจนในเลือด (SpO2)", ""},
	}
	var ml []string
	for _, m := range measured {
		if s := r.Get(m.key); s.Today != nil {
			ml = append(ml, fmt.Sprintf("   - %s %s%s", m.label, s.Def.Fmt(s.Today), m.hint))
		}
	}
	if len(ml) > 0 {
		lines = append(lines, "• ค่าที่วัดขณะนอน")
		lines = append(lines, ml...)
	}
	return strings.Join(lines, "\n")
}

func activitySection(in Input, a *model.Activity) string {
	r := in.Metrics
	switch {
	case !a.HasData():
		return "• ไม่มีข้อมูลกิจกรรมของวันนี้"
	case a.WearOK == nil || !*a.WearOK:
		return "• วันนี้สวมอุปกรณ์ไม่ถึงครึ่งวัน ข้อมูลกิจกรรมจึงไม่ครบและไม่นำมาคิดค่าเฉลี่ยค่ะ"
	}
	energy := line(r, "ใช้พลังงานทั้งวัน", "energy_kcal", "")
	if e, ae := r.Get("energy_kcal"), r.Get("active_energy_kcal"); e.Today != nil && ae.Today != nil {
		energy += " (ในจำนวนนี้มาจากการเคลื่อนไหว " + ae.Def.Fmt(ae.Today) + ")"
	}
	lines := []string{
		walk(r),
		energy,
		line(r, "Active Zone Minutes", "active_zone_min", "เวลาที่หัวใจเต้นแรงพอจะนับเป็นการออกกำลังกาย"),
		line(r, "เคลื่อนไหวรวม", "active_min", ""),
		line(r, "นั่งหรืออยู่นิ่ง", "sedentary_min", "ไม่รวมเวลานอน"),
		line(r, "ชีพจรขณะพัก", "resting_hr", ""),
	}
	if r.WeeklyModerateEquivalent != nil {
		lines = append(lines, fmt.Sprintf("• ออกกำลังกายสะสม 7 วันล่าสุด %s นาที จากเป้าหมาย WHO 150–300 นาทีต่อสัปดาห์\n  (นับแบบ WHO ที่นาทีออกกำลังหนักนับเป็น 2 เท่า มีข้อมูล %d จาก 7 วัน)",
			metrics.Commas(int64(*r.WeeklyModerateEquivalent)), r.WeeklyDays))
	}
	return strings.Join(lines, "\n")
}

// line แสดง "• label ค่า (คำอธิบาย)" คำอธิบายแสดงเฉพาะเมื่อมีค่า
func line(r *metrics.Result, label, key, hint string) string {
	s := r.Get(key)
	out := "• " + label + " " + s.Def.Fmt(s.Today)
	if hint != "" && s.Today != nil {
		out += " (" + hint + ")"
	}
	return out
}

func walk(r *metrics.Result) string {
	line := "• เดิน " + get(r, "steps")
	if d := r.Get("distance_km"); d.Today != nil {
		line += " (" + d.Def.Fmt(d.Today) + ")"
	}
	return line
}

// ตัวชี้วัดหลักที่แสดงในส่วนเทียบค่าเฉลี่ย
var averageKeys = []string{"asleep_min", "hrv_ms", "resting_hr", "steps", "active_zone_min", "energy_kcal", "sedentary_min"}

// averagesSection แสดงค่าเฉลี่ย 7/30/60/90 วัน ทุกค่าบอกว่าคิดจากข้อมูลกี่วัน และอธิบายเหตุผลท้ายส่วน (FR6, FR7)
func averagesSection(r *metrics.Result, minDays int) string {
	// ช่วงแรกที่ยังไม่มีตัวชี้วัดใดคำนวณค่าเฉลี่ยได้ บอกครั้งเดียวแทนการซ้ำทุกตัว
	anyAvg, maxWith := false, 0
	for _, k := range averageKeys {
		for _, w := range r.Get(k).Windows {
			anyAvg = anyAvg || w.Avg != nil
			maxWith = max(maxWith, w.WithData)
		}
	}
	if !anyAvg {
		return fmt.Sprintf("ยังเทียบไม่ได้ เพราะเพิ่งมีข้อมูล %d วัน ต้องมีอย่างน้อย %d วันจึงจะเริ่มคำนวณค่าเฉลี่ยได้ค่ะ", maxWith, minDays)
	}
	var blocks []string
	for _, k := range averageKeys {
		s := r.Get(k)
		anyData, anyAvg, maxWith := s.Today != nil, false, 0
		for _, w := range s.Windows {
			anyData = anyData || w.WithData > 0
			anyAvg = anyAvg || w.Avg != nil
			maxWith = max(maxWith, w.WithData)
		}
		if !anyData {
			continue
		}
		lines := []string{fmt.Sprintf("%s: เมื่อวาน %s", s.Def.Label, s.Def.Fmt(s.Today))}
		if !anyAvg {
			lines = append(lines, fmt.Sprintf("   ยังคำนวณค่าเฉลี่ยไม่ได้ เพราะมีข้อมูลแค่ %d วัน", maxWith))
		} else {
			for _, w := range s.Windows {
				if w.Avg == nil {
					lines = append(lines, fmt.Sprintf("   เฉลี่ย %d วัน: ยังไม่พอคำนวณ (มีข้อมูล %d วัน)", w.Days, w.WithData))
					continue
				}
				lines = append(lines, fmt.Sprintf("   เฉลี่ย %d วัน: %s (จากข้อมูล %d จาก %d วัน)", w.Days, s.Def.Format(*w.Avg), w.WithData, w.Days))
			}
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	if len(blocks) == 0 {
		return "ยังไม่มีข้อมูลพอสำหรับเทียบค่าเฉลี่ยค่ะ"
	}
	note := fmt.Sprintf("ℹ️ ค่าเฉลี่ยคิดเฉพาะวันที่มีข้อมูล วันที่ไม่ได้สวมหรือสวมไม่ถึงครึ่งวันจะไม่นำมาคิด และต้องมีข้อมูลอย่างน้อย %d วันจึงจะคำนวณ", minDays)
	if r.FirstDataDate != nil && r.Date.Sub(*r.FirstDataDate) < 90*24*time.Hour {
		note += " (เริ่มเก็บข้อมูลเมื่อ " + metrics.ThaiShort(*r.FirstDataDate) + " ข้อมูลจึงยังไม่ครบทุกช่วง)"
	}
	return strings.Join(blocks, "\n\n") + "\n\n" + note
}

func trendLines(r *metrics.Result) []string {
	var lines []string
	for _, k := range averageKeys {
		t := r.Get(k).Trend
		if t.PctChange == nil || t.Direction == metrics.TrendStable {
			continue
		}
		icon, dir := "🔺", "เพิ่มขึ้น"
		if t.Direction == metrics.TrendDown {
			icon, dir = "🔻", "ลดลง"
		}
		lines = append(lines, fmt.Sprintf("%s %s%s %.0f%%", icon, r.Get(k).Def.Label, dir, abs(*t.PctChange)))
	}
	return lines
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// Split แบ่งรายงานเป็นหลายข้อความเมื่อยาวเกิน maxRunes (LINE รับ 5,000 ตัวอักษรต่อข้อความ)
// แบ่งที่ขอบ section ก่อน ถ้า section เดียวยาวเกินจึงแบ่งตามบรรทัด
func Split(sections []string, maxRunes int) []string {
	var msgs []string
	cur := ""
	flush := func() {
		if strings.TrimSpace(cur) != "" {
			msgs = append(msgs, cur)
		}
		cur = ""
	}
	add := func(piece, sep string) {
		switch {
		case cur == "":
			cur = piece
		case runes(cur)+runes(sep)+runes(piece) > maxRunes:
			flush()
			cur = piece
		default:
			cur += sep + piece
		}
	}
	for _, s := range sections {
		if runes(s) <= maxRunes {
			add(s, "\n\n")
			continue
		}
		flush()
		for _, line := range strings.Split(s, "\n") {
			for runes(line) > maxRunes {
				r := []rune(line)
				add(string(r[:maxRunes]), "\n")
				line = string(r[maxRunes:])
			}
			add(line, "\n")
		}
		flush()
	}
	flush()
	return msgs
}

func runes(s string) int { return len([]rune(s)) }
