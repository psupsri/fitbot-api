-- Fitbot schema v1 — Database Design ที่อนุมัติที่ Gate M0 (13 ต.ค. 2026)
-- วันที่ (DATE) เป็นวันตาม timezone ของผู้ใช้ เวลา (TIMESTAMPTZ) เก็บเป็น UTC
-- ค่าที่ไม่มีเป็น NULL เสมอ ห้ามใช้ 0 แทน (FR4)

CREATE TABLE users (
    id                  BIGSERIAL PRIMARY KEY,
    display_name        TEXT        NOT NULL,
    timezone            TEXT        NOT NULL DEFAULT 'Asia/Bangkok',
    age                 INT         CHECK (age IS NULL OR age BETWEEN 1 AND 130),
    age_updated_at      TIMESTAMPTZ,
    line_user_id        TEXT,
    status              TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    sleep_window_start  TIME        NOT NULL DEFAULT '18:00',
    sleep_window_end    TIME        NOT NULL DEFAULT '12:00',
    resync_gap_minutes  INT         NOT NULL DEFAULT 120 CHECK (resync_gap_minutes > 0),
    terms_accepted_at   TIMESTAMPTZ, -- สงวนไว้สำหรับ T&C ในอนาคต (OQ8)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE oauth_credentials (
    user_id             BIGINT      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    health_user_id      TEXT,        -- จาก users/me/identity
    refresh_token_enc   BYTEA,       -- เข้ารหัส AES-256-GCM กุญแจอยู่นอกฐานข้อมูล (D7)
    access_token_enc    BYTEA,
    access_expires_at   TIMESTAMPTZ,
    refresh_expires_at  TIMESTAMPTZ,
    scopes              TEXT[]      NOT NULL DEFAULT '{}',
    status              TEXT        NOT NULL DEFAULT 'not_linked'
                        CHECK (status IN ('valid', 'expiring', 'invalid', 'not_linked')),
    expiry_warned_at    TIMESTAMPTZ, -- กันเตือน token ใกล้หมดอายุซ้ำ (D8)
    invalid_notified_at TIMESTAMPTZ, -- กันแจ้ง token ใช้ไม่ได้ซ้ำ (D7)
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE daily_activity (
    user_id             BIGINT           NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    date                DATE             NOT NULL,
    steps               INT,
    distance_m          DOUBLE PRECISION,
    energy_kcal         DOUBLE PRECISION, -- total-calories (รวม BMR)
    active_energy_kcal  DOUBLE PRECISION, -- active-energy-burned
    active_zone_min     INT,              -- fat burn + cardio + peak ไม่คูณ (ตรงกับแอป)
    azm_fat_burn_min    INT,
    azm_cardio_min      INT,
    azm_peak_min        INT,
    active_light_min    INT,              -- ข้อมูลเสริมจาก active-minutes
    active_moderate_min INT,
    active_vigorous_min INT,
    sedentary_min       INT,
    resting_hr          DOUBLE PRECISION,
    wear_minutes        INT,              -- จำนวนนาทีที่มีค่าชีพจร (OQ5.1 ทางเลือก A)
    wear_ok             BOOLEAN,          -- false = ค่ากิจกรรมของวันนี้ไม่ถูกนำมาคิด (FR25)
    raw                 JSONB,
    fetched_at          TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, date)
);

CREATE TABLE sleep_sessions (
    id               BIGSERIAL PRIMARY KEY,
    user_id          BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    external_id      TEXT        NOT NULL,
    start_at         TIMESTAMPTZ NOT NULL,
    end_at           TIMESTAMPTZ NOT NULL,
    kind             TEXT        NOT NULL CHECK (kind IN ('main', 'nap')),
    attributed_date  DATE        NOT NULL,
    asleep_min       INT,
    awake_min        INT,
    deep_min         INT,
    light_min        INT,
    rem_min          INT,
    api_main_sleep   BOOLEAN,    -- metadata.mainSleep ของ API ใช้เทียบกับกฎ FR19
    raw              JSONB,
    fetched_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, external_id),
    CHECK (end_at > start_at)
);
CREATE INDEX sleep_sessions_user_date ON sleep_sessions (user_id, attributed_date);

CREATE TABLE sleep_nights (
    user_id              BIGINT           NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    date                 DATE             NOT NULL,
    bedtime              TIMESTAMPTZ,
    wake_time            TIMESTAMPTZ,
    asleep_min           INT,
    deep_min             INT,
    light_min            INT,
    rem_min              INT,
    awake_min            INT,
    nap_min              INT,
    -- ค่าที่วัดขณะนอน API ผูกกับวันที่ตื่น (D+1) แต่เก็บไว้ที่คืนวัน D
    hrv_ms               DOUBLE PRECISION,
    spo2_avg             DOUBLE PRECISION,
    skin_temp_nightly_c  DOUBLE PRECISION,
    skin_temp_delta_c    DOUBLE PRECISION,
    respiratory_rate     DOUBLE PRECISION,
    fetched_at           TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, date)
);

CREATE TABLE daily_status (
    user_id          BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    date             DATE        NOT NULL,
    activity_status  TEXT        NOT NULL CHECK (activity_status IN ('complete', 'missing', 'insufficient_wear')),
    sleep_status     TEXT        NOT NULL CHECK (sleep_status IN ('complete', 'missing', 'insufficient_wear')),
    fetched_at       TIMESTAMPTZ,
    last_error       TEXT,
    PRIMARY KEY (user_id, date)
);

CREATE TABLE daily_reports (
    user_id              BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    report_date          DATE        NOT NULL,
    state                TEXT        NOT NULL DEFAULT 'pending'
                         CHECK (state IN ('pending', 'sync_notice_sent', 'report_sent', 'partial_report_sent')),
    sync_notice_sent_at  TIMESTAMPTZ,
    report_sent_at       TIMESTAMPTZ,
    content              TEXT,
    llm_used             BOOLEAN,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, report_date)
);

CREATE TABLE job_runs (
    id           BIGSERIAL PRIMARY KEY,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,
    status       TEXT        NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'success', 'partial_failure', 'failed')),
    summary      JSONB
);
