package completeness

import (
	"testing"
	"time"

	"fitbot/internal/model"
)

func TestEvaluate(t *testing.T) {
	bed := time.Now()
	night := &model.SleepNight{Bedtime: &bed}
	act := func(ok bool) *model.Activity {
		return &model.Activity{Steps: model.Ptr(100), WearMinutes: model.Ptr(800), WearOK: model.Ptr(ok)}
	}

	if st := Evaluate(act(true), night); !st.Complete() {
		t.Fatalf("สวมพอและมีการนอนหลักต้องครบ: %+v", st)
	}
	if st := Evaluate(act(false), night); st.Activity != InsufficientWear || st.Complete() {
		t.Fatalf("สวมไม่ถึงเกณฑ์ต้องเป็น insufficient_wear: %+v", st)
	}
	if st := Evaluate(act(true), &model.SleepNight{NapMin: model.Ptr(30)}); st.Sleep != Missing {
		t.Fatalf("มีแต่การงีบต้องถือว่าไม่มีการนอนหลัก: %+v", st)
	}
	st := Evaluate(nil, nil)
	if st.Activity != Missing || st.Sleep != Missing || len(MissingParts(st)) != 2 {
		t.Fatalf("%+v", st)
	}
	if got := MissingParts(Status{Activity: Complete, Sleep: Missing}); len(got) != 1 || got[0] != "ข้อมูลการนอน" {
		t.Fatalf("ต้องระบุได้ว่าขาดส่วนใด: %v", got)
	}
}
