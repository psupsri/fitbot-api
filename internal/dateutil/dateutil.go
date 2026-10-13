// Package dateutil จัดการ "วันที่" ตาม timezone ของผู้ใช้
// วันที่ (DATE) แทนด้วย time.Time เวลา 00:00 UTC เพื่อเทียบและเก็บลง PostgreSQL ได้ตรงกัน
package dateutil

import (
	"fmt"
	"time"
)

const Layout = "2006-01-02"

// Date คืนวันที่ปฏิทินของ t ตาม location ของ t (00:00 UTC)
func Date(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func Parse(s string) (time.Time, error) {
	t, err := time.Parse(Layout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("วันที่ %q ไม่ถูกต้อง ต้องเป็น YYYY-MM-DD", s)
	}
	return t, nil
}

func Format(d time.Time) string { return d.Format(Layout) }

// Yesterday คืน "เมื่อวาน" ตาม timezone ของผู้ใช้ (FR18)
func Yesterday(now time.Time, loc *time.Location) time.Time {
	return Date(now.In(loc)).AddDate(0, 0, -1)
}

// At คืนเวลาจริงของวันที่ d เวลา clockMin (นาทีนับจากเที่ยงคืน) ใน loc
func At(d time.Time, clockMin int, loc *time.Location) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), clockMin/60, clockMin%60, 0, 0, loc)
}

// ParseClock แปลง "HH:MM" เป็นนาทีนับจากเที่ยงคืน
func ParseClock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("เวลา %q ไม่ถูกต้อง ต้องเป็น HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func FormatClock(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }
