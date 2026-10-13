# M1 — เริ่มเก็บข้อมูลจริง: คู่มือรัน

Features ใน M1: F1 Foundation · F2 User CLI · F11 LINE Notifier · F3 OAuth และ Token · F4 Fetcher · F5 Sleep Attribution

## ติดตั้งครั้งแรก

```bash
brew services start postgresql@18        # PostgreSQL รับเฉพาะ localhost (RK11)
createdb fitbot
cd fitbot-api
go build -o bin/fitbot ./cmd/fitbot      # binary ไฟล์เดียว
./bin/fitbot keygen                       # ใส่ผลลัพธ์ใน .env พร้อม FITBOT_DATABASE_URL
./bin/fitbot migrate up
cp config.example.yaml config.yaml        # ไม่บังคับ ไม่มีไฟล์นี้จะใช้ค่าเริ่มต้นตาม Plan
```

## เพิ่มผู้ใช้และเชื่อมต่อ

```bash
./bin/fitbot user add --name "ชื่อ" --line-user-id U1234...    # timezone/ช่วงคืน/2 ชม. ใช้ค่าเริ่มต้น
./bin/fitbot auth --user 1               # เปิดเบราว์เซอร์ ยินยอม แล้วตรวจ ACCOUNT_NOT_LINKED
./bin/fitbot line test --user 1          # ทดสอบ LINE (หรือ --dry-run)
```

## ใช้งานทุกวัน (M1)

```bash
./bin/fitbot collect                     # เก็บข้อมูลเมื่อวานของทุกผู้ใช้ รันซ้ำได้ เขียนทับไม่ซ้ำ
./bin/fitbot show --user 1               # ดูสิ่งที่บันทึก เทียบกับแอป
./bin/fitbot collect --date 2026-10-12   # เก็บวันที่ระบุ (เช่น วันที่พลาด ถ้ายังอยู่ในข้อมูลของ API)
./bin/fitbot auth status                 # ดูวันหมดอายุ token
```

ข้อควรรู้
- **รันหลังเปิดแอปให้ sync แล้ว** ค่าที่วัดขณะนอนของคืนวัน D ถูกผูกกับวันที่ D+1 จะมีหลังตื่นและ sync
- **Token หมดอายุทุก 7 วัน** (โหมด Testing) ระบบเตือนทาง LINE เมื่อเหลือไม่เกิน 24 ชม. ให้รัน `fitbot auth --user 1` ใหม่
- **ดูใน DBeaver:** เชื่อม PostgreSQL `localhost:5432` ฐานข้อมูล `fitbot` ผู้ใช้ macOS ของคุณ ตาราง `daily_activity`, `sleep_nights`, `sleep_sessions`, `job_runs`
- ข้อมูลกิจกรรมของวันที่สวมไม่ถึง 50% ยังถูกเก็บ แต่ `wear_ok = false` จะไม่ถูกนำไปคิดใน M2 (FR25)

## ทำงานอัตโนมัติ

ดู [automation.md](automation.md) ติดตั้งแล้วไม่ต้องรันเองทุกเช้า

## รายงานทาง LINE (M2)

`fitbot run` ดึงข้อมูลแล้วส่งรายงานตามกฎ 5 ข้อ ดูรายละเอียดใน [M2-report.md](M2-report.md)

## Gate M1

ข้อมูลตรงกับแอป Google Health 3–5 วัน แล้วเริ่มรัน `collect` ทุกวัน
