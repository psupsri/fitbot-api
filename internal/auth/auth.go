// Package auth จัดการสิทธิ์ Google Health ต่อผู้ใช้ (F3): เชื่อมต่อผ่านเบราว์เซอร์ เก็บ token แบบเข้ารหัส (D7)
// refresh access token อัตโนมัติ และติดตามวันหมดอายุของ refresh token (โหมด Testing หมดอายุทุก 7 วัน, D8)
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"fitbot/internal/config"
	"fitbot/internal/googlehealth"
	"fitbot/internal/model"
	"fitbot/internal/oauthflow"
	"fitbot/internal/secure"
	"fitbot/internal/store"
)

type Manager struct {
	cfg   *config.Config
	store *store.Store
	box   *secure.Box
	now   func() time.Time
}

func NewManager(cfg *config.Config, st *store.Store, box *secure.Box, now func() time.Time) *Manager {
	return &Manager{cfg: cfg, store: st, box: box, now: now}
}

func (m *Manager) oauthConfig() *oauth2.Config {
	return oauthflow.Config(m.cfg.GoogleClientID, m.cfg.GoogleClientSecret, m.cfg.Google.RedirectPort, googlehealth.Scopes)
}

// AuthorizeResult คือผลการเชื่อมต่อ
type AuthorizeResult struct {
	Linked           bool   // false = ACCOUNT_NOT_LINKED
	LinkURL          string // ลิงก์ให้ผู้ใช้ไปเชื่อมบัญชี (เมื่อ Linked = false)
	RefreshExpiresAt time.Time
}

// Authorize เปิดเบราว์เซอร์ให้ยินยอม ตรวจ identity ทันทีตามคำแนะนำของ Google แล้วบันทึก token แบบเข้ารหัส
// ถ้าบัญชียังไม่เชื่อมกับ Google Health จะบันทึกสถานะ not_linked และไม่ถือว่าเชื่อมต่อแล้ว
func (m *Manager) Authorize(ctx context.Context, userID int64, printURL func(string)) (*AuthorizeResult, error) {
	if m.cfg.GoogleClientID == "" || m.cfg.GoogleClientSecret == "" {
		return nil, errors.New("ต้องตั้งค่า GOOGLE_HEALTH_CLIENT_ID และ GOOGLE_HEALTH_SECRET ใน .env")
	}
	oc := m.oauthConfig()
	tok, err := oauthflow.Authorize(ctx, oc, m.cfg.Google.RedirectPort, printURL)
	if err != nil {
		return nil, err
	}

	res := &AuthorizeResult{Linked: true, RefreshExpiresAt: m.now().Add(m.cfg.Google.RefreshTokenTTL())}
	gh := m.client(oc.Client(ctx, tok))
	healthID, err := gh.Identity(ctx)
	var ae *googlehealth.APIError
	switch {
	case errors.As(err, &ae) && errors.Is(ae, googlehealth.ErrAccountNotLinked):
		res.Linked, res.LinkURL = false, ae.RedirectURI
	case err != nil:
		return nil, fmt.Errorf("ตรวจ identity: %w", err)
	}

	refEnc, err := m.box.Encrypt(tok.RefreshToken)
	if err != nil {
		return nil, err
	}
	accEnc, err := m.box.Encrypt(tok.AccessToken)
	if err != nil {
		return nil, err
	}
	scopes := googlehealth.Scopes
	if s, ok := tok.Extra("scope").(string); ok && s != "" {
		scopes = strings.Fields(s)
	}
	cred := model.Credential{UserID: userID, RefreshTokenEnc: refEnc, AccessTokenEnc: accEnc, AccessExpiresAt: &tok.Expiry,
		RefreshExpiresAt: &res.RefreshExpiresAt, Scopes: scopes, Status: model.CredValid}
	if healthID != "" {
		cred.HealthUserID = &healthID
	}
	if !res.Linked {
		cred.Status = model.CredNotLinked
	}
	return res, m.store.SaveNewCredential(ctx, cred)
}

func (m *Manager) client(h *http.Client) *googlehealth.Client {
	c := googlehealth.New(h)
	c.BaseURL, c.MaxRetries = m.cfg.Google.BaseURL, m.cfg.Google.MaxRetries
	return c
}

