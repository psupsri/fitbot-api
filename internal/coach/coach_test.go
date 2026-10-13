package coach

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fitbot/internal/anomaly"
	"fitbot/internal/llm"
	"fitbot/internal/metrics"
	"fitbot/internal/model"
)

var day = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

func sampleInput(alerts []anomaly.Alert) Input {
	var rows []metrics.DayRow
	for i := 0; i < 8; i++ {
		d := day.AddDate(0, 0, i-7)
		bed := d
		steps := 8000
		if i == 7 {
			steps = 7350
		}
		rows = append(rows, metrics.DayRow{Date: d,
			Activity: &model.Activity{Steps: model.Ptr(steps), WearOK: model.Ptr(true)},
			Night:    &model.SleepNight{Date: d, Bedtime: &bed, AsleepMin: model.Ptr(442), NightMetrics: model.NightMetrics{HRVms: model.Ptr(31.2)}}})
	}
	res := metrics.Compute(rows, day, &rows[0].Date, metrics.DefaultConfig())
	return Input{User: model.User{Age: model.Ptr(40)}, Metrics: res, Alerts: alerts}
}

// FR8: ส่งให้ LLM เฉพาะตัวเลขสรุปที่คำนวณแล้ว
func TestFactsAreComputedSummaries(t *testing.T) {
	f := BuildFacts(sampleInput(nil))
	b, _ := json.Marshal(f)
	s := string(b)
	for _, want := range []string{"7,350 ก้าว", "น้อยกว่าเฉลี่ย", "7 ชม. 22 นาที", "40 ปี", "8 วัน"} {
		if !strings.Contains(s, want) {
			t.Errorf("facts ไม่มี %q: %s", want, s)
		}
	}
	for _, banned := range []string{"raw", "dataPoints", "physicalTime"} {
		if strings.Contains(s, banned) {
			t.Errorf("ห้ามมีข้อมูลดิบ %q", banned)
		}
	}
}

type fakeLLM struct {
	out  string
	err  error
	msgs []llm.Message
}

func (f *fakeLLM) Chat(_ context.Context, m []llm.Message) (string, error) {
	f.msgs = m
	return f.out, f.err
}

func TestCoachParsesAndStripsThinking(t *testing.T) {
	f := &fakeLLM{out: `<think>คิด</think>{"summary":"เมื่อวานนอนได้ 7 ชม. 22 นาที","advice":["1. เดินเพิ่ม 15 นาทีหลังอาหารเย็น","ดื่มน้ำ"],"watch_explanation":"x"}`}
	c, err := Coach(context.Background(), f, BuildFacts(sampleInput(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Advice[0] != "เดินเพิ่ม 15 นาทีหลังอาหารเย็น" || c.WatchExplanation != "" {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(f.msgs[0].Content, "เป็นกันเอง") || !strings.Contains(f.msgs[0].Content, "ห้ามเพิ่มหรือตัด") {
		t.Fatal("system prompt ต้องกำหนดน้ำเสียง (FR26) และห้าม LLM ตัดสินค่าผิดปกติ (FR17)")
	}
}

func TestParseRejectsBadOutput(t *testing.T) {
	for _, bad := range []string{`ไม่ใช่ json`, `{"summary":"","advice":["a"]}`, `{"summary":"a","advice":[]}`, `{"summary":"a","advice":{"x":1}}`} {
		if _, err := Parse(bad, false); err == nil {
			t.Errorf("ต้องปฏิเสธ %q", bad)
		}
	}
	if c, err := Parse(`{"summary":"a","advice":"ข้อเดียว"}`, false); err != nil || len(c.Advice) != 1 {
		t.Fatalf("advice เป็นข้อความเดียวต้องรับได้: %v", err)
	}
}

func TestWatchItemsPassedForExplanationOnly(t *testing.T) {
	alerts := []anomaly.Alert{{Level: anomaly.LevelWatch, Label: "ชีพจรขณะพัก", Value: "105 bpm", Criterion: "มากกว่า 100 bpm"}}
	f := BuildFacts(sampleInput(alerts))
	if len(f.Watch) != 1 || len(f.Improve) != 0 {
		t.Fatalf("%+v", f)
	}
	c, _ := Parse(`{"summary":"a","advice":["b"],"watch_explanation":"อธิบาย"}`, true)
	if c.WatchExplanation != "อธิบาย" {
		t.Fatal("ต้องเก็บคำอธิบายเมื่อมีค่าที่ควรเฝ้าระวัง")
	}
}

// RK7: ตรวจคำตอบที่พบจริงจาก qwen3.5:9b ระหว่าง M3
func TestValidateRejectsRealFailures(t *testing.T) {
	facts := `{"เมื่อวาน":"7 ชม. 22 นาที","ก้าว":"7,350 ก้าว"}`
	bad := []Coaching{
		{Summary: "นอนได้ 7 ชม. 22 นาที แต่ใช้เวลา сидนิ่งนาน", Advice: []string{"เดิน"}}, // อักษรรัสเซียหลุด
		{Summary: "นอนได้ประมาณ 7.5 ชั่วโมง", Advice: []string{"เดิน"}},                   // ปัดเศษ
		{Summary: "เดิน 6,200 กว่าก้าว", Advice: []string{"เดิน"}},
		{Summary: "เมื่อวานนอน 7 ชม. 22 นาทีครับ", Advice: []string{"เดิน"}}, // ต้องลงท้าย ค่ะ                        // ตัวเลขไม่ตรง
	}
	for _, c := range bad {
		if err := Validate(&c, facts); err == nil {
			t.Errorf("ต้องปฏิเสธ %q", c.Summary)
		}
	}
	good := Coaching{Summary: "เมื่อวานนอน 7 ชม. 22 นาที และเดิน 7,350 ก้าว", Advice: []string{"เดินเร็ว 15 นาทีหลังอาหารเย็น"}}
	if err := Validate(&good, facts); err != nil {
		t.Fatal(err)
	}
}

func TestCoachRetriesThenFails(t *testing.T) {
	f := &countingLLM{outs: []string{`{"summary":"เดิน 9,999 ก้าว","advice":["a"]}`, `{"summary":"เมื่อวานดีมาก","advice":["เดินเพิ่ม 15 นาที"]}`}}
	c, err := Coach(context.Background(), f, BuildFacts(sampleInput(nil)))
	if err != nil || f.calls != 2 || c.Summary != "เมื่อวานดีมาก" {
		t.Fatalf("calls=%d err=%v", f.calls, err)
	}
	f = &countingLLM{outs: []string{`{"summary":"9,999","advice":["a"]}`, `{"summary":"8,888","advice":["a"]}`}}
	if _, err := Coach(context.Background(), f, BuildFacts(sampleInput(nil))); err == nil || f.calls != MaxAttempts {
		t.Fatal("ไม่ผ่านทุกครั้งต้องคืน error เพื่อใช้ fallback")
	}
}

type countingLLM struct {
	outs  []string
	calls int
}

func (c *countingLLM) Chat(context.Context, []llm.Message) (string, error) {
	c.calls++
	return c.outs[c.calls-1], nil
}
