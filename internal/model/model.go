// Package model คือชนิดข้อมูลกลางที่ทุกโมดูลใช้ร่วมกัน
// ตัวชี้วัดทุกตัวเป็น pointer: nil = "ไม่มีข้อมูล" (FR4) ห้ามใช้ 0 แทน
package model

import (
	"encoding/json"
	"time"
)

const (
	UserActive   = "active"
	UserDisabled = "disabled"

	SleepMain = "main"
	SleepNap  = "nap"

	CredValid     = "valid"
	CredExpiring  = "expiring"
	CredInvalid   = "invalid"
	CredNotLinked = "not_linked"
)

type User struct {
	ID               int64
	DisplayName      string
	Timezone         string
	Age              *int
	AgeUpdatedAt     *time.Time
	LineUserID       *string
	Status           string
	SleepWindowStart int // นาทีนับจากเที่ยงคืน
	SleepWindowEnd   int
	ResyncGapMinutes int
	CreatedAt        time.Time
}

func (u User) Location() *time.Location {
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

type Credential struct {
	UserID            int64
	HealthUserID      *string
	RefreshTokenEnc   []byte
	AccessTokenEnc    []byte
	AccessExpiresAt   *time.Time
	RefreshExpiresAt  *time.Time
	Scopes            []string
	Status            string
	ExpiryWarnedAt    *time.Time
	InvalidNotifiedAt *time.Time
}

// Activity คือกิจกรรมของวัน D
type Activity struct {
	Steps             *int
	DistanceM         *float64
	EnergyKcal        *float64
	ActiveEnergyKcal  *float64
	ActiveZoneMin     *int
	AZMFatBurnMin     *int
	AZMCardioMin      *int
	AZMPeakMin        *int
	ActiveLightMin    *int
	ActiveModerateMin *int
	ActiveVigorousMin *int
	SedentaryMin      *int
	RestingHR         *float64
	WearMinutes       *int
	WearOK            *bool
	Raw               json.RawMessage
}

// HasData บอกว่ามีตัวชี้วัดกิจกรรมอย่างน้อยหนึ่งตัว (ไม่นับเวลาสวมใส่)
func (a *Activity) HasData() bool {
	return a != nil && (a.Steps != nil || a.DistanceM != nil || a.EnergyKcal != nil || a.ActiveEnergyKcal != nil ||
		a.ActiveZoneMin != nil || a.ActiveLightMin != nil || a.SedentaryMin != nil || a.RestingHR != nil)
}

type SleepSession struct {
	ExternalID     string
	Start, End     time.Time
	Kind           string
	AttributedDate time.Time
	AsleepMin      *int
	AwakeMin       *int
	DeepMin        *int
	LightMin       *int
	RemMin         *int
	APIMainSleep   *bool
	Raw            json.RawMessage
}

// Minutes คือนาทีที่หลับ ถ้า API ไม่ให้มาใช้ระยะเวลาทั้งช่วง
func (s SleepSession) Minutes() int {
	if s.AsleepMin != nil {
		return *s.AsleepMin
	}
	return int(s.End.Sub(s.Start).Minutes())
}

// NightMetrics คือค่าที่วัดขณะนอน (API ผูกกับวันที่ตื่น D+1 แต่เป็นของคืนวัน D)
type NightMetrics struct {
	HRVms            *float64
	SpO2Avg          *float64
	SkinTempNightlyC *float64
	SkinTempDeltaC   *float64
	RespiratoryRate  *float64
}

func (m NightMetrics) HasData() bool {
	return m.HRVms != nil || m.SpO2Avg != nil || m.SkinTempNightlyC != nil || m.SkinTempDeltaC != nil || m.RespiratoryRate != nil
}

type SleepNight struct {
	Date      time.Time
	Bedtime   *time.Time
	WakeTime  *time.Time
	AsleepMin *int
	DeepMin   *int
	LightMin  *int
	RemMin    *int
	AwakeMin  *int
	NapMin    *int
	NightMetrics
}

func (n *SleepNight) HasMainSleep() bool { return n != nil && n.Bedtime != nil }

func Ptr[T any](v T) *T { return &v }
