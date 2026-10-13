package report

import (
	"strings"
	"testing"
	"time"

	"fitbot/internal/anomaly"
	"fitbot/internal/coach"
	"fitbot/internal/metrics"
	"fitbot/internal/model"
)

var day = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

// input เป็นตัวเลขสมมติที่มีครบทุกตัวชี้วัด
func input(alerts []anomaly.Alert) Input {
	bed := time.Date(2026, 10, 12, 16, 10, 0, 0, time.UTC)
	wake := time.Date(2026, 10, 12, 23, 40, 0, 0, time.UTC)
	night := &model.SleepNight{Date: day, Bedtime: &bed, WakeTime: &wake, AsleepMin: model.Ptr(442), DeepMin: model.Ptr(74),
		LightMin: model.Ptr(250), RemMin: model.Ptr(118), AwakeMin: model.Ptr(4), NightMetrics: model.NightMetrics{HRVms: model.Ptr(31.2)}}
	act := &model.Activity{Steps: model.Ptr(7350), DistanceM: model.Ptr(6120.4), EnergyKcal: model.Ptr(2210.4), ActiveEnergyKcal: model.Ptr(412.3),
		ActiveZoneMin: model.Ptr(48), AZMFatBurnMin: model.Ptr(28), AZMCardioMin: model.Ptr(20), AZMPeakMin: model.Ptr(0),
		RestingHR: model.Ptr(64.0), SedentaryMin: model.Ptr(812), WearMinutes: model.Ptr(1398), WearOK: model.Ptr(true)}
	res := metrics.Compute([]metrics.DayRow{{Date: day, Night: night, Activity: act}}, day, &day, metrics.DefaultConfig())
	return Input{User: model.User{DisplayName: "คุณเอ", Timezone: "Asia/Bangkok"}, Date: day, Metrics: res, Alerts: alerts}
}

// AC F10: ตัวเลขตรงกับ Metrics Engine
func TestComposeNumbersMatch(t *testing.T) {
	out := Compose(input(nil), 4500)
	for _, want := range []string{"ประจำวันที่ 12 ต.ค. 2569", "เข้านอน 23:10 น. ตื่น 06:40 น.", "หลับจริงรวม 7 ชม. 22 นาที", "หลับลึก 1 ชม. 14 นาที",
		"เดิน 7,350 ก้าว (6.12 กม.)", "ใช้พลังงานทั้งวัน 2,210 kcal (ในจำนวนนี้มาจากการเคลื่อนไหว 412 kcal)", "Active Zone Minutes 48 นาที",
		"ชีพจรขณะพัก 64 bpm", "HRV (ความแปรปรวนของจังหวะหัวใจ) 31 ms", "ออกกำลังกายสะสม 7 วันล่าสุด 68 นาที"} {
		if !strings.Contains(out.Text, want) {
			t.Errorf("ไม่พบ %q", want)
		}
	}
}

// AC F10: section 6 แสดงเมื่อมีการเตือนเท่านั้น พร้อม disclaimer และเป็น section สุดท้าย (FR14)
func TestWatchSectionOnlyWhenAlerts(t *testing.T) {
	if out := Compose(input(nil), 4500); strings.Contains(out.Text, "🩺") || strings.Contains(out.Text, Disclaimer) {
		t.Fatal("ไม่มีการเตือนต้องไม่แสดง section นี้")
	}
	alerts := []anomaly.Alert{{RuleID: "rhr_high", Level: anomaly.LevelWatch, Label: "ชีพจรขณะพัก", Value: "105 bpm", Criterion: "มากกว่า 100 bpm", Reference: "AHA"}}
	out := Compose(input(alerts), 4500)
	if !strings.HasSuffix(out.Text, "🩺 ค่าที่ควรเฝ้าระวัง\n• ชีพจรขณะพัก: 105 bpm\n  เกณฑ์: มากกว่า 100 bpm\n  อ้างอิง: AHA\n\n"+Disclaimer) {
		t.Fatalf("section สุดท้ายไม่ถูกต้อง:\n%s", out.Text)
	}
}

// AC F10: แสดง "ไม่มีข้อมูล" แทน 0
func TestNoDataNotZero(t *testing.T) {
	out := Compose(input(nil), 4500)
	if strings.Contains(out.Text, "SpO2) 0") || !strings.Contains(out.Text, "ใช้พลังงานทั้งวัน") {
		t.Fatal(out.Text)
	}
	in := input(nil)
	in.Metrics.Rows[0].Activity.EnergyKcal = nil
	in.Metrics = metrics.Compute(in.Metrics.Rows, day, &day, metrics.DefaultConfig())
	if text := Compose(in, 4500).Text; !strings.Contains(text, "ใช้พลังงานทั้งวัน ไม่มีข้อมูล") {
		t.Fatal(text)
	}
}

// AC F10: รายงานบางส่วนบอกว่าข้อมูลไม่ครบ
func TestPartialReportNotice(t *testing.T) {
	in := input(nil)
	in.Partial, in.Missing = true, []string{"ข้อมูลการนอน"}
	if out := Compose(in, 4500); !strings.Contains(out.Text, "⚠️ รายงานนี้ยังไม่ครบ: ขาดข้อมูลการนอน") {
		t.Fatal(out.Text)
	}
}

