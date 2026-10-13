package auth

import (
	"testing"
	"time"

	"fitbot/internal/model"
)

// D8: valid → expiring เมื่อเหลือ ≤ 24 ชม. → invalid เมื่อหมดอายุ
func TestStatusByTime(t *testing.T) {
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	exp := func(h int) model.Credential {
		e := now.Add(time.Duration(h) * time.Hour)
		return model.Credential{Status: model.CredValid, RefreshExpiresAt: &e}
	}
	cases := map[int]string{48: model.CredValid, 24: model.CredExpiring, 1: model.CredExpiring, 0: model.CredInvalid, -5: model.CredInvalid}
	for h, want := range cases {
		if got := statusByTime(exp(h), now, 24*time.Hour); got != want {
			t.Errorf("เหลือ %d ชม.: got %s want %s", h, got, want)
		}
	}
	nl := model.Credential{Status: model.CredNotLinked}
	if statusByTime(nl, now, 24*time.Hour) != model.CredNotLinked {
		t.Error("not_linked ต้องคงเดิม")
	}
}
