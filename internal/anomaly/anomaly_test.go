package anomaly

import (
	"testing"
	"time"

	"fitbot/internal/metrics"
	"fitbot/internal/model"
)

var end = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

func nights(n int, f func(i int) *model.SleepNight) []metrics.DayRow {
	rows := make([]metrics.DayRow, n)
	for i := range rows {
		rows[i].Date = end.AddDate(0, 0, i-(n-1))
		rows[i].Night = f(i)
	}
	return rows
}

func withSleep(min int) *model.SleepNight {
	t := end
	return &model.SleepNight{Bedtime: &t, AsleepMin: model.Ptr(min)}
}

func eval(t *testing.T, rows []metrics.DayRow) []Alert {
	t.Helper()
	rules, err := LoadRules("")
	if err != nil {
		t.Fatal(err)
	}
	return Evaluate(metrics.Compute(rows, end, nil, metrics.DefaultConfig()), rules, 14)
}

func ids(as []Alert) map[string]bool {
	out := map[string]bool{}
	for _, a := range as {
		out[a.RuleID] = true
	}
	return out
}

func TestDefaultRulesLoad(t *testing.T) {
	rules, err := LoadRules("")
	if err != nil || len(rules) != 7 {
		t.Fatalf("rules=%d err=%v", len(rules), err)
	}
}

// Q27: เฝ้าระวัง < 5 ชม. ควรปรับปรุง < 6 ชม.
func TestSleepThresholds(t *testing.T) {
	if got := ids(eval(t, nights(7, func(int) *model.SleepNight { return withSleep(330) }))); !got["sleep_avg7_improve"] || got["sleep_avg7_watch"] {
		t.Fatalf("5.5 ชม. ควรเป็นควรปรับปรุงเท่านั้น: %v", got)
	}
	if got := ids(eval(t, nights(7, func(int) *model.SleepNight { return withSleep(270) }))); !got["sleep_avg7_watch"] {
		t.Fatalf("4.5 ชม. ควรเฝ้าระวัง: %v", got)
	}
	if got := eval(t, nights(7, func(int) *model.SleepNight { return withSleep(442) })); len(got) != 0 {
		t.Fatalf("7 ชม. ไม่ควรเตือน: %v", got)
	}
}

func TestSleepAverageNeedsMinimumDays(t *testing.T) {
	got := eval(t, nights(7, func(i int) *model.SleepNight {
		if i < 5 {
			return nil
		}
		return withSleep(200)
	}))
	if len(got) != 0 {
		t.Fatalf("ข้อมูล 2 วันไม่ควรเตือนค่าเฉลี่ย 7 วัน: %v", got)
	}
}

// AC F8: ทุกการเตือนบอกค่า เกณฑ์ และแหล่งอ้างอิง
func TestRestingHRAlertHasValueCriterionReference(t *testing.T) {
	rows := []metrics.DayRow{{Date: end, Activity: &model.Activity{RestingHR: model.Ptr(105.0), WearOK: model.Ptr(true)}}}
	alerts := eval(t, rows)
	if len(alerts) != 1 || alerts[0].RuleID != "rhr_high" {
		t.Fatalf("%v", alerts)
	}
	if a := alerts[0]; a.Value != "105 bpm" || a.Criterion == "" || a.Reference == "" {
		t.Fatalf("%+v", a)
	}
}

func TestSpO2NullDoesNotAlert(t *testing.T) {
	if got := eval(t, nights(3, func(int) *model.SleepNight { return &model.SleepNight{} })); len(got) != 0 {
		t.Fatalf("SpO2 ไม่มีค่าต้องไม่เตือน: %v", got)
	}
}

func hrvRows(n int, recent []float64) []metrics.DayRow {
	return nights(n, func(i int) *model.SleepNight {
		if k := i - (n - len(recent)); k >= 0 {
			return &model.SleepNight{NightMetrics: model.NightMetrics{HRVms: model.Ptr(recent[k])}}
		}
		v := 30.0 + float64(i%5) - 2 // ค่าปกติ 28–32 ms
		return &model.SleepNight{NightMetrics: model.NightMetrics{HRVms: &v}}
	})
}

func TestHRVPersonalRule(t *testing.T) {
	if !ids(eval(t, hrvRows(40, []float64{20, 19, 21})))["hrv_low"] {
		t.Fatal("HRV ต่ำ 3 วันติดควรเตือน")
	}
	if ids(eval(t, hrvRows(40, []float64{20, 30, 21})))["hrv_low"] {
		t.Fatal("ไม่ติดกัน 3 วันไม่ควรเตือน")
	}
}

// AC F8: กฎแบบค่าส่วนตัวเริ่มเมื่อมีข้อมูล ≥ 14 วัน
func TestPersonalRuleNeeds14Days(t *testing.T) {
	if ids(eval(t, hrvRows(16, []float64{10, 10, 10})))["hrv_low"] {
		t.Fatal("baseline 13 วันยังไม่ควรใช้กฎค่าส่วนตัว")
	}
	if !ids(eval(t, hrvRows(17, []float64{10, 10, 10})))["hrv_low"] {
		t.Fatal("baseline 14 วันต้องใช้กฎได้")
	}
}

func TestSkinTempBothDirections(t *testing.T) {
	rows := nights(35, func(i int) *model.SleepNight {
		v := float64(i%3-1) * 0.1
		if i >= 33 {
			v = 1.5
		}
		return &model.SleepNight{NightMetrics: model.NightMetrics{SkinTempDeltaC: &v}}
	})
	if !ids(eval(t, rows))["skin_temp_deviation"] {
		t.Fatal("อุณหภูมิสูงผิดปกติ 2 คืนติดควรเตือน")
	}
}
