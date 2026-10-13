package sleep

import (
	"testing"
	"time"

	"fitbot/internal/model"
)

var bkk, _ = time.LoadLocation("Asia/Bangkok")

func win() Window {
	return Window{StartMin: 18 * 60, EndMin: 12 * 60, Loc: bkk, MergeGap: 90 * time.Minute}
}

func at(day, hh, mm int) time.Time { return time.Date(2026, 10, day, hh, mm, 0, 0, bkk) }

func sess(id string, start time.Time, dur time.Duration) model.SleepSession {
	m := int(dur.Minutes())
	return model.SleepSession{ExternalID: id, Start: start, End: start.Add(dur), AsleepMin: &m}
}

var d12 = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

func kinds(ss []model.SleepSession) map[string]string {
	out := map[string]string{}
	for _, s := range ss {
		out[s.ExternalID] = s.Kind
	}
	return out
}

// AC F5: หลับ 23:00 วัน D เป็นของวัน D
func TestBedBeforeMidnightBelongsToD(t *testing.T) {
	out, night := Attribute(d12, []model.SleepSession{sess("a", at(12, 23, 0), 7*time.Hour)}, win(), model.NightMetrics{})
	if out[0].Kind != model.SleepMain || !out[0].AttributedDate.Equal(d12) || *night.AsleepMin != 420 {
		t.Fatalf("kind=%s date=%v", out[0].Kind, out[0].AttributedDate)
	}
}

// AC F5: หลับ 01:00 วัน D+1 เป็นของวัน D
func TestBedAfterMidnightBelongsToD(t *testing.T) {
	out, night := Attribute(d12, []model.SleepSession{sess("a", at(13, 1, 0), 6*time.Hour)}, win(), model.NightMetrics{})
	if out[0].Kind != model.SleepMain || !out[0].AttributedDate.Equal(d12) || !night.HasMainSleep() {
		t.Fatalf("kind=%s date=%v", out[0].Kind, out[0].AttributedDate)
	}
}

// รูปแบบเดียวกับข้อมูลที่พบใน M0 (ตัวเลขสมมติ): เข้านอน 23:10 ตื่น 06:40
func TestRealNightFromSpike(t *testing.T) {
	start := time.Date(2026, 10, 12, 16, 10, 0, 0, time.UTC)
	m := 442
	s := model.SleepSession{ExternalID: "x", Start: start, End: time.Date(2026, 10, 12, 23, 40, 0, 0, time.UTC), AsleepMin: &m}
	_, night := Attribute(d12, []model.SleepSession{s}, win(), model.NightMetrics{})
	if *night.AsleepMin != 442 || night.Bedtime.In(bkk).Format("15:04") != "23:10" {
		t.Fatalf("%+v", night)
	}
}

// AC F5: งีบบ่ายแยกจากการนอนหลัก
func TestAfternoonNapSeparated(t *testing.T) {
	out, night := Attribute(d12, []model.SleepSession{
		sess("nap", at(12, 14, 0), 30*time.Minute),
		sess("main", at(12, 23, 0), 7*time.Hour),
	}, win(), model.NightMetrics{})
	k := kinds(out)
	if k["nap"] != model.SleepNap || k["main"] != model.SleepMain || *night.NapMin != 30 || *night.AsleepMin != 420 {
		t.Fatalf("kinds=%v", k)
	}
}

// AC F5: การนอนขาดตอนรวมเป็นการนอนหลักเดียว
func TestFragmentedNightMerged(t *testing.T) {
	out, night := Attribute(d12, []model.SleepSession{
		sess("a", at(12, 23, 0), 3*time.Hour),
		sess("b", at(13, 2, 40), 4*time.Hour+20*time.Minute),
	}, win(), model.NightMetrics{})
	k := kinds(out)
	if k["a"] != model.SleepMain || k["b"] != model.SleepMain || *night.AsleepMin != 440 {
		t.Fatalf("kinds=%v", k)
	}
	if !night.Bedtime.Equal(at(12, 23, 0)) || !night.WakeTime.Equal(at(13, 7, 0)) {
		t.Fatalf("bed=%v wake=%v", night.Bedtime, night.WakeTime)
	}
}

func TestEveningDozeFarFromMainIsNap(t *testing.T) {
	out, night := Attribute(d12, []model.SleepSession{
		sess("doze", at(12, 19, 0), 20*time.Minute),
		sess("main", at(12, 23, 30), 7*time.Hour),
	}, win(), model.NightMetrics{})
	if kinds(out)["doze"] != model.SleepNap || *night.AsleepMin != 420 || *night.NapMin != 20 {
		t.Fatalf("%v", kinds(out))
	}
}

func TestMorningSleepBelongsToPreviousNight(t *testing.T) {
	out, night := Attribute(d12, []model.SleepSession{sess("x", at(12, 6, 0), time.Hour)}, win(), model.NightMetrics{})
	if !out[0].AttributedDate.Equal(d12.AddDate(0, 0, -1)) || night.HasMainSleep() {
		t.Fatalf("date=%v", out[0].AttributedDate)
	}
}

func TestNoSessionsGivesNullNotZero(t *testing.T) {
	_, night := Attribute(d12, nil, win(), model.NightMetrics{})
	if night.AsleepMin != nil || night.NapMin != nil || night.DeepMin != nil {
		t.Fatal("expected nil values")
	}
}

func TestCustomWindow(t *testing.T) {
	w := win()
	w.StartMin, w.EndMin = 20*60, 14*60 // ผู้ใช้ตั้ง 20:00–14:00
	out, _ := Attribute(d12, []model.SleepSession{sess("a", at(12, 19, 0), 30*time.Minute)}, w, model.NightMetrics{})
	if out[0].Kind != model.SleepNap {
		t.Fatal("19:00 อยู่นอกช่วง 20:00–14:00 ต้องเป็นการงีบ")
	}
}

func TestFetchRange(t *testing.T) {
	from, to := win().FetchRange(d12)
	if !from.Equal(at(12, 12, 0)) || !to.Equal(at(13, 12, 0)) {
		t.Fatalf("from=%v to=%v", from, to)
	}
}
