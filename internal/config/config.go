// Package config โหลดการตั้งค่าจาก config.yaml และค่าลับจาก environment หรือไฟล์ .env
// ค่าลับห้ามอยู่ใน config.yaml หรือในโค้ด (F1)
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"

	"fitbot/internal/llm"
	"fitbot/internal/metrics"
)

type Config struct {
	// ค่าลับจาก environment
	DatabaseURL        string `yaml:"-"` // FITBOT_DATABASE_URL
	EncryptionKey      string `yaml:"-"` // FITBOT_ENCRYPTION_KEY
	GoogleClientID     string `yaml:"-"` // GOOGLE_HEALTH_CLIENT_ID
	GoogleClientSecret string `yaml:"-"` // GOOGLE_HEALTH_SECRET
	LINEToken          string `yaml:"-"` // LINE_CHANNEL_ACCESS_TOKEN

	UserDefaults UserDefaults   `yaml:"user_defaults"`
	Google       Google         `yaml:"google_health"`
	Sleep        Sleep          `yaml:"sleep"`
	Wear         Wear           `yaml:"wear"`
	LINE         LINE           `yaml:"line"`
	Analysis     metrics.Config `yaml:"analysis"`
	Anomaly      Anomaly        `yaml:"anomaly"`
	Report       Report         `yaml:"report"`
	LLM          llm.Config     `yaml:"llm"`
}

type Anomaly struct {
	RulesFile string `yaml:"rules_file"` // ว่าง = ใช้กฎตั้งต้นที่ฝังใน binary (internal/anomaly/rules.yaml)
}

type Report struct {
	MaxMessageRunes int `yaml:"max_message_runes"`
}

type UserDefaults struct {
	Timezone         string `yaml:"timezone"`
	SleepWindowStart string `yaml:"sleep_window_start"`
	SleepWindowEnd   string `yaml:"sleep_window_end"`
	ResyncGapMinutes int    `yaml:"resync_gap_minutes"`
}

type Google struct {
	BaseURL              string `yaml:"base_url"`
	RedirectPort         int    `yaml:"redirect_port"`
	RefreshTokenTTLHours int    `yaml:"refresh_token_ttl_hours"`
	WarnBeforeHours      int    `yaml:"warn_before_hours"`
	AgeRefreshDays       int    `yaml:"age_refresh_days"`
	MaxRetries           int    `yaml:"max_retries"`
}

func (g Google) RefreshTokenTTL() time.Duration {
	return time.Duration(g.RefreshTokenTTLHours) * time.Hour
}
func (g Google) WarnBefore() time.Duration { return time.Duration(g.WarnBeforeHours) * time.Hour }

type Sleep struct {
	MergeGapMinutes int `yaml:"merge_gap_minutes"`
}

type Wear struct {
	MinRatio float64 `yaml:"min_ratio"`
}

type LINE struct {
	BaseURL string `yaml:"base_url"`
}

func Default() *Config {
	return &Config{
		UserDefaults: UserDefaults{Timezone: "Asia/Bangkok", SleepWindowStart: "18:00", SleepWindowEnd: "12:00", ResyncGapMinutes: 120},
		Google: Google{BaseURL: "https://health.googleapis.com", RedirectPort: 8765, RefreshTokenTTLHours: 168,
			WarnBeforeHours: 24, AgeRefreshDays: 30, MaxRetries: 3},
		Sleep:    Sleep{MergeGapMinutes: 90},
		Wear:     Wear{MinRatio: 0.5},
		LINE:     LINE{BaseURL: "https://api.line.me"},
		Analysis: metrics.DefaultConfig(),
		Report:   Report{MaxMessageRunes: 4500},
		LLM:      llm.DefaultConfig(),
	}
}

// LoadEnv อ่าน .env ในโฟลเดอร์ปัจจุบันและโฟลเดอร์แม่ (ค่าที่ตั้งไว้แล้วใน environment ไม่ถูกเขียนทับ)
func LoadEnv() {
	for _, p := range []string{".env", "../.env"} {
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
		}
	}
}

// Load อ่าน config.yaml (ถ้ามี) และค่าลับจาก environment
func Load(path string) (*Config, error) {
	LoadEnv()
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil && !(errors.Is(err, os.ErrNotExist) && path == "config.yaml") {
			return nil, fmt.Errorf("อ่าน %s: %w", path, err)
		}
		if err == nil {
			if err := yaml.Unmarshal(b, cfg); err != nil {
				return nil, fmt.Errorf("แปลง %s: %w", path, err)
			}
		}
	}
	cfg.DatabaseURL = os.Getenv("FITBOT_DATABASE_URL")
	cfg.EncryptionKey = os.Getenv("FITBOT_ENCRYPTION_KEY")
	cfg.GoogleClientID = os.Getenv("GOOGLE_HEALTH_CLIENT_ID")
	cfg.GoogleClientSecret = os.Getenv("GOOGLE_HEALTH_SECRET")
	cfg.LINEToken = os.Getenv("LINE_CHANNEL_ACCESS_TOKEN")
	cfg.LLM.APIKey = os.Getenv("LLM_API_KEY")

	if cfg.DatabaseURL == "" {
		return nil, errors.New("ต้องตั้งค่า FITBOT_DATABASE_URL ใน .env")
	}
	if _, err := time.LoadLocation(cfg.UserDefaults.Timezone); err != nil {
		return nil, fmt.Errorf("user_defaults.timezone ไม่ถูกต้อง: %w", err)
	}
	if cfg.Wear.MinRatio <= 0 || cfg.Wear.MinRatio > 1 {
		return nil, errors.New("wear.min_ratio ต้องอยู่ระหว่าง 0 ถึง 1")
	}
	return cfg, nil
}
