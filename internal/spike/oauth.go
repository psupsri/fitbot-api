// Package spike คือเครื่องมือของ M0 (F0 API Spike): เชื่อม OAuth, เรียกทุก endpoint ที่เกี่ยวข้อง
// บันทึก JSON ดิบเป็น fixture และสรุปว่าตัวชี้วัดใดดึงได้จริง
//
// token ของ spike เก็บในไฟล์ .spike/token.json (สิทธิ์ 0600, อยู่ใน .gitignore) เป็นการชั่วคราว
// ระบบจริงจะเก็บแบบเข้ารหัสในฐานข้อมูลตาม D7 ใน F3
package spike

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"fitbot/internal/oauthflow"
)

const TokenFile = ".spike/token.json"

func OAuthConfig(clientID, secret string, port int, scopes []string) *oauth2.Config {
	return oauthflow.Config(clientID, secret, port, scopes)
}

// SavedToken คือ token ที่บันทึกพร้อมเวลาที่ได้รับ (ใช้ประเมินวันหมดอายุของ refresh token ในโหมด Testing)
type SavedToken struct {
	Token      *oauth2.Token `json:"token"`
	ObtainedAt time.Time     `json:"obtained_at"`
}

// Authorize เปิดเบราว์เซอร์ให้ยินยอมแล้วแลก code เป็น token (ใช้ร่วมกับ fitbot auth)
func Authorize(ctx context.Context, oc *oauth2.Config, port int, printURL func(string)) (*oauth2.Token, error) {
	return oauthflow.Authorize(ctx, oc, port, printURL)
}

func SaveToken(path string, st SavedToken) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func LoadToken(path string) (*SavedToken, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("ยังไม่มี token ให้รัน `spike auth` ก่อน")
	}
	if err != nil {
		return nil, err
	}
	var st SavedToken
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// HTTPClient คืน client ที่ refresh token อัตโนมัติและบันทึก token ใหม่ลงไฟล์
func HTTPClient(ctx context.Context, oc *oauth2.Config, path string) (*http.Client, *SavedToken, error) {
	st, err := LoadToken(path)
	if err != nil {
		return nil, nil, err
	}
	ts := &savingSource{base: oc.TokenSource(ctx, st.Token), path: path, saved: st}
	return oauth2.NewClient(ctx, ts), st, nil
}

type savingSource struct {
	base  oauth2.TokenSource
	path  string
	saved *SavedToken
	mu    sync.Mutex
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, err := s.base.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			return nil, fmt.Errorf("refresh token หมดอายุหรือถูกเพิกถอน ให้รัน `spike auth` ใหม่: %w", err)
		}
		return nil, err
	}
	if tok.AccessToken != s.saved.Token.AccessToken {
		if tok.RefreshToken == "" {
			tok.RefreshToken = s.saved.Token.RefreshToken
		}
		s.saved.Token = tok
		_ = SaveToken(s.path, *s.saved)
	}
	return tok, nil
}