func TestInsufficientWearActivity(t *testing.T) {
	in := input(nil)
	in.Metrics.Rows[0].Activity.WearOK = model.Ptr(false)
	if out := Compose(in, 4500); !strings.Contains(out.Text, "สวมอุปกรณ์ไม่ถึงครึ่งวัน") || strings.Contains(out.Text, "• เดิน") {
		t.Fatal(out.Text)
	}
}

func TestImproveGoesToTrendSection(t *testing.T) {
	alerts := []anomaly.Alert{{Level: anomaly.LevelImprove, Label: "การนอนหลัก", Value: "เฉลี่ย 7 วัน 5 ชม. 30 นาที", Criterion: "เฉลี่ย 7 วันน้อยกว่า 6 ชม."}}
	out := Compose(input(alerts), 4500)
	if !strings.Contains(out.Text, "💡 แนวโน้มและคำแนะนำวันนี้\nสิ่งที่ควรปรับปรุง\n• การนอนหลัก") || strings.Contains(out.Text, "🩺") {
		t.Fatal(out.Text)
	}
}

// AC F10: ยาวเกินแบ่งเป็นหลายข้อความ
func TestSplitLongReport(t *testing.T) {
	long := strings.Repeat("ก", 3000)
	msgs := Split([]string{"หัว", long, long, strings.Repeat("ข\n", 6000)}, 4500)
	for _, m := range msgs {
		if n := len([]rune(m)); n > 4500 {
			t.Fatalf("ข้อความยาว %d", n)
		}
	}
	if len(msgs) < 4 {
		t.Fatalf("ต้องแบ่ง ได้ %d", len(msgs))
	}
	if got := Split([]string{"a", "b"}, 4500); len(got) != 1 || got[0] != "a\n\nb" {
		t.Fatalf("สั้นต้องรวมเป็นข้อความเดียว: %q", got)
	}
}

// FR7: ทุกค่าเฉลี่ยระบุจำนวนวันที่มีข้อมูลและเหตุผล
func TestAveragesShowDayCounts(t *testing.T) {
	var rows []metrics.DayRow
	for i := 0; i < 12; i++ {
		d := day.AddDate(0, 0, i-11)
		rows = append(rows, metrics.DayRow{Date: d, Activity: &model.Activity{Steps: model.Ptr(5000), WearOK: model.Ptr(true)}})
	}
	first := rows[0].Date
	in := input(nil)
	in.MinDays = 3
	in.Metrics = metrics.Compute(rows, day, &first, metrics.DefaultConfig())
	text := Compose(in, 4500).Text
	for _, want := range []string{"ก้าวเดิน: เมื่อวาน 5,000 ก้าว\n   เฉลี่ย 7 วัน: 5,000 ก้าว (จากข้อมูล 7 จาก 7 วัน)\n   เฉลี่ย 30 วัน: 5,000 ก้าว (จากข้อมูล 12 จาก 30 วัน)",
		"เฉลี่ย 90 วัน: 5,000 ก้าว (จากข้อมูล 12 จาก 90 วัน)", "เริ่มเก็บข้อมูลเมื่อ 1 ต.ค.", "อย่างน้อย 3 วัน"} {
		if !strings.Contains(text, want) {
			t.Errorf("ไม่พบ %q ใน\n%s", want, text)
		}
	}
}

func TestComposeWithCoaching(t *testing.T) {
	in := input([]anomaly.Alert{{Level: anomaly.LevelWatch, Label: "ชีพจรขณะพัก", Value: "105 bpm", Criterion: "มากกว่า 100 bpm", Reference: "AHA"}})
	in.Coaching = &coach.Coaching{Summary: "เมื่อวานนอนดี", Advice: []string{"เดิน 15 นาที"}, WatchExplanation: "ชีพจรสูงกว่าปกติ"}
	text := Compose(in, 4500).Text
	if !strings.HasPrefix(strings.SplitN(text, "\n\n", 2)[1], "💬 สรุปเมื่อวาน\nเมื่อวานนอนดี") {
		t.Fatalf("สรุปจาก AI ต้องอยู่ต่อจากหัวรายงาน:\n%s", text)
	}
	if !strings.Contains(text, "💡 แนวโน้มและคำแนะนำวันนี้\nคำแนะนำวันนี้\n• เดิน 15 นาที") || !strings.Contains(text, "ชีพจรสูงกว่าปกติ\n\n"+Disclaimer) {
		t.Fatalf("%s", text)
	}
}

// D5: AI ใช้ไม่ได้ยังได้รายงาน พร้อมหมายเหตุ
func TestFallbackNote(t *testing.T) {
	in := input(nil)
	in.LLMFailed = true
	if text := Compose(in, 4500).Text; !strings.HasSuffix(text, FallbackNote) || !strings.Contains(text, "เดิน 7,350") {
		t.Fatal(text)
	}
}

func TestAveragesCollapsedWhenTooFewDays(t *testing.T) {
	text := Compose(Input{User: model.User{Timezone: "Asia/Bangkok"}, Date: day, MinDays: 3, Metrics: input(nil).Metrics}, 4500).Text
	if !strings.Contains(text, "📈 เทียบกับค่าเฉลี่ยของคุณ\nยังเทียบไม่ได้ เพราะเพิ่งมีข้อมูล 1 วัน ต้องมีอย่างน้อย 3 วัน") {
		t.Fatal(text)
	}
}