// Client คืน Google Health client ของผู้ใช้ที่ refresh token อัตโนมัติและบันทึก access token ใหม่
func (m *Manager) Client(ctx context.Context, userID int64) (*googlehealth.Client, error) {
	c, err := m.store.GetCredential(ctx, userID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && c.RefreshTokenEnc == nil) {
		return nil, fmt.Errorf("%w: ผู้ใช้ %d ยังไม่เคยเชื่อมต่อ ให้รัน `fitbot auth --user %d`", googlehealth.ErrUnauthorized, userID, userID)
	}
	if err != nil {
		return nil, err
	}
	if c.Status == model.CredInvalid {
		return nil, fmt.Errorf("%w: ให้รัน `fitbot auth --user %d`", googlehealth.ErrUnauthorized, userID)
	}
	refresh, err := m.box.Decrypt(c.RefreshTokenEnc)
	if err != nil {
		return nil, err
	}
	tok := &oauth2.Token{RefreshToken: refresh, TokenType: "Bearer"}
	if c.AccessTokenEnc != nil && c.AccessExpiresAt != nil {
		if acc, err := m.box.Decrypt(c.AccessTokenEnc); err == nil {
			tok.AccessToken, tok.Expiry = acc, *c.AccessExpiresAt
		}
	}
	ts := &persistingSource{base: m.oauthConfig().TokenSource(ctx, tok), last: tok.AccessToken, refresh: refresh, m: m, userID: userID}
	return m.client(oauth2.NewClient(ctx, ts)), nil
}

type persistingSource struct {
	base    oauth2.TokenSource
	last    string
	refresh string
	m       *Manager
	userID  int64
	mu      sync.Mutex
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tok, err := p.base.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			_ = p.m.store.SetCredentialStatus(context.Background(), p.userID, model.CredInvalid)
			return nil, fmt.Errorf("%w: refresh token หมดอายุหรือถูกเพิกถอน", googlehealth.ErrUnauthorized)
		}
		return nil, err
	}
	if tok.AccessToken != p.last {
		p.last = tok.AccessToken
		if accEnc, err := p.m.box.Encrypt(tok.AccessToken); err == nil {
			var refEnc []byte
			if tok.RefreshToken != "" && tok.RefreshToken != p.refresh {
				refEnc, _ = p.m.box.Encrypt(tok.RefreshToken)
			}
			_ = p.m.store.UpdateAccessToken(context.Background(), p.userID, accEnc, tok.Expiry, refEnc)
		}
	}
	return tok, nil
}

// TokenState คือผลการตรวจสถานะ token
type TokenState struct {
	Status       string
	ExpiresAt    *time.Time
	ShouldWarn   bool // เหลือ ≤ warn_before_hours และยังไม่เคยเตือนตั้งแต่ re-auth ล่าสุด
	ShouldNotify bool // ใช้ไม่ได้ และยังไม่เคยแจ้งตั้งแต่ re-auth ล่าสุด
}

// Check ตรวจสถานะ token และปรับเป็น expiring/invalid ตามเวลา (D8)
func (m *Manager) Check(ctx context.Context, userID int64) (TokenState, error) {
	c, err := m.store.GetCredential(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return TokenState{Status: model.CredNotLinked}, nil
	}
	if err != nil {
		return TokenState{}, err
	}
	st := TokenState{Status: c.Status, ExpiresAt: c.RefreshExpiresAt}
	if next := statusByTime(c, m.now(), m.cfg.Google.WarnBefore()); next != c.Status {
		if err := m.store.SetCredentialStatus(ctx, userID, next); err != nil {
			return st, err
		}
		st.Status = next
	}
	st.ShouldWarn = st.Status == model.CredExpiring && c.ExpiryWarnedAt == nil
	st.ShouldNotify = (st.Status == model.CredInvalid || st.Status == model.CredNotLinked) && c.RefreshTokenEnc != nil && c.InvalidNotifiedAt == nil
	return st, nil
}

// statusByTime คำนวณสถานะจากเวลาหมดอายุของ refresh token
func statusByTime(c model.Credential, now time.Time, warnBefore time.Duration) string {
	if c.RefreshExpiresAt == nil || (c.Status != model.CredValid && c.Status != model.CredExpiring) {
		return c.Status
	}
	switch left := c.RefreshExpiresAt.Sub(now); {
	case left <= 0:
		return model.CredInvalid
	case left <= warnBefore:
		return model.CredExpiring
	}
	return c.Status
}

// MarkLinked คืนสถานะเป็น valid หลังเรียก API สำเร็จ (เช่น หลังผู้ใช้แก้ ACCOUNT_NOT_LINKED)
func (m *Manager) MarkLinked(ctx context.Context, userID int64) error {
	c, err := m.store.GetCredential(ctx, userID)
	if err != nil || c.Status != model.CredNotLinked {
		return err
	}
	return m.store.SetCredentialStatus(ctx, userID, model.CredValid)
}

// MarkFailure บันทึกสถานะเมื่อ API ตอบว่า token ใช้ไม่ได้หรือบัญชียังไม่เชื่อม
func (m *Manager) MarkFailure(ctx context.Context, userID int64, err error) error {
	switch {
	case errors.Is(err, googlehealth.ErrAccountNotLinked):
		return m.store.SetCredentialStatus(ctx, userID, model.CredNotLinked)
	case errors.Is(err, googlehealth.ErrUnauthorized):
		return m.store.SetCredentialStatus(ctx, userID, model.CredInvalid)
	}
	return nil
}
