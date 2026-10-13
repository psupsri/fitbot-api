// Package store อ่านและเขียนฐานข้อมูล ข้อมูลรายวันเขียนแบบ upsert เพื่อให้ดึงซ้ำแล้วเขียนทับได้ ไม่ซ้ำ
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"fitbot/internal/model"
)

var ErrNotFound = errors.New("ไม่พบข้อมูล")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ---------- users ----------

const userCols = `id, display_name, timezone, age, age_updated_at, line_user_id, status,
	sleep_window_start, sleep_window_end, resync_gap_minutes, created_at`

func scanUser(row pgx.Row) (model.User, error) {
	var u model.User
	var ws, we pgtype.Time
	err := row.Scan(&u.ID, &u.DisplayName, &u.Timezone, &u.Age, &u.AgeUpdatedAt, &u.LineUserID, &u.Status,
		&ws, &we, &u.ResyncGapMinutes, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	u.SleepWindowStart = int(ws.Microseconds / 60_000_000)
	u.SleepWindowEnd = int(we.Microseconds / 60_000_000)
	return u, err
}

func clock(min int) pgtype.Time {
	return pgtype.Time{Microseconds: int64(min) * 60_000_000, Valid: true}
}

func (s *Store) CreateUser(ctx context.Context, u model.User) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `INSERT INTO users
		(display_name, timezone, age, age_updated_at, line_user_id, status, sleep_window_start, sleep_window_end, resync_gap_minutes)
		VALUES ($1, $2, $3, CASE WHEN $3::int IS NULL THEN NULL ELSE now() END, $4, $5, $6, $7, $8)
		RETURNING `+userCols,
		u.DisplayName, u.Timezone, u.Age, u.LineUserID, u.Status, clock(u.SleepWindowStart), clock(u.SleepWindowEnd), u.ResyncGapMinutes))
}

func (s *Store) UpdateUser(ctx context.Context, u model.User) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `UPDATE users SET display_name = $2, timezone = $3, line_user_id = $4, status = $5,
		sleep_window_start = $6, sleep_window_end = $7, resync_gap_minutes = $8, updated_at = now()
		WHERE id = $1 RETURNING `+userCols,
		u.ID, u.DisplayName, u.Timezone, u.LineUserID, u.Status, clock(u.SleepWindowStart), clock(u.SleepWindowEnd), u.ResyncGapMinutes))
}

func (s *Store) UpdateUserAge(ctx context.Context, userID int64, age int) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET age = $2, age_updated_at = now(), updated_at = now() WHERE id = $1`, userID, age)
	return err
}

func (s *Store) GetUser(ctx context.Context, id int64) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) ListUsers(ctx context.Context, activeOnly bool) ([]model.User, error) {
	q := `SELECT ` + userCols + ` FROM users`
	if activeOnly {
		q += ` WHERE status = 'active'`
	}
	rows, err := s.pool.Query(ctx, q+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---------- oauth_credentials ----------

func (s *Store) GetCredential(ctx context.Context, userID int64) (model.Credential, error) {
	var c model.Credential
	err := s.pool.QueryRow(ctx, `SELECT user_id, health_user_id, refresh_token_enc, access_token_enc, access_expires_at,
		refresh_expires_at, scopes, status, expiry_warned_at, invalid_notified_at
		FROM oauth_credentials WHERE user_id = $1`, userID).
		Scan(&c.UserID, &c.HealthUserID, &c.RefreshTokenEnc, &c.AccessTokenEnc, &c.AccessExpiresAt,
			&c.RefreshExpiresAt, &c.Scopes, &c.Status, &c.ExpiryWarnedAt, &c.InvalidNotifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Credential{UserID: userID, Status: model.CredNotLinked}, ErrNotFound
	}
	return c, err
}

// SaveNewCredential บันทึก token หลัง re-auth และล้างสถานะการเตือน
func (s *Store) SaveNewCredential(ctx context.Context, c model.Credential) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO oauth_credentials
		(user_id, health_user_id, refresh_token_enc, access_token_enc, access_expires_at, refresh_expires_at, scopes, status, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (user_id) DO UPDATE SET health_user_id = EXCLUDED.health_user_id,
			refresh_token_enc = EXCLUDED.refresh_token_enc, access_token_enc = EXCLUDED.access_token_enc,
			access_expires_at = EXCLUDED.access_expires_at, refresh_expires_at = EXCLUDED.refresh_expires_at,
			scopes = EXCLUDED.scopes, status = EXCLUDED.status,
			expiry_warned_at = NULL, invalid_notified_at = NULL, updated_at = now()`,
		c.UserID, c.HealthUserID, c.RefreshTokenEnc, c.AccessTokenEnc, c.AccessExpiresAt, c.RefreshExpiresAt, c.Scopes, c.Status)
	return err
}

// UpdateAccessToken บันทึก access token ใหม่หลัง refresh (และ refresh token ถ้าถูกหมุนเวียน)
func (s *Store) UpdateAccessToken(ctx context.Context, userID int64, accessEnc []byte, expires time.Time, refreshEnc []byte) error {
	_, err := s.pool.Exec(ctx, `UPDATE oauth_credentials SET access_token_enc = $2, access_expires_at = $3,
		refresh_token_enc = COALESCE($4, refresh_token_enc), updated_at = now() WHERE user_id = $1`,
		userID, accessEnc, expires, refreshEnc)
	return err
}

func (s *Store) SetCredentialStatus(ctx context.Context, userID int64, status string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO oauth_credentials (user_id, status) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET status = EXCLUDED.status, updated_at = now()`, userID, status)
	return err
}

func (s *Store) MarkExpiryWarned(ctx context.Context, userID int64, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE oauth_credentials SET expiry_warned_at = $2 WHERE user_id = $1`, userID, at)
	return err
}

func (s *Store) MarkInvalidNotified(ctx context.Context, userID int64, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE oauth_credentials SET invalid_notified_at = $2 WHERE user_id = $1`, userID, at)
	return err
}

// ---------- ข้อมูลรายวัน ----------

// SaveDay บันทึกกิจกรรม ช่วงการนอน และสรุปการนอนของวัน D ใน transaction เดียว (เขียนทับ)
// activity ที่ไม่มีค่าเลยหรือ night ที่ว่างจะไม่ถูกเขียน (ไม่มีแถว = ไม่มีข้อมูล)
func (s *Store) SaveDay(ctx context.Context, userID int64, d time.Time, a *model.Activity, sessions []model.SleepSession, night *model.SleepNight) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if a.HasData() {
			if _, err := tx.Exec(ctx, `INSERT INTO daily_activity
				(user_id, date, steps, distance_m, energy_kcal, active_energy_kcal, active_zone_min, azm_fat_burn_min,
				 azm_cardio_min, azm_peak_min, active_light_min, active_moderate_min, active_vigorous_min, sedentary_min,
				 resting_hr, wear_minutes, wear_ok, raw, fetched_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18, now())
				ON CONFLICT (user_id, date) DO UPDATE SET steps = EXCLUDED.steps, distance_m = EXCLUDED.distance_m,
					energy_kcal = EXCLUDED.energy_kcal, active_energy_kcal = EXCLUDED.active_energy_kcal,
					active_zone_min = EXCLUDED.active_zone_min, azm_fat_burn_min = EXCLUDED.azm_fat_burn_min,
					azm_cardio_min = EXCLUDED.azm_cardio_min, azm_peak_min = EXCLUDED.azm_peak_min,
					active_light_min = EXCLUDED.active_light_min, active_moderate_min = EXCLUDED.active_moderate_min,
					active_vigorous_min = EXCLUDED.active_vigorous_min, sedentary_min = EXCLUDED.sedentary_min,
					resting_hr = EXCLUDED.resting_hr, wear_minutes = EXCLUDED.wear_minutes, wear_ok = EXCLUDED.wear_ok,
					raw = EXCLUDED.raw, fetched_at = now()`,
				userID, d, a.Steps, a.DistanceM, a.EnergyKcal, a.ActiveEnergyKcal, a.ActiveZoneMin, a.AZMFatBurnMin,
				a.AZMCardioMin, a.AZMPeakMin, a.ActiveLightMin, a.ActiveModerateMin, a.ActiveVigorousMin, a.SedentaryMin,
				a.RestingHR, a.WearMinutes, a.WearOK, jsonb(a.Raw)); err != nil {
				return fmt.Errorf("บันทึก daily_activity: %w", err)
			}
		}
		for _, ss := range sessions {
			if _, err := tx.Exec(ctx, `INSERT INTO sleep_sessions
				(user_id, external_id, start_at, end_at, kind, attributed_date, asleep_min, awake_min, deep_min,
				 light_min, rem_min, api_main_sleep, raw, fetched_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, now())
				ON CONFLICT (user_id, external_id) DO UPDATE SET start_at = EXCLUDED.start_at, end_at = EXCLUDED.end_at,
					kind = EXCLUDED.kind, attributed_date = EXCLUDED.attributed_date, asleep_min = EXCLUDED.asleep_min,
					awake_min = EXCLUDED.awake_min, deep_min = EXCLUDED.deep_min, light_min = EXCLUDED.light_min,
					rem_min = EXCLUDED.rem_min, api_main_sleep = EXCLUDED.api_main_sleep, raw = EXCLUDED.raw, fetched_at = now()`,
				userID, ss.ExternalID, ss.Start, ss.End, ss.Kind, ss.AttributedDate, ss.AsleepMin, ss.AwakeMin,
				ss.DeepMin, ss.LightMin, ss.RemMin, ss.APIMainSleep, jsonb(ss.Raw)); err != nil {
				return fmt.Errorf("บันทึก sleep_sessions: %w", err)
			}
		}
		if night != nil && (night.HasMainSleep() || night.NapMin != nil || night.NightMetrics.HasData()) {
			if _, err := tx.Exec(ctx, `INSERT INTO sleep_nights
				(user_id, date, bedtime, wake_time, asleep_min, deep_min, light_min, rem_min, awake_min, nap_min,
				 hrv_ms, spo2_avg, skin_temp_nightly_c, skin_temp_delta_c, respiratory_rate, fetched_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15, now())
				ON CONFLICT (user_id, date) DO UPDATE SET bedtime = EXCLUDED.bedtime, wake_time = EXCLUDED.wake_time,
					asleep_min = EXCLUDED.asleep_min, deep_min = EXCLUDED.deep_min, light_min = EXCLUDED.light_min,
					rem_min = EXCLUDED.rem_min, awake_min = EXCLUDED.awake_min, nap_min = EXCLUDED.nap_min,
					hrv_ms = EXCLUDED.hrv_ms, spo2_avg = EXCLUDED.spo2_avg, skin_temp_nightly_c = EXCLUDED.skin_temp_nightly_c,
					skin_temp_delta_c = EXCLUDED.skin_temp_delta_c, respiratory_rate = EXCLUDED.respiratory_rate, fetched_at = now()`,
				userID, d, night.Bedtime, night.WakeTime, night.AsleepMin, night.DeepMin, night.LightMin, night.RemMin,
				night.AwakeMin, night.NapMin, night.HRVms, night.SpO2Avg, night.SkinTempNightlyC, night.SkinTempDeltaC,
				night.RespiratoryRate); err != nil {
				return fmt.Errorf("บันทึก sleep_nights: %w", err)
			}
		}
		return nil
	})
}

// GetDay อ่านข้อมูลที่บันทึกของวัน D (ใช้แสดงผลและทดสอบ) ไม่มีแถวคืน nil
func (s *Store) GetDay(ctx context.Context, userID int64, d time.Time) (*model.Activity, *model.SleepNight, error) {
	a := &model.Activity{}
	err := s.pool.QueryRow(ctx, `SELECT steps, distance_m, energy_kcal, active_energy_kcal, active_zone_min, azm_fat_burn_min,
		azm_cardio_min, azm_peak_min, active_light_min, active_moderate_min, active_vigorous_min, sedentary_min, resting_hr,
		wear_minutes, wear_ok FROM daily_activity WHERE user_id = $1 AND date = $2`, userID, d).
		Scan(&a.Steps, &a.DistanceM, &a.EnergyKcal, &a.ActiveEnergyKcal, &a.ActiveZoneMin, &a.AZMFatBurnMin, &a.AZMCardioMin,
			&a.AZMPeakMin, &a.ActiveLightMin, &a.ActiveModerateMin, &a.ActiveVigorousMin, &a.SedentaryMin, &a.RestingHR,
			&a.WearMinutes, &a.WearOK)
	if errors.Is(err, pgx.ErrNoRows) {
		a = nil
	} else if err != nil {
		return nil, nil, err
	}
	n := &model.SleepNight{Date: d}
	err = s.pool.QueryRow(ctx, `SELECT bedtime, wake_time, asleep_min, deep_min, light_min, rem_min, awake_min, nap_min,
		hrv_ms, spo2_avg, skin_temp_nightly_c, skin_temp_delta_c, respiratory_rate
		FROM sleep_nights WHERE user_id = $1 AND date = $2`, userID, d).
		Scan(&n.Bedtime, &n.WakeTime, &n.AsleepMin, &n.DeepMin, &n.LightMin, &n.RemMin, &n.AwakeMin, &n.NapMin,
			&n.HRVms, &n.SpO2Avg, &n.SkinTempNightlyC, &n.SkinTempDeltaC, &n.RespiratoryRate)
	if errors.Is(err, pgx.ErrNoRows) {
		n = nil
	} else if err != nil {
		return nil, nil, err
	}
	return a, n, nil
}

// ---------- job_runs ----------

func (s *Store) StartJob(ctx context.Context, summary any) (int64, error) {
	b, err := json.Marshal(summary)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx, `INSERT INTO job_runs (summary) VALUES ($1) RETURNING id`, b).Scan(&id)
	return id, err
}

func (s *Store) FinishJob(ctx context.Context, id int64, status string, summary any) error {
	b, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE job_runs SET finished_at = now(), status = $2, summary = $3 WHERE id = $1`, id, status, b)
	return err
}

func jsonb(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}
