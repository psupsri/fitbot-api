package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"fitbot/internal/anomaly"
	"fitbot/internal/collector"
	"fitbot/internal/completeness"
	"fitbot/internal/dateutil"
	"fitbot/internal/llm"
	"fitbot/internal/model"
	"fitbot/internal/orchestrator"
)

func (a *app) orchestrator(dryRun bool) (*orchestrator.Orchestrator, error) {
	am, err := a.authManager()
	if err != nil {
		return nil, err
	}
	rules, err := anomaly.LoadRules(a.cfg.Anomaly.RulesFile)
	if err != nil {
		return nil, err
	}
	n := a.notifier(dryRun)
	c := &collector.Collector{Store: a.store, Auth: am, Notifier: n, Cfg: a.cfg, Now: time.Now, Log: a.log, DryRun: dryRun}
	o := &orchestrator.Orchestrator{Store: a.store, Collector: c, Notifier: n, Cfg: a.cfg, Rules: rules, Now: time.Now, Log: a.log, DryRun: dryRun}
	if a.cfg.LLM.Enabled {
		o.LLM = llm.New(a.cfg.LLM)
	}
	return o, nil
}

// run (F12): ทำงานประจำวันตามกฎ 5 ข้อ
func (a *app) runDaily(ctx context.Context, args []string) error {
	fs := flags("run")
	id := fs.Int64("user", 0, "เฉพาะผู้ใช้นี้")
	date := fs.String("date", "", "วันที่ของรายงาน YYYY-MM-DD (ค่าเริ่มต้น: เมื่อวาน)")
	dryRun := fs.Bool("dry-run", false, "แสดงข้อความแทนการส่ง LINE และไม่บันทึกสถานะการส่ง")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d, err := parseDate(*date)
	if err != nil {
		return err
	}
	o, err := a.orchestrator(*dryRun)
	if err != nil {
		return err
	}
	fmt.Printf("=== fitbot run %s ===\n", time.Now().Format("2006-01-02 15:04:05")) // แยกแต่ละรอบใน log ของ launchd
	results, err := o.Run(ctx, orchestrator.Options{Date: d, UserID: *id})
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Println("ไม่มีผู้ใช้ที่เปิดใช้งาน")
		return nil
	}
	failed := 0
	for _, r := range results {
		if r.Error != "" {
			failed++
			fmt.Printf("✘ [%d] %s · %s · %s\n", r.UserID, r.Name, r.Date, r.Error)
			continue
		}
		fmt.Printf("✔ [%d] %s · %s · %s · %s\n", r.UserID, r.Name, r.Date, r.Action, r.Detail)
		if r.RetryAt != "" {
			fmt.Printf("    → ควรรันใหม่หลัง %s\n", r.RetryAt)
		}
	}
	if failed > 0 {
		return fmt.Errorf("ผู้ใช้ %d จาก %d คนประมวลผลไม่สำเร็จ", failed, len(results))
	}
	return nil
}

// report preview: แสดงรายงานจากข้อมูลในฐานข้อมูล ไม่ดึงใหม่ ไม่ส่ง ไม่เปลี่ยนสถานะ
func (a *app) reportPreview(ctx context.Context, args []string) error {
	fs := flags("report preview")
	id := fs.Int64("user", 0, "user id")
	date := fs.String("date", "", "วันที่ YYYY-MM-DD (ค่าเริ่มต้น: เมื่อวาน)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == 0 {
		return errors.New("ต้องระบุ --user")
	}
	u, err := a.store.GetUser(ctx, *id)
	if err != nil {
		return err
	}
	d := dateutil.Yesterday(time.Now(), u.Location())
	if p, err := parseDate(*date); err != nil {
		return err
	} else if p != nil {
		d = *p
	}
	o, err := a.orchestrator(true)
	if err != nil {
		return err
	}
	act, night, err := a.store.GetDay(ctx, u.ID, d)
	if err != nil {
		return err
	}
	st := completeness.Evaluate(act, night)
	start := time.Now()
	out, llmUsed, err := o.BuildReport(ctx, u, d, !st.Complete(), completeness.MissingParts(st))
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Round(100 * time.Millisecond)
	for i, m := range out.Messages {
		fmt.Printf("──── ข้อความ %d/%d (%d ตัวอักษร) ────\n%s\n\n", i+1, len(out.Messages), len([]rune(m)), m)
	}
	fmt.Printf("AI: %v · ใช้เวลา %s\n", llmUsed, elapsed)
	return nil
}

