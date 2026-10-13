// Package oauthflow คือขั้นตอน OAuth 2.0 + PKCE ผ่านเบราว์เซอร์ รับ callback ที่ localhost
package oauthflow

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Endpoint ของ Google OAuth
var Google = oauth2.Endpoint{
	AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
}

// Config สร้าง oauth2.Config ที่ redirect ไป http://localhost:{port}/callback
func Config(clientID, secret string, port int, scopes []string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: clientID, ClientSecret: secret, Endpoint: Google, Scopes: scopes,
		RedirectURL: fmt.Sprintf("http://localhost:%d/callback", port),
	}
}

// Authorize เปิดเบราว์เซอร์ให้ยินยอม รับ callback ที่ localhost แล้วแลก code เป็น token (มี PKCE)
func Authorize(ctx context.Context, oc *oauth2.Config, port int, printURL func(string)) (*oauth2.Token, error) {
	state, verifier := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	authURL := oc.AuthCodeURL(state, oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"), oauth2.S256ChallengeOption(verifier))

	ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return nil, fmt.Errorf("เปิดพอร์ต %d สำหรับรับ callback ไม่ได้: %w", port, err)
	}
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		res := result{code: q.Get("code")}
		switch {
		case q.Get("error") != "":
			res.err = fmt.Errorf("ไม่ได้รับอนุญาต: %s", q.Get("error"))
		case q.Get("state") != state:
			res.err = errors.New("state ไม่ตรงกัน")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			fmt.Fprintf(w, "<p>เชื่อมต่อไม่สำเร็จ: %s</p>", res.err)
		} else {
			fmt.Fprint(w, "<p>เชื่อมต่อ Fitbot spike สำเร็จ ปิดหน้านี้แล้วกลับไปที่ terminal ได้เลย</p>")
		}
		once.Do(func() { done <- res })
	})}
	go srv.Serve(ln)
	defer srv.Close()

	printURL(authURL)
	_ = exec.Command("open", authURL).Start() // macOS

	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Minute):
		return nil, errors.New("หมดเวลารอการยืนยันในเบราว์เซอร์")
	}
	if res.err != nil {
		return nil, res.err
	}
	tok, err := oc.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("แลก authorization code: %w", err)
	}
	if tok.RefreshToken == "" {
		return nil, errors.New("ไม่ได้รับ refresh token (ลองถอนสิทธิ์แอปใน Google Account แล้วรันใหม่)")
	}
	return tok, nil
}
