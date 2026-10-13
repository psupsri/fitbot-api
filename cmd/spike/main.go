// spike — เครื่องมือของ M0 (F0 API Spike) ตรวจว่าดึงตัวชี้วัดจาก Google Health API ได้จริงด้วย Go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"fitbot/internal/googlehealth"
	"fitbot/internal/spike"
)

const usage = `spike — M0 / F0 API Spike ของ Fitbot

  spike auth                         เชื่อม Google Health (เปิดเบราว์เซอร์) แล้วตรวจ ACCOUNT_NOT_LINKED
  spike run [--date YYYY-MM-DD] [--out DIR] [--endpoints FILE] [--only a,b]
                                     เรียกทุก endpoint บันทึก JSON ดิบ + index.md (ค่าเริ่มต้น: เมื่อวาน)
  spike call --path /v4/... [--method GET|POST] [--body JSON]
                                     เรียก endpoint ใดก็ได้หนึ่งครั้ง พิมพ์ผลลงหน้าจอ
  spike endpoints [--dump FILE]      แสดง/ส่งออกรายการ endpoint เพื่อแก้ไขโดยไม่ต้อง build ใหม่

ตั้งค่าใน .env (ดู .env.example): GOOGLE_HEALTH_CLIENT_ID, GOOGLE_HEALTH_SECRET, SPIKE_REDIRECT_PORT, SPIKE_TIMEZONE
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ผิดพลาด:", err)
		os.Exit(1)
	}
}

type env struct {
	clientID, secret string
	port             int
	loc              *time.Location
}

func loadEnv() (*env, error) {
	// อ่าน .env ในโฟลเดอร์ปัจจุบันก่อน แล้วจึงโฟลเดอร์แม่ (ค่าที่ตั้งไว้แล้วไม่ถูกเขียนทับ)
	for _, p := range []string{".env", "../.env"} {
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
		}
	}
	e := &env{clientID: os.Getenv("GOOGLE_HEALTH_CLIENT_ID"), secret: os.Getenv("GOOGLE_HEALTH_SECRET"), port: 8765, loc: time.Local}
	if e.clientID == "" || e.secret == "" {
		return nil, errors.New("ต้องตั้งค่า GOOGLE_HEALTH_CLIENT_ID และ GOOGLE_HEALTH_SECRET ใน .env")
	}
	if p := os.Getenv("SPIKE_REDIRECT_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("SPIKE_REDIRECT_PORT ไม่ถูกต้อง: %w", err)
		}
		e.port = n
	}
	tz := os.Getenv("SPIKE_TIMEZONE")
	if tz == "" {
		tz = "Asia/Bangkok"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("SPIKE_TIMEZONE ไม่ถูกต้อง: %w", err)
	}
	e.loc = loc
	return e, nil
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	if cmd == "endpoints" {
		return cmdEndpoints(rest)
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	switch cmd {
	case "auth":
		return cmdAuth(ctx, e, rest)
	case "run":
		return cmdRun(ctx, e, rest)
	case "call":
		return cmdCall(ctx, e, rest)
	}
	return fmt.Errorf("ไม่รู้จักคำสั่ง %q", cmd)
}

func client(ctx context.Context, e *env) (*googlehealth.Client, *spike.SavedToken, error) {
	oc := spike.OAuthConfig(e.clientID, e.secret, e.port, googlehealth.Scopes)
	h, st, err := spike.HTTPClient(ctx, oc, spike.TokenFile)
	if err != nil {
		return nil, nil, err
	}
	return googlehealth.New(h), st, nil
}

func cmdAuth(ctx context.Context, e *env, _ []string) error {
	oc := spike.OAuthConfig(e.clientID, e.secret, e.port, googlehealth.Scopes)
	fmt.Println("redirect URI ที่ต้องลงทะเบียนใน Google Cloud:", oc.RedirectURL)
	tok, err := spike.Authorize(ctx, oc, e.port, func(u string) {
		fmt.Println("กำลังเปิดเบราว์เซอร์ ถ้าไม่เปิดให้คัดลอกลิงก์นี้ไปเปิดเอง:\n" + u)
	})
	if err != nil {
		return err
	}
	if s, ok := tok.Extra("scope").(string); ok {
		fmt.Println("scope ที่ได้รับ:\n  " + strings.ReplaceAll(s, " ", "\n  "))
	}
	if err := spike.SaveToken(spike.TokenFile, spike.SavedToken{Token: tok, ObtainedAt: time.Now()}); err != nil {
		return err
	}
	fmt.Println("บันทึก token ที่", spike.TokenFile)

	// ตามคำแนะนำของ Google: เรียก users.getIdentity ทันทีหลังแลก code เพื่อตรวจ ACCOUNT_NOT_LINKED
	c, _, err := client(ctx, e)
	if err != nil {
		return err
	}
	resp, err := c.Do(ctx, "GET", "/v4/users/me/identity", nil)
	var ae *googlehealth.APIError
	switch {
	case errors.As(err, &ae) && errors.Is(ae, googlehealth.ErrAccountNotLinked):
		return fmt.Errorf("บัญชีนี้ยังไม่เชื่อมกับ Google Health: เปิดแอป Google Health/Fitbit แล้วสร้างหรือย้ายบัญชีก่อน (%s)", ae.RedirectURI)
	case err != nil:
		return fmt.Errorf("เรียก identity ไม่สำเร็จ: %w", err)
	}
	fmt.Printf("เชื่อมต่อสำเร็จ identity:\n%s\n", resp.Body)
	fmt.Println("หมายเหตุ: ถ้า OAuth consent screen อยู่ในสถานะ Testing refresh token จะหมดอายุใน 7 วัน ประมาณ",
		time.Now().Add(7*24*time.Hour).In(e.loc).Format("2006-01-02 15:04"))
	return nil
}

func cmdRun(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	date := fs.String("date", "", "วันที่ D (ค่าเริ่มต้น: เมื่อวานตาม SPIKE_TIMEZONE)")
	out := fs.String("out", "fixtures/spike", "โฟลเดอร์ปลายทาง")
	epFile := fs.String("endpoints", "", "ไฟล์รายการ endpoint (ค่าเริ่มต้น: รายการที่ฝังใน binary)")
	only := fs.String("only", "", "เรียกเฉพาะชื่อเหล่านี้ คั่นด้วย ,")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now().In(e.loc)
	d := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, time.UTC)
	if *date != "" {
		var err error
		if d, err = time.Parse("2006-01-02", *date); err != nil {
			return fmt.Errorf("--date ต้องเป็น YYYY-MM-DD")
		}
	}
	eps, err := spike.LoadEndpoints(*epFile)
	if err != nil {
		return err
	}
	if *only != "" {
		want := map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			want[strings.TrimSpace(n)] = true
		}
		var f []spike.Endpoint
		for _, ep := range eps {
			if want[ep.Name] {
				f = append(f, ep)
			}
		}
		eps = f
	}
	c, _, err := client(ctx, e)
	if err != nil {
		return err
	}
	dir := filepath.Join(*out, d.Format("2006-01-02"))
	fmt.Printf("เรียก %d endpoint สำหรับวันที่ %s → %s\n\n", len(eps), d.Format("2006-01-02"), dir)
	results, err := spike.Run(ctx, c, eps, spike.DayVars(d), dir, e.loc)
	for _, r := range results {
		mark := "✔"
		detail := fmt.Sprintf("%d รายการ", r.Items)
		if r.Error != "" {
			mark, detail = "✘", r.Error
		} else if r.Items == 0 {
			mark = "○"
		}
		if r.Note != "" {
			detail += " · " + r.Note
		}
		fmt.Printf("%s %-42s %s\n", mark, r.Endpoint.Name, detail)
	}
	if err != nil {
		return err
	}
	if err := spike.WriteIndex(filepath.Join(dir, "index.md"), d, results); err != nil {
		return err
	}
	fmt.Printf("\n✔ = ได้ข้อมูล  ○ = สำเร็จแต่ไม่มีข้อมูล  ✘ = ผิดพลาด\nสรุปอยู่ที่ %s\n", filepath.Join(dir, "index.md"))
	return nil
}

func cmdCall(ctx context.Context, e *env, args []string) error {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	method := fs.String("method", "GET", "GET หรือ POST")
	path := fs.String("path", "", "เช่น /v4/users/me/profile")
	body := fs.String("body", "", "JSON body สำหรับ POST")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("ต้องระบุ --path")
	}
	c, _, err := client(ctx, e)
	if err != nil {
		return err
	}
	var b []byte
	if *body != "" {
		b = []byte(*body)
	}
	resp, err := c.Do(ctx, strings.ToUpper(*method), *path, b)
	if resp != nil {
		fmt.Printf("HTTP %d (%s)\n%s\n", resp.Status, resp.Duration.Round(time.Millisecond), resp.Body)
	}
	return err
}

func cmdEndpoints(args []string) error {
	fs := flag.NewFlagSet("endpoints", flag.ContinueOnError)
	dump := fs.String("dump", "", "เขียนรายการลงไฟล์เพื่อแก้ไข")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dump != "" {
		return os.WriteFile(*dump, spike.DefaultEndpointsJSON(), 0o644)
	}
	eps, err := spike.LoadEndpoints("")
	if err != nil {
		return err
	}
	for _, ep := range eps {
		fmt.Printf("%-42s %-4s %s\n    %s\n", ep.Name, ep.Method, ep.Path, ep.Checks)
	}
	return nil
}
