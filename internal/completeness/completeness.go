// Package completeness ตัดสินความครบของข้อมูลวัน D (F6)
// ครบ = มีกิจกรรมที่สวมอุปกรณ์ ≥ min_ratio ของวัน (FR25) และมีการนอนหลักของคืน D
package completeness

import (
	"fitbot/internal/model"
)

const (
	Complete         = "complete"
	Missing          = "missing"
	InsufficientWear = "insufficient_wear"
)

type Status struct {
	Activity string
	Sleep    string
}

func (s Status) Complete() bool { return s.Activity == Complete && s.Sleep == Complete }

// Evaluate คืนสถานะความครบ ระบุได้ว่าขาดส่วนใด
func Evaluate(a *model.Activity, night *model.SleepNight) Status {
	st := Status{Activity: Complete, Sleep: Complete}
	switch {
	case !a.HasData():
		st.Activity = Missing
	case a.WearOK == nil || !*a.WearOK:
		// ไม่มีข้อมูลเวลาสวมใส่ = ยืนยันไม่ได้ว่าสวมพอ (OQ5.1 ทางเลือก A)
		st.Activity = InsufficientWear
	}
	if !night.HasMainSleep() {
		st.Sleep = Missing
	}
	return st
}

// MissingParts คืนคำอธิบายภาษาไทยของส่วนที่ขาด
func MissingParts(st Status) []string {
	var out []string
	switch st.Activity {
	case Missing:
		out = append(out, "ข้อมูลกิจกรรม")
	case InsufficientWear:
		out = append(out, "ข้อมูลกิจกรรม (สวมอุปกรณ์ไม่ถึงครึ่งวัน)")
	}
	if st.Sleep != Complete {
		out = append(out, "ข้อมูลการนอน")
	}
	return out
}
