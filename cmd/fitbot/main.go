// fitbot — โค้ชสุขภาพ AI จากข้อมูล Fitbit Air (M1: เก็บข้อมูลจริงรายวัน)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"text/tabwriter"
	"time"

	"fitbot/internal/auth"
	"fitbot/internal/collector"
	"fitbot/internal/config"
	"fitbot/internal/dateutil"
	"fitbot/internal/db"
	"fitbot/internal/line"
	"fitbot/internal/model"
	"fitbot/internal/secure"
	"fitbot/internal/store"
)

const usage = `fitbot — โค้ชสุขภาพ AI (MVP สั่งรันด้วยมือ)

  fitbot [--config config.yaml] <คำสั่ง> [ตัวเลือก]

รายงานประจำวัน (M2)
  run [--date YYYY-MM-DD] [--user ID] [--dry-run]
                     ดึงข้อมูล ตรวจความครบ แล้วส่งรายงานหรือข้อความให้ sync ตามกฎ 5 ข้อ
                     รันซ้ำได้ ไม่ส่งซ้ำ จะบอกเวลาที่ควรรันใหม่เมื่อยังไม่ถึง 2 ชม.
  report preview --user ID [--date YYYY-MM-DD]
                     แสดงรายงานจากข้อมูลในฐานข้อมูล ไม่ส่ง ไม่เปลี่ยนสถานะ
  dev seed --user ID [--days N]
                     สร้างข้อมูลจำลอง (เฉพาะฐานข้อมูลที่ชื่อลงท้าย _dev)

ทำงานอัตโนมัติ (macOS launchd)
  schedule install [--mode run|collect]   รันเองทุกวัน 08:00–20:00 ทุก 2 ชม. และตอน login (ค่าเริ่มต้น: run)
  schedule status | schedule run-now | schedule uninstall

เก็บข้อมูลอย่างเดียว
  collect [--date YYYY-MM-DD] [--user ID] [--dry-run]
                     ดึงและบันทึกข้อมูลวัน D (ค่าเริ่มต้น: เมื่อวาน ทุกผู้ใช้) ดึงซ้ำได้ เขียนทับไม่ซ้ำ
                     --dry-run: แสดงข้อความเตือนทาง LINE แทนการส่งจริง
  show --user ID [--date YYYY-MM-DD]
                     แสดงข้อมูลที่บันทึกในฐานข้อมูล ใช้เทียบกับแอป

ผู้ใช้ (F2)
  user add --name NAME [--line-user-id U...] [--timezone TZ] [--sleep-start HH:MM]
           [--sleep-end HH:MM] [--resync-gap MIN] [--age N]
  user update --id ID [ตัวเลือกเดียวกับ add]
  user list | user show --id ID | user disable --id ID | user enable --id ID

Google Health (F3)
  auth --user ID     เชื่อมต่อหรือเชื่อมต่อใหม่ (เปิดเบราว์เซอร์)
  auth status        สถานะ token ของทุกผู้ใช้

LINE (F11)
  line test --user ID [--message TEXT] [--dry-run]

ระบบ (F1)
  migrate up | migrate down [--steps N]
  keygen             สร้าง FITBOT_ENCRYPTION_KEY
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ผิดพลาด:", err)
		os.Exit(1)
	}
}

type app struct {
	cfg   *config.Config
	log   *slog.Logger
	store *store.Store
}

func run(ctx context.Context, args []string) error {
	cfgPath := "config.yaml"
	if len(args) >= 2 && args[0] == "--config" {
		cfgPath, args = args[1], args[2:]
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	if cmd == "keygen" {
		k, err := secure.GenerateKey()
		if err != nil {
			return err
		}
		fmt.Println("FITBOT_ENCRYPTION_KEY=" + k)
		fmt.Fprintln(os.Stderr, "เก็บไว้ใน .env (นอกฐานข้อมูล) ถ้ากุญแจหายต้อง re-auth ทุกผู้ใช้")
		return nil
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	a := &app{cfg: cfg, log: slog.New(slog.NewTextHandler(os.Stderr, nil)), store: store.New(pool)}

	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	switch cmd {
	case "migrate":
		return a.migrate(ctx, sub, rest[min(1, len(rest)):])
	case "user":
		return a.user(ctx, sub, rest[min(1, len(rest)):])
	case "auth":
		if sub == "status" {
			return a.authStatus(ctx)
		}
		return a.auth(ctx, rest)
	case "collect":
		return a.collect(ctx, rest)
	case "show":
		return a.show(ctx, rest)
	case "run":
		return a.runDaily(ctx, rest)
	case "report":
		if sub != "preview" {
			return errors.New("ใช้ fitbot report preview --user ID")
		}
		return a.reportPreview(ctx, rest[1:])
	case "dev":
		if sub != "seed" {
			return errors.New("ใช้ fitbot dev seed --user ID")
		}
		return a.devSeed(ctx, rest[1:])
	case "schedule":
		return a.schedule(ctx, sub, rest[min(1, len(rest)):])
	case "line":
		if sub != "test" {
			return errors.New("ใช้ fitbot line test --user ID")
		}
		return a.lineTest(ctx, rest[1:])
	}
	return fmt.Errorf("ไม่รู้จักคำสั่ง %q (ดู fitbot help)", cmd)
}

func flags(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }

func (a *app) authManager() (*auth.Manager, error) {
	box, err := secure.New(a.cfg.EncryptionKey)
	if err != nil {
		return nil, err
	}
	return auth.NewManager(a.cfg, a.store, box, time.Now), nil
}

func (a *app) notifier(dryRun bool) line.Notifier {
	if dryRun {
		return line.DryRun{Out: os.Stdout}
	}
	return line.New(a.cfg.LINEToken, a.cfg.LINE.BaseURL)
}

// ---------- migrate ----------

func (a *app) migrate(ctx context.Context, sub string, args []string) error {
	fs := flags("migrate")
	steps := fs.Int("steps", 1, "จำนวนขั้นที่ย้อน")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var done []string
	var err error
	switch sub {
	case "up":
		done, err = db.Up(ctx, a.store.Pool())
	case "down":
		done, err = db.Down(ctx, a.store.Pool(), *steps)
	default:
		return errors.New("ใช้ migrate up หรือ migrate down")
	}
	for _, d := range done {
		fmt.Printf("migrate %s: %s\n", sub, d)
	}
	if err == nil && len(done) == 0 {
		fmt.Println("ไม่มี migration ที่ต้องทำ")
	}
	return err
}

// ---------- user (F2) ----------

func (a *app) user(ctx context.Context, sub string, args []string) error {
	fs := flags("user " + sub)
	id := fs.Int64("id", 0, "user id")
	name := fs.String("name", "", "ชื่อที่แสดง")
	lineID := fs.String("line-user-id", "", "LINE user ID")
	tz := fs.String("timezone", "", "เช่น Asia/Bangkok")
	sleepStart := fs.String("sleep-start", "", "เริ่มช่วงคืน HH:MM")
	sleepEnd := fs.String("sleep-end", "", "สิ้นสุดช่วงคืน HH:MM (วันถัดไป)")
	gap := fs.Int("resync-gap", 0, "นาทีที่รอหลังแจ้ง sync")
	age := fs.Int("age", 0, "อายุ (ถ้า Google Health ไม่มี)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	apply := func(u *model.User) error {
		if set["name"] {
			u.DisplayName = *name
		}
		if set["line-user-id"] {
			u.LineUserID = nil
			if *lineID != "" {
				u.LineUserID = lineID
			}
		}
		if set["timezone"] {
			if _, err := time.LoadLocation(*tz); err != nil {
				return fmt.Errorf("timezone ไม่ถูกต้อง: %w", err)
			}
			u.Timezone = *tz
		}
		var err error
		if set["sleep-start"] {
			if u.SleepWindowStart, err = dateutil.ParseClock(*sleepStart); err != nil {
				return err
			}
		}
		if set["sleep-end"] {
			if u.SleepWindowEnd, err = dateutil.ParseClock(*sleepEnd); err != nil {
				return err
			}
		}
		if set["resync-gap"] {
			if *gap <= 0 {
				return errors.New("resync-gap ต้องมากกว่า 0")
			}
			u.ResyncGapMinutes = *gap
		}
		if set["age"] {
			u.Age = age
		}
		return nil
	}
	needID := func() (model.User, error) {
		if *id == 0 {
			return model.User{}, errors.New("ต้องระบุ --id")
		}
		return a.store.GetUser(ctx, *id)
	}

	switch sub {
	case "add":
		if *name == "" {
			return errors.New("ต้องระบุ --name")
		}
		d := a.cfg.UserDefaults
		start, err := dateutil.ParseClock(d.SleepWindowStart)
		if err != nil {
			return err
		}
		end, err := dateutil.ParseClock(d.SleepWindowEnd)
		if err != nil {
			return err
		}
		u := model.User{Timezone: d.Timezone, Status: model.UserActive, SleepWindowStart: start, SleepWindowEnd: end, ResyncGapMinutes: d.ResyncGapMinutes}
		if err := apply(&u); err != nil {
			return err
		}
		if u, err = a.store.CreateUser(ctx, u); err != nil {
			return err
		}
		fmt.Printf("เพิ่มผู้ใช้ id=%d แล้ว\n", u.ID)
		printUser(u)
		return nil
	case "update", "disable", "enable":
		u, err := needID()
		if err != nil {
			return err
		}
		if err := apply(&u); err != nil {
			return err
		}
		switch sub {
		case "disable":
			u.Status = model.UserDisabled
		case "enable":
			u.Status = model.UserActive
		}
		if u, err = a.store.UpdateUser(ctx, u); err != nil {
			return err
		}
		if set["age"] {
			if err := a.store.UpdateUserAge(ctx, u.ID, *age); err != nil {
				return err
			}
			u.Age = age
		}
		printUser(u)
		return nil
	case "show":
		u, err := needID()
		if err != nil {
			return err
		}
		printUser(u)
		return nil
	case "list":
		users, err := a.store.ListUsers(ctx, false)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tชื่อ\tสถานะ\tTimezone\tช่วงคืน\tรอ sync\tLINE")
		for _, u := range users {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s–%s\t%d นาที\t%s\n", u.ID, u.DisplayName, u.Status, u.Timezone,
				dateutil.FormatClock(u.SleepWindowStart), dateutil.FormatClock(u.SleepWindowEnd), u.ResyncGapMinutes, deref(u.LineUserID))
		}
		return tw.Flush()
	}
	return fmt.Errorf("ไม่รู้จักคำสั่ง user %q", sub)
}

func printUser(u model.User) {
	age := "ไม่มีข้อมูล (ใช้เกณฑ์ผู้ใหญ่ทั่วไป)"
	if u.Age != nil {
		age = fmt.Sprint(*u.Age)
	}
	fmt.Printf("  id: %d\n  ชื่อ: %s\n  สถานะ: %s\n  timezone: %s\n  อายุ: %s\n  LINE user ID: %s\n  ช่วงคืน: %s ถึง %s (วันถัดไป)\n  รอหลังแจ้ง sync: %d นาที\n",
		u.ID, u.DisplayName, u.Status, u.Timezone, age, deref(u.LineUserID),
		dateutil.FormatClock(u.SleepWindowStart), dateutil.FormatClock(u.SleepWindowEnd), u.ResyncGapMinutes)
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// ---------- auth (F3) ----------

func (a *app) auth(ctx context.Context, args []string) error {
	fs := flags("auth")
	id := fs.Int64("user", 0, "user id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == 0 {
		return errors.New("ต้องระบุ --user")
	}
	if _, err := a.store.GetUser(ctx, *id); err != nil {
		return fmt.Errorf("ผู้ใช้ %d: %w", *id, err)
	}
	am, err := a.authManager()
	if err != nil {
		return err
	}
	res, err := am.Authorize(ctx, *id, func(u string) {
		fmt.Println("กำลังเปิดเบราว์เซอร์ ถ้าไม่เปิดให้คัดลอกลิงก์นี้ไปเปิดเอง:\n" + u)
	})
	if err != nil {
		return err
	}
	if !res.Linked {
		return fmt.Errorf("บัญชี Google นี้ยังไม่เชื่อมกับ Google Health: เปิดแอปเพื่อสร้างหรือย้ายบัญชีก่อน (%s) แล้วรัน fitbot collect อีกครั้ง", res.LinkURL)
	}
	fmt.Printf("เชื่อมต่อสำเร็จ token จะหมดอายุประมาณ %s (โหมด Testing) ระบบจะเตือนทาง LINE ล่วงหน้า %d ชม.\n",
		res.RefreshExpiresAt.Local().Format("2006-01-02 15:04"), a.cfg.Google.WarnBeforeHours)
	return nil
}

func (a *app) authStatus(ctx context.Context) error {
	am, err := a.authManager()
	if err != nil {
		return err
	}
	users, err := a.store.ListUsers(ctx, false)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tชื่อ\tสถานะ token\tหมดอายุ")
	for _, u := range users {
		st, err := am.Check(ctx, u.ID)
		if err != nil {
			return err
		}
		exp := "-"
		if st.ExpiresAt != nil {
			exp = st.ExpiresAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", u.ID, u.DisplayName, st.Status, exp)
	}
	return tw.Flush()
}

// ---------- collect / show ----------

func parseDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	d, err := dateutil.Parse(s)
	return &d, err
}

func (a *app) collect(ctx context.Context, args []string) error {
	fs := flags("collect")
	id := fs.Int64("user", 0, "เฉพาะผู้ใช้นี้")
	date := fs.String("date", "", "วันที่ YYYY-MM-DD (ค่าเริ่มต้น: เมื่อวาน)")
	dryRun := fs.Bool("dry-run", false, "แสดงข้อความ LINE แทนการส่งจริง")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d, err := parseDate(*date)
	if err != nil {
		return err
	}
	am, err := a.authManager()
	if err != nil {
		return err
	}
	c := &collector.Collector{Store: a.store, Auth: am, Notifier: a.notifier(*dryRun), Cfg: a.cfg, Now: time.Now, Log: a.log, DryRun: *dryRun}
	results, err := c.Run(ctx, collector.Options{Date: d, UserID: *id})
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Println("ไม่มีผู้ใช้ที่เปิดใช้งาน (เพิ่มด้วย fitbot user add)")
		return nil
	}
	failed := 0
	for _, r := range results {
		if r.Error != "" {
			failed++
			fmt.Printf("✘ [%d] %s · %s · %s\n", r.UserID, r.Name, r.Date, r.Error)
			continue
		}
		fmt.Printf("✔ [%d] %s · %s · %s\n", r.UserID, r.Name, r.Date, r.Detail)
	}
	if failed > 0 {
		return fmt.Errorf("ผู้ใช้ %d จาก %d คนเก็บข้อมูลไม่สำเร็จ", failed, len(results))
	}
	return nil
}

func (a *app) show(ctx context.Context, args []string) error {
	fs := flags("show")
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
	act, night, err := a.store.GetDay(ctx, u.ID, d)
	if err != nil {
		return err
	}
	printDay(u, d, act, night)
	return nil
}

func printDay(u model.User, d time.Time, a *model.Activity, n *model.SleepNight) {
	loc := u.Location()
	i := func(p *int, unit string) string {
		if p == nil {
			return "ไม่มีข้อมูล"
		}
		return fmt.Sprintf("%d%s", *p, unit)
	}
	f := func(p *float64, format string) string {
		if p == nil {
			return "ไม่มีข้อมูล"
		}
		return fmt.Sprintf(format, *p)
	}
	t := func(p *time.Time) string {
		if p == nil {
			return "ไม่มีข้อมูล"
		}
		return p.In(loc).Format("2006-01-02 15:04")
	}
	fmt.Printf("ข้อมูลของ %s วันที่ %s\n\nกิจกรรม\n", u.DisplayName, dateutil.Format(d))
	if a == nil {
		fmt.Println("  ไม่มีข้อมูล")
	} else {
		var km *float64
		if a.DistanceM != nil {
			km = model.Ptr(*a.DistanceM / 1000)
		}
		wear := "ไม่มีข้อมูล"
		if a.WearMinutes != nil {
			wear = fmt.Sprintf("%d นาที (%.0f%%)", *a.WearMinutes, float64(*a.WearMinutes)*100/1440)
			if a.WearOK != nil && !*a.WearOK {
				wear += " ไม่ถึงเกณฑ์: กิจกรรมวันนี้จะไม่ถูกนำมาคิด (FR25)"
			}
		}
		fmt.Printf("  ก้าวเดิน            %s\n  ระยะทาง            %s\n  พลังงานทั้งวัน       %s\n  พลังงานจากกิจกรรม    %s\n",
			i(a.Steps, " ก้าว"), f(km, "%.2f กม."), f(a.EnergyKcal, "%.0f kcal"), f(a.ActiveEnergyKcal, "%.0f kcal"))
		fmt.Printf("  Active Zone Minutes %s (fat burn %s · cardio %s · peak %s)\n",
			i(a.ActiveZoneMin, " นาที"), i(a.AZMFatBurnMin, ""), i(a.AZMCardioMin, ""), i(a.AZMPeakMin, ""))
		fmt.Printf("  นาทีที่เคลื่อนไหว     เบา %s · ปานกลาง %s · หนัก %s\n", i(a.ActiveLightMin, ""), i(a.ActiveModerateMin, ""), i(a.ActiveVigorousMin, ""))
		fmt.Printf("  เวลานั่งนิ่ง          %s\n  ชีพจรขณะพัก         %s\n  เวลาสวมใส่          %s\n",
			i(a.SedentaryMin, " นาที"), f(a.RestingHR, "%.0f bpm"), wear)
	}
	fmt.Println("\nการนอนคืนนี้")
	if n == nil {
		fmt.Println("  ไม่มีข้อมูล")
		return
	}
	fmt.Printf("  เข้านอน             %s\n  ตื่น                 %s\n  หลับ                %s (ลึก %s · ตื้น %s · REM %s · ตื่นระหว่างคืน %s)\n  งีบ                 %s\n",
		t(n.Bedtime), t(n.WakeTime), i(n.AsleepMin, " นาที"), i(n.DeepMin, ""), i(n.LightMin, ""), i(n.RemMin, ""), i(n.AwakeMin, ""), i(n.NapMin, " นาที"))
	fmt.Printf("  HRV                 %s\n  SpO2                %s\n  อุณหภูมิผิวหนัง       %s (เทียบ baseline %s)\n  อัตราการหายใจ        %s\n",
		f(n.HRVms, "%.1f ms"), f(n.SpO2Avg, "%.1f%%"), f(n.SkinTempNightlyC, "%.2f °C"), f(n.SkinTempDeltaC, "%+.2f °C"), f(n.RespiratoryRate, "%.1f ครั้ง/นาที"))
}

// ---------- line (F11) ----------

func (a *app) lineTest(ctx context.Context, args []string) error {
	fs := flags("line test")
	id := fs.Int64("user", 0, "user id")
	msg := fs.String("message", "ทดสอบการส่งข้อความจาก Fitbot", "ข้อความ")
	dryRun := fs.Bool("dry-run", false, "แสดงแทนการส่งจริง")
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
	to := ""
	if u.LineUserID != nil {
		to = *u.LineUserID
	}
	if err := a.notifier(*dryRun).Push(ctx, to, []string{*msg}); err != nil {
		return err
	}
	fmt.Println("ส่งสำเร็จ")
	return nil
}
