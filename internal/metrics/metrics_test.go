package metrics

import (
	"math"
	"strings"
	"testing"
	"time"

	"fitbot/internal/model"
)

var end = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

// rowsWith สร้างข้อมูลจำลองที่สิ้นสุดวัน end; ค่าติดลบ = ไม่มีข้อมูล
func rowsWith(steps []int) []DayRow {
	n := len(steps)
	rows := make([]DayRow, n)
	for i, s := range steps {
		rows[i].Date = end.AddDate(0, 0, i-(n-1))
		if s >= 0 {
			rows[i].Activity = &model.Activity{Steps: model.Ptr(s), WearOK: model.Ptr(true)}
		}
	}
	return rows
}

func empty(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = -1
	}
	return s
}

// AC F7: ค่าเฉลี่ยข้ามวันที่ไม่มีข้อมูล และมีจำนวนวันที่มีข้อมูล
func TestAverageSkipsNull(t *testing.T) {
	steps := empty(90)
	steps[89], steps[88], steps[86] = 1000, 3000, 5000
	w7 := Compute(rowsWith(steps), end, nil, DefaultConfig()).Get("steps").Window(7)
	if w7.WithData != 3 || *w7.Avg != 3000 {
		t.Fatalf("withData=%d avg=%v", w7.WithData, w7.Avg)
	}
}

func TestZeroIsData(t *testing.T) {
	w := Compute(rowsWith([]int{0, 0, 0}), end, nil, DefaultConfig()).Get("steps").Window(7)
	if w.WithData != 3 || *w.Avg != 0 {
		t.Fatalf("0 ต้องนับเป็นข้อมูล: %+v", w)
	}
}

// FR25: วันที่สวมไม่ถึงเกณฑ์ไม่ถูกนำมาคิด
func TestInsufficientWearExcluded(t *testing.T) {
	rows := rowsWith([]int{1000, 1000, 1000, 9000})
	rows[3].Activity.WearOK = model.Ptr(false)
	s := Compute(rows, end, nil, DefaultConfig()).Get("steps")
	if s.Today != nil || *s.Window(7).Avg != 1000 {
		t.Fatalf("today=%v avg=%v", s.Today, *s.Window(7).Avg)
	}
}

// FR7: "30 วัน (มีข้อมูล 12 วัน เริ่มเก็บเมื่อ …)"
func TestWindowNoteExplainsTrackingStart(t *testing.T) {
	steps := empty(90)
	for i := 78; i < 90; i++ {
		steps[i] = 5000
	}
	first := end.AddDate(0, 0, -11)
	w30 := Compute(rowsWith(steps), end, &first, DefaultConfig()).Get("steps").Window(30)
	if w30.WithData != 12 || !strings.Contains(w30.Note, "มีข้อมูล 12 วัน เริ่มเก็บเมื่อ 1 ต.ค.") {
		t.Fatalf("note = %q", w30.Note)
	}
}

func TestBelowMinimumNoAverage(t *testing.T) {
	w := Compute(rowsWith([]int{-1, -1, -1, -1, -1, 4000, 6000}), end, nil, DefaultConfig()).Get("steps").Window(7)
	if w.Avg != nil || w.Sufficient || !strings.Contains(w.Note, "ยังไม่พอ") {
		t.Fatalf("%+v", w)
	}
}

// AC F7: อ่านไม่เกิน 90 วัน
func TestReadsAtMost90Days(t *testing.T) {
	steps := make([]int, 120)
	if res := Compute(rowsWith(steps), end, nil, DefaultConfig()); len(res.Rows) != 90 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
}

// AC F7: ไม่คำนวณแนวโน้มเมื่อข้อมูลน้อยกว่าขั้นต่ำ
func TestTrendNeedsMinimumDays(t *testing.T) {
	if tr := Compute(rowsWith([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}), end, nil, DefaultConfig()).Get("steps").Trend; tr.Direction != "" {
		t.Fatal("ไม่ควรมีแนวโน้ม")
	}
}

func TestTrendUp(t *testing.T) {
	steps := make([]int, 30)
	for i := range steps {
		steps[i] = 5000
		if i >= 23 {
			steps[i] = 7000
		}
	}
	tr := Compute(rowsWith(steps), end, nil, DefaultConfig()).Get("steps").Trend
	if tr.Direction != TrendUp || math.Abs(*tr.PctChange-40) > 0.01 {
		t.Fatalf("%+v", tr)
	}
}

// AC F7: ผลคงที่กับข้อมูลชุดเดิม
func TestDeterministic(t *testing.T) {
	steps := []int{3000, -1, 5000, 7000, 8000, -1, 9000, 4000}
	a := Compute(rowsWith(steps), end, nil, DefaultConfig())
	b := Compute(rowsWith(steps), end, nil, DefaultConfig())
	if *a.Get("steps").Window(7).Avg != *b.Get("steps").Window(7).Avg {
		t.Fatal("ผลต้องคงที่")
	}
}

func TestWeeklyWHOEquivalent(t *testing.T) {
	rows := rowsWith([]int{1, 1})
	for _, r := range rows {
		r.Activity.ActiveZoneMin, r.Activity.AZMFatBurnMin, r.Activity.AZMCardioMin, r.Activity.AZMPeakMin = model.Ptr(48), model.Ptr(28), model.Ptr(20), model.Ptr(0)
	}
	res := Compute(rows, end, nil, DefaultConfig())
	if *res.WeeklyAZM != 96 || *res.WeeklyModerateEquivalent != 136 || res.WeeklyDays != 2 {
		t.Fatalf("azm=%v eq=%v", *res.WeeklyAZM, *res.WeeklyModerateEquivalent)
	}
}

func TestFormat(t *testing.T) {
	if FormatDuration(442) != "7 ชม. 22 นาที" || FormatInt(" ก้าว")(7350) != "7,350 ก้าว" || ThaiDate(end) != "12 ต.ค. 2569" {
		t.Fatal("format ผิด")
	}
	d, _ := DefByKey("steps")
	if d.Fmt(nil) != "ไม่มีข้อมูล" {
		t.Fatal("nil ต้องแสดงเป็นไม่มีข้อมูล")
	}
}