// dev seed: สร้างข้อมูลจำลองเพื่อดูรูปแบบรายงานที่มีข้อมูลหลายวัน ใช้ได้เฉพาะฐานข้อมูลที่ชื่อลงท้ายด้วย _dev
func (a *app) devSeed(ctx context.Context, args []string) error {
	fs := flags("dev seed")
	id := fs.Int64("user", 0, "user id")
	days := fs.Int("days", 45, "จำนวนวัน")
	end := fs.String("end", "", "วันสุดท้าย (ค่าเริ่มต้น: เมื่อวาน)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var dbName string
	if err := a.store.Pool().QueryRow(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		return err
	}
	if !strings.HasSuffix(dbName, "_dev") {
		return fmt.Errorf("dev seed ใช้ได้กับฐานข้อมูลที่ชื่อลงท้ายด้วย _dev เท่านั้น (ตอนนี้คือ %s) กันข้อมูลจำลองปนกับข้อมูลจริง", dbName)
	}
	if *id == 0 {
		return errors.New("ต้องระบุ --user")
	}
	u, err := a.store.GetUser(ctx, *id)
	if err != nil {
		return err
	}
	last := dateutil.Yesterday(time.Now(), u.Location())
	if p, err := parseDate(*end); err != nil {
		return err
	} else if p != nil {
		last = *p
	}
	rng := rand.New(rand.NewPCG(uint64(u.ID), 7))
	norm := func(m, sd float64) float64 { return m + rng.NormFloat64()*sd }
	ip := func(v float64) *int { return model.Ptr(int(v)) }
	fp := model.Ptr[float64]
	loc := u.Location()
	for i := *days - 1; i >= 0; i-- {
		d := last.AddDate(0, 0, -i)
		roll := rng.Float64()
		var act *model.Activity
		if roll > 0.05 { // 5% ของวันไม่ได้สวม
			wear := 1400.0
			if roll < 0.10 {
				wear = 500 // สวมไม่ถึงครึ่งวัน
			}
			steps := max(800, norm(7000, 2000))
			fat, cardio := max(0, norm(25, 12)), max(0, norm(12, 10))
			act = &model.Activity{Steps: ip(steps), DistanceM: fp(steps * 0.85), EnergyKcal: fp(norm(2150, 150)), ActiveEnergyKcal: fp(norm(380, 120)),
				ActiveZoneMin: ip(fat + cardio), AZMFatBurnMin: ip(fat), AZMCardioMin: ip(cardio), AZMPeakMin: ip(0),
				ActiveLightMin: ip(norm(90, 20)), ActiveModerateMin: ip(max(0, norm(10, 8))), ActiveVigorousMin: ip(max(0, norm(20, 15))),
				SedentaryMin: ip(norm(850, 60)), RestingHR: fp(norm(71, 2)), WearMinutes: ip(wear), WearOK: model.Ptr(wear >= 720)}
		}
		var night *model.SleepNight
		if rng.Float64() > 0.05 {
			bed := dateutil.At(d, 22*60+50, loc).Add(time.Duration(norm(0, 40)) * time.Minute)
			asleep := norm(420, 45)
			if i < 7 {
				asleep = norm(340, 20) // สัปดาห์ล่าสุดนอนน้อยลง เพื่อให้เห็นแนวโน้มและกฎ "ควรปรับปรุง"
			}
			wake := bed.Add(time.Duration(asleep+10) * time.Minute)
			night = &model.SleepNight{Date: d, Bedtime: &bed, WakeTime: &wake, AsleepMin: ip(asleep), DeepMin: ip(asleep * 0.16),
				LightMin: ip(asleep * 0.56), RemMin: ip(asleep * 0.28), AwakeMin: ip(10),
				NightMetrics: model.NightMetrics{HRVms: fp(norm(28, 2.5)), SkinTempDeltaC: fp(norm(0, 0.3)), RespiratoryRate: fp(norm(14.8, 0.5))}}
			if rng.Float64() < 0.3 {
				night.SpO2Avg = fp(norm(95.8, 0.6))
			}
		}
		if err := a.store.SaveDay(ctx, u.ID, d, act, nil, night); err != nil {
			return err
		}
	}
	fmt.Printf("สร้างข้อมูลจำลอง %d วันถึง %s ในฐานข้อมูล %s แล้ว ดูรายงานด้วย: fitbot report preview --user %d\n", *days, dateutil.Format(last), dbName, u.ID)
	return nil
}
