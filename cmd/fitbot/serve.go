package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"fitbot/internal/anomaly"
	"fitbot/internal/httpapi"
)

// serve: HTTP API ให้ fitbot-web (อ่านข้อมูล และสั่งส่งรายงานตามกฎเดิม) (ระยะ 1 ไม่มีล็อกอิน จึงยอมฟังเฉพาะ loopback)
func (a *app) serve(ctx context.Context, args []string) error {
	fs := flags("serve")
	addr := fs.String("addr", "127.0.0.1:8787", "ที่อยู่ที่ฟัง (เฉพาะ 127.0.0.1 หรือ localhost)")
	web := fs.String("web", "", "โฟลเดอร์หน้าเว็บที่ build แล้ว เช่น ../fitbot-web/dist (ว่าง = API อย่างเดียว)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return fmt.Errorf("--addr ไม่ถูกต้อง: %w", err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("ยังไม่มีระบบล็อกอิน จึงฟังได้เฉพาะ 127.0.0.1 หรือ localhost เพื่อไม่ให้ข้อมูลสุขภาพเปิดออกนอกเครื่อง")
	}
	rules, err := anomaly.LoadRules(a.cfg.Anomaly.RulesFile)
	if err != nil {
		return err
	}
	// ปุ่มส่งรายงานใช้ Orchestrator ตัวเดียวกับ fitbot run (ส่ง LINE จริง ใช้ AI ถ้าเปิดไว้)
	o, err := a.orchestrator(false)
	if err != nil {
		return err
	}
	api := &httpapi.Server{Store: a.store, Cfg: a.cfg, Rules: rules, Runner: o, Log: a.log, Now: time.Now}
	mux := http.NewServeMux()
	mux.Handle("/api/", api.Handler())
	if *web != "" {
		mux.Handle("/", http.FileServer(http.Dir(*web)))
	}
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Printf("fitbot serve: http://%s (กด Ctrl+C เพื่อหยุด)\n", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
