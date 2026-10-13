package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"fitbot/internal/completeness"
	"fitbot/internal/dateutil"
	"fitbot/internal/metrics"
	"fitbot/internal/model"
)

// ErrAlreadyRunning: มีอีกโปรเซสถือ lock อยู่ (D3)
var ErrAlreadyRunning = errors.New("มี fitbot อีกโปรเซสกำลังทำงานอยู่ ลองใหม่เมื่อโปรเซสนั้นจบ")

// runLockKey คือ PostgreSQL advisory lock ที่กันรันซ้อน ใช้ร่วมกันทั้ง collect และ run
const runLockKey int64 = 0x666974626f74

// WithRunLock เรียก fn ภายใต้ lock ถ้ามีอีกโปรเซสถืออยู่คืน ErrAlreadyRunning ทันที
func (s *Store) WithRunLock(ctx context.Context, fn func() error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, runLockKey).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return ErrAlreadyRunning
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, runLockKey)
	return fn()
}

// ---------- daily_status (F6) ----------

func (s *Store) SaveStatus(ctx context.Context, userID int64, d time.Time, st completeness.Status) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO daily_status (user_id, date, activity_status, sleep_status, fetched_at, last_error)
		VALUES ($1, $2, $3, $4, now(), NULL)
		ON CONFLICT (user_id, date) DO UPDATE SET activity_status = EXCLUDED.activity_status,
			sleep_status = EXCLUDED.sleep_status, fetched_at = now(), last_error = NULL`, userID, d, st.Activity, st.Sleep)
	return err
}

// SaveFetchError บันทึกข้อผิดพลาดล่าสุดโดยไม่ลบสถานะเดิม
func (s *Store) SaveFetchError(ctx context.Context, userID int64, d time.Time, msg string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO daily_status (user_id, date, activity_status, sleep_status, last_error)
		VALUES ($1, $2, 'missing', 'missing', $3)
		ON CONFLICT (user_id, date) DO UPDATE SET last_error = EXCLUDED.last_error`, userID, d, msg)
	return err
}

// ---------- ข้อมูลย้อนหลังสำหรับ Metrics Engine (FR24) ----------

// History อ่าน days วันที่สิ้นสุดที่ end (รวม end) เรียงเก่าไปใหม่ ทุกวันมีหนึ่งแถว (วันที่ไม่มีข้อมูลเป็น nil)
func (s *Store) History(ctx context.Context, userID int64, end time.Time, days int) ([]metrics.DayRow, error) {
	start := end.AddDate(0, 0, -(days - 1))
	out := make([]metrics.DayRow, days)
	index := map[string]int{}
	for i := range out {
		out[i].Date = start.AddDate(0, 0, i)
		index[dateutil.Format(out[i].Date)] = i
	}
	rows, err := s.pool.Query(ctx, `SELECT date, steps, distance_m, energy_kcal, active_energy_kcal, active_zone_min,
		azm_fat_burn_min, azm_cardio_min, azm_peak_min, active_light_min, active_moderate_min, active_vigorous_min,
		sedentary_min, resting_hr, wear_minutes, wear_ok
		FROM daily_activity WHERE user_id = $1 AND date BETWEEN $2 AND $3`, userID, start, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d time.Time
		a := &model.Activity{}
		if err := rows.Scan(&d, &a.Steps, &a.DistanceM, &a.EnergyKcal, &a.ActiveEnergyKcal, &a.ActiveZoneMin,
			&a.AZMFatBurnMin, &a.AZMCardioMin, &a.AZMPeakMin, &a.ActiveLightMin, &a.ActiveModerateMin, &a.ActiveVigorousMin,
			&a.SedentaryMin, &a.RestingHR, &a.WearMinutes, &a.WearOK); err != nil {
			rows.Close()
			return nil, err
		}
		out[index[dateutil.Format(d)]].Activity = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT date, bedtime, wake_time, asleep_min, deep_min, light_min, rem_min, awake_min, nap_min,
		hrv_ms, spo2_avg, skin_temp_nightly_c, skin_temp_delta_c, respiratory_rate
		FROM sleep_nights WHERE user_id = $1 AND date BETWEEN $2 AND $3`, userID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		n := &model.SleepNight{}
		if err := rows.Scan(&n.Date, &n.Bedtime, &n.WakeTime, &n.AsleepMin, &n.DeepMin, &n.LightMin, &n.RemMin, &n.AwakeMin, &n.NapMin,
			&n.HRVms, &n.SpO2Avg, &n.SkinTempNightlyC, &n.SkinTempDeltaC, &n.RespiratoryRate); err != nil {
			return nil, err
		}
		out[index[dateutil.Format(n.Date)]].Night = n
	}
	return out, rows.Err()
}

// FirstDataDate คือวันแรกที่มีข้อมูลของผู้ใช้ (อธิบายจำนวนวันในค่าเฉลี่ย FR7)
func (s *Store) FirstDataDate(ctx context.Context, userID int64) (*time.Time, error) {
	var d *time.Time
	err := s.pool.QueryRow(ctx, `SELECT MIN(date) FROM (
		SELECT date FROM daily_activity WHERE user_id = $1 UNION ALL SELECT date FROM sleep_nights WHERE user_id = $1) t`, userID).Scan(&d)
	return d, err
}

// ---------- daily_reports (F12) ----------

const (
	ReportPending        = "pending"
	ReportSyncNoticeSent = "sync_notice_sent"
	ReportSent           = "report_sent"
	ReportPartialSent    = "partial_report_sent"
)

type Report struct {
	State            string
	SyncNoticeSentAt *time.Time
	ReportSentAt     *time.Time
	Content          *string
}

func (s *Store) GetReport(ctx context.Context, userID int64, d time.Time) (Report, error) {
	r := Report{State: ReportPending}
	err := s.pool.QueryRow(ctx, `SELECT state, sync_notice_sent_at, report_sent_at, content
		FROM daily_reports WHERE user_id = $1 AND report_date = $2`, userID, d).Scan(&r.State, &r.SyncNoticeSentAt, &r.ReportSentAt, &r.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	return r, err
}

func (s *Store) MarkSyncNoticeSent(ctx context.Context, userID int64, d, at time.Time, content string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO daily_reports (user_id, report_date, state, sync_notice_sent_at, content, updated_at)
		VALUES ($1, $2, 'sync_notice_sent', $3, $4, now())
		ON CONFLICT (user_id, report_date) DO UPDATE SET state = 'sync_notice_sent',
			sync_notice_sent_at = EXCLUDED.sync_notice_sent_at, content = EXCLUDED.content, updated_at = now()
		WHERE daily_reports.state = 'pending'`, userID, d, at, content)
	return err
}

// MarkReportSent บันทึกว่าส่งรายงานแล้ว (ไม่เขียนทับรายงานที่ส่งไปแล้ว เพื่อให้ไม่เกิน 1 ฉบับต่อวัน)
func (s *Store) MarkReportSent(ctx context.Context, userID int64, d, at time.Time, state, content string, llmUsed bool) error {
	if state != ReportSent && state != ReportPartialSent {
		return fmt.Errorf("state ไม่ถูกต้อง: %s", state)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO daily_reports (user_id, report_date, state, report_sent_at, content, llm_used, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (user_id, report_date) DO UPDATE SET state = EXCLUDED.state, report_sent_at = EXCLUDED.report_sent_at,
			content = EXCLUDED.content, llm_used = EXCLUDED.llm_used, updated_at = now()
		WHERE daily_reports.state NOT IN ('report_sent', 'partial_report_sent')`, userID, d, state, at, content, llmUsed)
	return err
}
