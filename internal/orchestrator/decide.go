package orchestrator

import (
	"time"

	"fitbot/internal/store"
)

type Action string

const (
	ActSkip          Action = "skip"           // กฎ 1: ส่งรายงานแล้ววันนี้
	ActFullReport    Action = "full_report"    // กฎ 2: ข้อมูลครบ
	ActSyncNotice    Action = "sync_notice"    // กฎ 3: ยังไม่เคยแจ้ง → ส่งข้อความให้เปิดแอปเพื่อ sync
	ActWait          Action = "wait"           // กฎ 4: แจ้งแล้ว ห่างไม่ถึง resync gap → ไม่ส่ง แสดงเวลาที่ควรรันใหม่
	ActPartialReport Action = "partial_report" // กฎ 5: แจ้งแล้ว ห่าง ≥ resync gap → รายงานบางส่วน
)

type Decision struct {
	Action  Action
	RetryAt time.Time // ActWait: เวลาที่ควรรันใหม่
}

// Decide ตัดสินตามกฎ 5 ข้อ (FR10–13) จากสถานะการส่ง ไม่ใช่เวลาที่เรียก (Q30)
func Decide(r store.Report, complete bool, now time.Time, gap time.Duration) Decision {
	switch r.State {
	case store.ReportSent, store.ReportPartialSent:
		return Decision{Action: ActSkip}
	}
	if complete {
		return Decision{Action: ActFullReport}
	}
	if r.State != store.ReportSyncNoticeSent || r.SyncNoticeSentAt == nil {
		return Decision{Action: ActSyncNotice}
	}
	if retryAt := r.SyncNoticeSentAt.Add(gap); now.Before(retryAt) {
		return Decision{Action: ActWait, RetryAt: retryAt}
	}
	return Decision{Action: ActPartialReport}
}
