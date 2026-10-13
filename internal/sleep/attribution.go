// Package sleep จัดช่วงการนอนเป็นการนอนหลักหรือการงีบ แล้วสรุปการนอนของคืนวัน D (F5)
//
// กติกา (FR19, FR23):
//   - ช่วงคืนวัน D = [D sleep_window_start, D+1 sleep_window_end) ตั้งค่าได้ต่อผู้ใช้ (ค่าเริ่มต้น 18:00–12:00)
//   - ช่วงการนอนที่ "เริ่ม" ในช่วงคืนวัน D เป็นผู้สมัครการนอนหลักของวัน D
//   - ช่วงที่ยาวที่สุดเป็นแกน ช่วงอื่นที่ห่างจากกลุ่มไม่เกิน merge_gap รวมเป็นการนอนหลักเดียวกัน
//     (กันการนอนขาดตอนถูกนับเป็น 2 ช่วง) ที่เหลือในช่วงคืนนั้นเป็นการงีบ
//   - ช่วงการนอนที่เริ่มนอกช่วงคืนเป็นการงีบของวันที่ช่วงนั้นเริ่ม
//
// metadata.mainSleep ของ API ถูกเก็บไว้เทียบเท่านั้น กฎข้างบนเป็นตัวตัดสิน
package sleep

import (
	"sort"
	"time"

	"fitbot/internal/dateutil"
	"fitbot/internal/model"
)

type Window struct {
	StartMin int // นาทีนับจากเที่ยงคืนวัน D
	EndMin   int // นาทีนับจากเที่ยงคืนวัน D+1 (ถ้า EndMin > StartMin ถือว่าอยู่วันเดียวกับ StartMin)
	Loc      *time.Location
	MergeGap time.Duration
}

func WindowFor(u model.User, mergeGapMin int) Window {
	return Window{StartMin: u.SleepWindowStart, EndMin: u.SleepWindowEnd, Loc: u.Location(), MergeGap: time.Duration(mergeGapMin) * time.Minute}
}

// NightRange คือช่วงเวลาจริงของคืนวัน D
func (w Window) NightRange(d time.Time) (time.Time, time.Time) {
	start := dateutil.At(d, w.StartMin, w.Loc)
	endDay := d
	if w.EndMin <= w.StartMin {
		endDay = d.AddDate(0, 0, 1)
	}
	return start, dateutil.At(endDay, w.EndMin, w.Loc)
}

// FetchRange คือช่วงเวลาเริ่มของช่วงการนอนที่ต้องดึง เพื่อได้ทั้งการนอนหลักและการงีบของวัน D:
// ตั้งแต่สิ้นสุดช่วงคืนก่อนหน้า ถึงสิ้นสุดช่วงคืนวัน D
func (w Window) FetchRange(d time.Time) (time.Time, time.Time) {
	_, prevEnd := w.NightRange(d.AddDate(0, 0, -1))
	_, end := w.NightRange(d)
	return prevEnd, end
}

// DateOf คืนวันที่ที่ช่วงการนอนซึ่งเริ่มเวลา start เป็นของ และอยู่ในช่วงคืนหรือไม่
func (w Window) DateOf(start time.Time) (time.Time, bool) {
	local := dateutil.Date(start.In(w.Loc))
	for _, d := range []time.Time{local, local.AddDate(0, 0, -1)} {
		s, e := w.NightRange(d)
		if !start.Before(s) && start.Before(e) {
			return d, true
		}
	}
	return local, false
}

// Attribute กำหนด Kind และ AttributedDate ให้ทุกช่วง แล้วคืนสรุปคืนวัน D
func Attribute(d time.Time, sessions []model.SleepSession, w Window, metrics model.NightMetrics) ([]model.SleepSession, *model.SleepNight) {
	out := make([]model.SleepSession, len(sessions))
	copy(out, sessions)
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })

	nightIdx := map[time.Time][]int{}
	for i := range out {
		date, inNight := w.DateOf(out[i].Start)
		out[i].AttributedDate = date
		out[i].Kind = model.SleepNap
		if inNight {
			nightIdx[date] = append(nightIdx[date], i)
		}
	}
	for _, idx := range nightIdx {
		for _, i := range mainCluster(out, idx, w.MergeGap) {
			out[i].Kind = model.SleepMain
		}
	}

	night := &model.SleepNight{Date: d, NightMetrics: metrics}
	var asleep, deep, light, rem, awake, nap sum
	for _, s := range out {
		if !s.AttributedDate.Equal(d) {
			continue
		}
		if s.Kind == model.SleepNap {
			nap.add(model.Ptr(s.Minutes()))
			continue
		}
		if night.Bedtime == nil || s.Start.Before(*night.Bedtime) {
			night.Bedtime = model.Ptr(s.Start)
		}
		if night.WakeTime == nil || s.End.After(*night.WakeTime) {
			night.WakeTime = model.Ptr(s.End)
		}
		asleep.add(model.Ptr(s.Minutes()))
		deep.add(s.DeepMin)
		light.add(s.LightMin)
		rem.add(s.RemMin)
		awake.add(s.AwakeMin)
	}
	night.AsleepMin, night.DeepMin, night.LightMin, night.RemMin, night.AwakeMin = asleep.ptr(), deep.ptr(), light.ptr(), rem.ptr(), awake.ptr()
	night.NapMin = nap.ptr()
	return out, night
}

// mainCluster เลือกช่วงที่ยาวที่สุดเป็นแกน แล้วขยายทั้งสองทางตราบที่ช่องว่าง ≤ gap (idx เรียงตามเวลาเริ่มแล้ว)
func mainCluster(s []model.SleepSession, idx []int, gap time.Duration) []int {
	if len(idx) == 0 {
		return nil
	}
	anchor := 0
	for k := range idx {
		if s[idx[k]].Minutes() > s[idx[anchor]].Minutes() {
			anchor = k
		}
	}
	lo, hi := anchor, anchor
	start, end := s[idx[anchor]].Start, s[idx[anchor]].End
	for changed := true; changed; {
		changed = false
		if lo > 0 && start.Sub(s[idx[lo-1]].End) <= gap {
			lo--
			if s[idx[lo]].Start.Before(start) {
				start = s[idx[lo]].Start
			}
			changed = true
		}
		if hi < len(idx)-1 && s[idx[hi+1]].Start.Sub(end) <= gap {
			hi++
			if s[idx[hi]].End.After(end) {
				end = s[idx[hi]].End
			}
			changed = true
		}
	}
	return idx[lo : hi+1]
}

// sum รวมค่าที่อาจเป็น nil: ถ้าไม่มีค่าเลยผลเป็น nil ไม่ใช่ 0 (FR4)
type sum struct {
	v   int
	any bool
}

func (s *sum) add(p *int) {
	if p != nil {
		s.v += *p
		s.any = true
	}
}

func (s sum) ptr() *int {
	if !s.any {
		return nil
	}
	return model.Ptr(s.v)
}
