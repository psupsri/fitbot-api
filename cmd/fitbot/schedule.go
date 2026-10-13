package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"text/template"
	"time"
)

// การทำงานอัตโนมัติบน macOS ด้วย launchd (F13 ดึงจาก P2 มาทำตามที่ PO อนุมัติ)
// รันทุก 2 ชม. ตั้งแต่ 08:00 ถึง 20:00 และตอน login เพื่อให้วันที่เปิดเครื่องสายยังได้รายงานภายในวันนั้น
// ระบบทำงานตามสถานะ จึงถูกเรียกกี่ครั้งก็ไม่ส่งซ้ำ (D3)

const scheduleLabel = "com.fitbot.daily"

var scheduleHours = []int{8, 10, 12, 14, 16, 18, 20}

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Binary}}</string>
		<string>{{.Mode}}</string>
	</array>
	<key>WorkingDirectory</key>
	<string>{{.WorkDir}}</string>
	<key>StartCalendarInterval</key>
	<array>{{range .Hours}}
		<dict><key>Hour</key><integer>{{.}}</integer><key>Minute</key><integer>0</integer></dict>{{end}}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{.Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{.Log}}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
	</dict>
</dict>
</plist>
`))

type schedulePaths struct {
	Label, Binary, Mode, WorkDir, Log, Plist string
	Hours                                    []int
}

func paths(mode string) (schedulePaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return schedulePaths{}, err
	}
	bin, err := os.Executable()
	if err != nil {
		return schedulePaths{}, err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return schedulePaths{}, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return schedulePaths{}, err
	}
	return schedulePaths{
		Label: scheduleLabel, Binary: bin, Mode: mode, WorkDir: wd, Hours: scheduleHours,
		Log:   filepath.Join(home, "Library", "Logs", "fitbot.log"),
		Plist: filepath.Join(home, "Library", "LaunchAgents", scheduleLabel+".plist"),
	}, nil
}

func launchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	return string(out), err
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func (a *app) schedule(ctx context.Context, sub string, args []string) error {
	fs := flags("schedule " + sub)
	mode := fs.String("mode", "run", "run = ส่งรายงานทาง LINE, collect = เก็บข้อมูลอย่างเดียว")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch sub {
	case "install":
		return a.scheduleInstall(*mode)
	case "uninstall":
		p, err := paths("")
		if err != nil {
			return err
		}
		_, _ = launchctl("bootout", domain()+"/"+scheduleLabel)
		if err := os.Remove(p.Plist); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Println("ยกเลิกการทำงานอัตโนมัติแล้ว")
		return nil
	case "status":
		return a.scheduleStatus(ctx)
	case "run-now":
		if out, err := launchctl("kickstart", domain()+"/"+scheduleLabel); err != nil {
			return fmt.Errorf("สั่งรันไม่สำเร็จ (ติดตั้งแล้วหรือยัง?): %s", out)
		}
		fmt.Println("สั่งให้ launchd รันแล้ว ดูผลด้วย: fitbot schedule status")
		return nil
	}
	return errors.New("ใช้ fitbot schedule install|uninstall|status|run-now")
}

func (a *app) scheduleInstall(mode string) error {
	if mode != "run" && mode != "collect" {
		return errors.New("--mode ต้องเป็น run หรือ collect")
	}
	p, err := paths(mode)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(p.WorkDir, "go.mod")); err != nil {
		return errors.New("ให้รันคำสั่งนี้จากโฟลเดอร์ fitbot-api (ใช้เป็น working directory เพื่ออ่าน .env และ config.yaml)")
	}
	var buf bytes.Buffer
	if err := plistTmpl.Execute(&buf, p); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.Plist), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.Log), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p.Plist, buf.Bytes(), 0o644); err != nil {
		return err
	}
	_, _ = launchctl("bootout", domain()+"/"+scheduleLabel) // ติดตั้งซ้ำได้
	if out, err := launchctl("bootstrap", domain(), p.Plist); err != nil {
		return fmt.Errorf("ลงทะเบียนกับ launchd ไม่สำเร็จ: %s", out)
	}
	what := "เก็บข้อมูลและส่งรายงานทาง LINE"
	if mode == "collect" {
		what = "เก็บข้อมูลอย่างเดียว"
	}
	fmt.Printf("ติดตั้งแล้ว: fitbot %s (%s) ทุกวัน 08:00–20:00 ทุก 2 ชม. และตอน login\n", mode, what)
	fmt.Printf("  ไฟล์ตั้งเวลา: %s\n  log: %s\n  binary: %s\n", p.Plist, p.Log, p.Binary)
	fmt.Println("launchd จะรันทันทีหนึ่งครั้งตอนติดตั้ง (ไม่ส่งซ้ำถ้าส่งรายงานของเมื่อวานไปแล้ว)")
	return nil
}

var (
	reState = regexp.MustCompile(`(?m)^\s*state = (.+)$`)
	reExit  = regexp.MustCompile(`(?m)^\s*last exit code = (.+)$`)
	reRuns  = regexp.MustCompile(`(?m)^\s*runs = (\d+)$`)
)

func (a *app) scheduleStatus(ctx context.Context) error {
	p, err := paths("")
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Plist); err != nil {
		fmt.Println("ยังไม่ได้ติดตั้งการทำงานอัตโนมัติ (fitbot schedule install)")
	} else {
		out, err := launchctl("print", domain()+"/"+scheduleLabel)
		if err != nil {
			fmt.Println("มีไฟล์ตั้งเวลา แต่ยังไม่ได้ลงทะเบียนกับ launchd (รัน fitbot schedule install อีกครั้ง)")
		} else {
			get := func(re *regexp.Regexp) string {
				if m := re.FindStringSubmatch(out); m != nil {
					return m[1]
				}
				return "-"
			}
			fmt.Printf("การทำงานอัตโนมัติ: ติดตั้งแล้ว (08:00–20:00 ทุก 2 ชม. และตอน login)\n  สถานะ: %s · รันไปแล้ว %s ครั้ง · exit code ล่าสุด: %s\n  log: %s\n",
				get(reState), get(reRuns), get(reExit), p.Log)
		}
	}

	// บริการที่ต้องพร้อมตอนรันอัตโนมัติ
	if err := a.store.Pool().Ping(ctx); err == nil {
		fmt.Println("  PostgreSQL: พร้อม")
	}
	c := &http.Client{Timeout: 2 * time.Second}
	if resp, err := c.Get("http://localhost:11434/api/version"); err == nil {
		resp.Body.Close()
		fmt.Println("  Ollama: พร้อม")
	} else {
		fmt.Println("  Ollama: ไม่ทำงาน (รายงานจะมีแค่ตัวเลข) เปิดแอป Ollama และตั้งให้เปิดตอน login")
	}

	rows, err := a.store.Pool().Query(ctx, `SELECT started_at, status, coalesce(summary->'users'->0->>'action', ''),
		coalesce(summary->'users'->0->>'detail', summary->'users'->0->>'error', '') FROM job_runs
		WHERE summary->>'command' = 'run' AND coalesce(summary->>'dry_run', 'false') = 'false' ORDER BY id DESC LIMIT 5`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Println("\nการรันล่าสุด:")
	for rows.Next() {
		var at time.Time
		var status, action, detail string
		if err := rows.Scan(&at, &status, &action, &detail); err != nil {
			return err
		}
		fmt.Printf("  %s  %-15s %-14s %s\n", at.Local().Format("2006-01-02 15:04"), status, action, detail)
	}
	return rows.Err()
}
