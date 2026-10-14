# fitbot-api

โค้ชสุขภาพ AI ที่อ่านข้อมูล Fitbit Air ของเมื่อวานผ่าน Google Health API เทียบกับค่าเฉลี่ยของผู้ใช้เอง แล้วส่งสรุปและคำแนะนำภาษาไทยทาง LINE ทุกเช้า

- Go, binary ไฟล์เดียว (Modular Monolith) รันบน macOS
- PostgreSQL เก็บข้อมูลรายวัน, token เข้ารหัส AES-256-GCM
- ตัวเลขทุกตัวคำนวณด้วยโค้ด ส่วนสรุปและคำแนะนำมาจาก LLM บนเครื่อง (Ollama) ผ่าน adapter แบบ OpenAI-compatible พร้อม guardrail และ fallback
- launchd รันเองทุกวัน ทำงานตามสถานะ รันซ้ำได้ไม่ส่งซ้ำ

## เริ่มต้น

```bash
brew services start postgresql@18 && createdb fitbot
cp .env.example .env                 # ใส่ค่าลับ (ดูคำอธิบายในไฟล์)
go build -o bin/fitbot ./cmd/fitbot
./bin/fitbot keygen                  # ใส่ผลลัพธ์ใน .env
./bin/fitbot migrate up
./bin/fitbot user add --name "ชื่อ" --line-user-id U...
./bin/fitbot auth --user 1
./bin/fitbot run --dry-run           # ดูรายงานโดยไม่ส่ง
./bin/fitbot schedule install        # ทำงานอัตโนมัติ
./bin/fitbot serve                   # API ให้ dashboard (fitbot-web) ฟังเฉพาะ 127.0.0.1:8787
```

`go test ./...` (integration test ใช้ฐานข้อมูล `fitbot_test` บนเครื่อง ถ้าไม่มีจะข้าม)

## เอกสาร

| เอกสาร | เนื้อหา |
| --- | --- |
| [docs/M0-api-spike.md](docs/M0-api-spike.md) | ผลสำรวจ Google Health API, mapping ตัวชี้วัด, Database Design |
| [docs/M1-runbook.md](docs/M1-runbook.md) | ติดตั้งและใช้งาน |
| [docs/M2-report.md](docs/M2-report.md) | รายงาน, กฎ 5 ข้อ, Metrics และ Anomaly rules |
| [docs/M3-ai-coaching.md](docs/M3-ai-coaching.md) | AI coaching, guardrail, ผลประเมิน |
| [docs/automation.md](docs/automation.md) | การทำงานอัตโนมัติด้วย launchd |
| [docs/dashboard-api.md](docs/dashboard-api.md) | HTTP API แบบอ่านอย่างเดียวสำหรับ dashboard |

> คำแนะนำจาก Fitbot มาจาก AI และระบบอัตโนมัติ ไม่ใช่คำวินิจฉัยของแพทย์
