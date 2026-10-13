# M0 — F0 API Spike: รายงานเพื่อรีวิวที่ Gate

ฉบับวันที่ 13 ตุลาคม 2026 · สถานะ: **Product Owner ตอบข้อตัดสินใจของ Gate M0 ครบแล้ว** (ดูหัวข้อ "ผลการตัดสินที่ Gate M0") · ทดสอบกับบัญชีจริงของ Product Owner วันที่ 11–12 ต.ค. 2026 (Google Fitbit Air)

> ตัวเลขสุขภาพในเอกสารนี้เป็น**ตัวอย่างสมมติ**ที่มีรูปแบบเดียวกับข้อมูลจริง ข้อมูลจริงอยู่ใน `fixtures/` ซึ่งไม่เผยแพร่

## สรุปสั้น

| Acceptance Criteria ของ F0 | สถานะ |
| --- | --- |
| เรียกจาก Go ได้ | ✔ เครื่องมือ `spike` เรียกจริงได้ทุก endpoint (OAuth, ตามหน้า, retry, ตรวจ `ACCOUNT_NOT_LINKED`) |
| ตัวอย่างข้อมูลจริงหรือคำยืนยันว่าดึงไม่ได้ ครบทุกตัวชี้วัด | ✔ ตารางด้านล่าง fixture อยู่ใน `fixtures/spike/` และ `fixtures/spike-fix/` (ไม่ขึ้น GitHub) |
| รู้โครงสร้างช่วงการนอน | ✔ มี stages, summary และ `metadata.mainSleep` |
| รู้วิธีวัดเวลาที่สวมใส่ | ✔ วัดได้จากจำนวนนาทีที่มีค่าชีพจร (ทางเลือก A) รอ PO อนุมัติ |

ข้อค้นพบที่ต้องตัดสินที่ Gate:
1. **คะแนนการนอน, readiness, cardio load และจำนวนชั้นไม่มีใน API** (RK1 เกิดจริง)
2. **ค่าที่วัดขณะนอน (HRV, อุณหภูมิผิวหนัง, อัตราการหายใจ, SpO2) ของคืนวัน D ถูกผูกกับวันที่ D+1** (วันที่ตื่น) Fetcher ต้องอ่านวันที่ D+1 สำหรับรายงานวัน D

## ตัวอย่างค่าที่ได้ (ตัวเลขสมมติ) ที่ PO ใช้เทียบกับแอป

| ตัวชี้วัด | ค่าจาก API | เทียบกับแอป |
| --- | --- | --- |
| ก้าวเดิน | 7,350 ก้าว | ☐ |
| ระยะทาง | 6.12 กม. | ☐ |
| พลังงานทั้งวัน (`total-calories`) | 2,210 kcal | ☐ |
| พลังงานจากกิจกรรม (`active-energy-burned`) | 412 kcal | ☐ |
| Active Zone Minutes | Fat burn 28 · Cardio 20 · Peak 0 → **68** ถ้านับ cardio/peak เป็น 2 เท่าแบบ Fitbit (48 ถ้าไม่นับคูณ) | ☐ ตัวเลขใดตรงกับแอป |
| เวลานั่งนิ่ง | 812 นาที (13 ชม. 32 นาที) ไม่รวมเวลานอน | ☐ |
| ชีพจรขณะพัก | 64 bpm | ☐ |
| การนอนคืนวันที่ 12 | เข้านอน 23:10 ตื่น 06:40 · หลับ 442 นาที (7 ชม. 22 นาที) · ลึก 74 · ตื้น 250 · REM 118 · ตื่น 8 นาที | ☐ |
| HRV คืนวันที่ 12 | 31.2 ms (ผูกกับวันที่ 13) | ☐ |
| อุณหภูมิผิวหนังคืนวันที่ 12 | +0.15 °C จาก baseline (ผูกกับวันที่ 13) | ☐ |
| เวลาสวมใส่ (ตัวแทน) | มีค่าชีพจร 1,398 นาที = 97% ของวัน | — |

## Mapping ตัวชี้วัดกับ API (ยืนยันด้วยข้อมูลจริง)

ทุก endpoint อยู่ใต้ `https://health.googleapis.com/v4/users/me/dataTypes/{type}/dataPoints` ทุกตัวมาจาก `dataSource.device.displayName = Google Fitbit Air`

| ตัวชี้วัด | Data type · วิธีเรียก | Field จริง | วันที่ที่ใช้สำหรับรายงานวัน D | ผล |
| --- | --- | --- | --- | --- |
| ก้าวเดิน | `steps` · dailyRollUp | `steps.countSum` | D | ✔ |
| ระยะทาง | `distance` · dailyRollUp | `distance.millimetersSum` | D | ✔ |
| พลังงาน | `total-calories` · dailyRollUp | `totalCalories.kcalSum` | D | ✔ |
| พลังงานจากกิจกรรม | `active-energy-burned` · dailyRollUp | `activeEnergyBurned.kcalSum` | D | ✔ |
| Active Zone Minutes | `active-zone-minutes` · dailyRollUp | `activeZoneMinutes.sumInFatBurnHeartZone / sumInCardioHeartZone / sumInPeakHeartZone` | D | ✔ |
| นาทีที่เคลื่อนไหว (เสริม) | `active-minutes` · dailyRollUp | แยก LIGHT 92 / MODERATE 5 / VIGOROUS 30 | D | ✔ ไม่อยู่ใน Plan |
| เวลานั่งนิ่ง | `sedentary-period` · dailyRollUp | `sedentaryPeriod.durationSum` (เช่น `"48720s"`) | D | ✔ |
| ชีพจรขณะพัก | `daily-resting-heart-rate` · list | `beatsPerMinute` (`calculationMethod = WITH_SLEEP`) | D | ✔ |
| HRV | `daily-heart-rate-variability` · list | `averageHeartRateVariabilityMilliseconds` | **D+1** | ✔ |
| อุณหภูมิผิวหนัง | `daily-sleep-temperature-derivations` · list | `nightlyTemperatureCelsius − baselineTemperatureCelsius` | **D+1** | ✔ |
| อัตราการหายใจ (เสริม) | `daily-respiratory-rate` · list | `breathsPerMinute` | **D+1** | ✔ ไม่อยู่ใน Plan |
| SpO2 ขณะนอน | `daily-oxygen-saturation` · list | `averagePercentage` | **D+1** | ◐ **มีบางวันเท่านั้น** (พบเฉพาะวันที่ 11) API ส่งเฉพาะค่าที่ผ่านการตรวจสอบ จึงเป็น null บ่อย ตรงกับ FR4 |
| การนอน | `sleep` · list | `interval`, `stages[]`, `summary.minutesAsleep/minutesAwake/stagesSummary[]`, `metadata.mainSleep` | session ที่เริ่มในช่วงคืน D | ✔ |
| อายุ | `GET /v4/users/me/profile` | `age` (อายุเป็นปี) | — | ✔ |
| health user id | `GET /v4/users/me/identity` | `healthUserId` | — | ✔ |
| เวลาสวมใส่ (ตัวแทน) | `heart-rate` · list (9 หน้า ~37,000 sample/วัน) | นับนาทีที่ไม่ซ้ำจาก `sampleTime.physicalTime` | D | ✔ |
| **จำนวนชั้น** | `floors` · dailyRollUp | ได้ `{}` ว่าง | — | ✘ Fitbit Air ไม่มีข้อมูล |
| **คะแนนการนอน / readiness / cardio load** | — | ไม่มี data type | — | ✘ ไม่มีใน API |
| อุณหภูมิผิวหนังดิบ | `skin-temperature-sensors` (v4beta) | ว่าง | — | ✘ ไม่จำเป็น เพราะ v4 มีค่ารายคืนแล้ว (RK5 ลดลง) |

### รูปแบบ filter ที่ API รับจริง (ต่างจากที่คาดจากเอกสาร)

| ประเภท | ใช้ได้ | ใช้ไม่ได้ |
| --- | --- | --- |
| Daily (`daily-*`) | `x.date >= "2026-10-12" AND x.date < "2026-10-14"` | `=`, `>`, `<=` (`INVALID_DATA_POINT_FILTER_RESTRICTION_COMPARATOR`) |
| Sleep | `sleep.interval.civil_end_time >= "…T12:00:00" AND … < "…"` | `civil_start_time`, `start_time` (`INVALID_DATA_POINT_FILTER_DATA_TYPE_MEMBER`) |

ผลต่อ F4: ดึงการนอนด้วย `civil_end_time` ตั้งแต่ D 12:00 ถึง D+2 00:00 แล้วคัดตามเวลาเริ่มฝั่ง Fitbot ตามกฎ FR19

## โครงสร้างการนอน

- หนึ่งคืนเป็นหนึ่ง session (ทั้ง 2 คืนที่ตรวจ) มี `metadata.mainSleep = true` และ `stagesStatus = SUCCEEDED`
- `summary.minutesAsleep` = ผลรวม LIGHT + DEEP + REM ใช้ได้เลย ไม่ต้องคำนวณจาก stages
- เวลาเป็น UTC พร้อม `startUtcOffset = 25200s` (+07:00)
- ยังไม่พบการงีบหรือการนอนขาดตอนในข้อมูล 2 วันนี้ จะเก็บกรณีจริงระหว่าง M1 ส่วน unit test ของ F5 ใช้ข้อมูลจำลองไปก่อน
- ข้อเสนอเดิมคงไว้: กฎช่วงคืนของ Plan เป็นตัวตัดสิน เก็บ `mainSleep` ของ API ไว้เทียบ

## OQ5.1 — วิธีวัดเวลาที่สวมใส่ (เสนอให้อนุมัติ)

| ทางเลือก | ผลที่ได้ (ตัวอย่าง) | ประเมิน |
| --- | --- | --- |
| **A นาทีที่มีค่าชีพจร (แนะนำ)** | 1,398 นาที (97%) | สะท้อนการสวมจริง ค่าใช้จ่ายคือเรียก API 9 ครั้งต่อวันต่อผู้ใช้ |
| B ผลรวมช่วง `activity-level` | ราว 57% ของวัน ไม่รวมเวลานอนและมีช่วงหายไปประมาณ 3 ชม. | ใช้แทนไม่ได้ จะตัดสินผิดว่าสวมไม่พอได้ง่าย |
| C ไม่ตรวจ | — | FR25 ไม่มีผลจริง |

## ร่าง Database Design ฉบับสุดท้าย (OQ6)

| ตาราง | การเปลี่ยนแปลงจากร่างใน Plan | เหตุผล |
| --- | --- | --- |
| `daily_activity` | ตัด `floors`, `cardio_load` | ไม่มีข้อมูลจาก Fitbit Air / ไม่มีใน API |
| `daily_activity` | `active_zone_min` เก็บแยก `azm_fat_burn_min`, `azm_cardio_min`, `azm_peak_min` แล้วคำนวณผลรวมตามที่แอปแสดง | API ให้แยกโซน |
| `daily_activity` | `energy_kcal` = `total-calories`; เพิ่ม `active_energy_kcal` (nullable) | ได้ทั้งสองค่า รอ PO เลือกว่าแสดงตัวไหน |
| `sleep_nights` | ตัด `sleep_score` | ไม่มีใน API และไม่คำนวณเลียนแบบ (RK1) |
| `sleep_nights` | เพิ่ม `skin_temp_nightly_c`, `respiratory_rate` (nullable) | API ให้มา ใช้ประกอบคำแนะนำได้โดยไม่เพิ่มกฎใหม่ |
| `sleep_sessions` | ตัด `restlessness`; เพิ่ม `api_main_sleep` | ไม่มีใน API / ใช้ตรวจสอบกฎ FR19 |
| `oauth_credentials` | `health_user_id` จาก identity; เพิ่ม `expiry_warned_at`, `invalid_notified_at` | กันการเตือนซ้ำตาม D7/D8 |

ตารางอื่นคงตามร่างใน Plan หลังอนุมัติจะเขียนเป็น migration ใน F1

## ข้อค้นพบอื่น

| เรื่อง | รายละเอียด | ผลต่อ Plan |
| --- | --- | --- |
| ความเร็ว | ประมาณ 1.5–2 วินาทีต่อการเรียก ทั้งชุดต่อวันน้อยกว่า 1 นาที | ไม่มีปัญหาสำหรับ MVP |
| Refresh token โหมด Testing | เอกสารยืนยันว่าหมดอายุใน 7 วัน | D8 ตามเดิม |
| ACCOUNT_NOT_LINKED | บัญชีนี้เชื่อมแล้ว (identity ตอบ 200) รูปแบบ error ทดสอบด้วย unit test ตามตัวอย่างในเอกสาร | — |
| ข้อมูลเริ่มต้น | `membershipStartDate` ยังไม่นาน baseline อุณหภูมิยังสั้น | กฎค่าส่วนตัว (FR20) รอ 14 วันตาม Plan อยู่แล้ว |
| การขยายผู้ใช้ | OAuth client ที่ยังไม่ verify รับได้ไม่เกิน 100 คน | อัปเดต RK13 |

## ผลการตัดสินที่ Gate M0 (13 ต.ค. 2026)

| # | เรื่อง | ผลการตัดสินของ Product Owner |
| --- | --- | --- |
| 1 | ความถูกต้องของข้อมูลวันที่ 12 | ตรงกับแอปเกือบทั้งหมด ยกเว้นพลังงานที่แอปแสดง 2,212 kcal แต่ API ให้ 2,210.4 kcal (ต่าง 0.1%) ดึงซ้ำแล้วได้ค่าเดิม จึงไม่ได้มาจากการ sync ช้า จะติดตามต่อตอนเทียบข้อมูล 3–5 วันใน M1 |
| 2 | Active Zone Minutes | แอปแสดงผลรวม fat burn + cardio + peak **โดยไม่นับคูณ 2** |
| 3 | พลังงาน | แสดงทั้งสองค่า: พลังงานทั้งวัน (`total-calories`) และพลังงานจากกิจกรรม (`active-energy-burned`) |
| 4 | OQ4 ตัวชี้วัดสุดท้าย | อนุมัติ: ตัดคะแนนการนอน, readiness, cardio load และจำนวนชั้น; เพิ่มอัตราการหายใจและนาทีที่เคลื่อนไหวเป็นข้อมูลเสริม |
| 5 | OQ5.1 วิธีวัดเวลาสวมใส่ | อนุมัติทางเลือก A: นับนาทีที่มีค่าชีพจร |
| 6 | OQ6 Database Design | อนุมัติตามหัวข้อด้านบน พร้อมการปรับตามข้อ 2–4 |

ผลต่อ Database Design ที่อนุมัติ:
- `daily_activity.active_zone_min` = ผลรวมของสามโซนโดยไม่คูณ (เก็บแยกโซนด้วยตามเดิม)
- `daily_activity.energy_kcal` (total) และ `active_energy_kcal` ใช้แสดงทั้งคู่
- เพิ่ม `daily_activity.active_light_min`, `active_moderate_min`, `active_vigorous_min` จาก `active-minutes` (nullable)
- `sleep_nights.respiratory_rate` เป็นข้อมูลเสริม ไม่มีกฎค่าผิดปกติใหม่

## วิธีรันซ้ำ

```bash
cd fitbot-api
go build -o bin/spike ./cmd/spike
./bin/spike auth                      # อ่าน .env จาก fitbot-api/ หรือ fitbot/
./bin/spike run --date 2026-10-12     # ผลใน fixtures/spike/<วันที่>/index.md
./bin/spike call --path "/v4/users/me/profile"
```

## ไฟล์ใน M0

| ไฟล์ | หน้าที่ |
| --- | --- |
| `cmd/spike/main.go` | CLI: `auth`, `run`, `call`, `endpoints` |
| `internal/googlehealth/client.go` | HTTP client, retry 429/5xx, แปลง error รวม `ACCOUNT_NOT_LINKED` (ใช้ต่อใน F4) |
| `internal/spike/oauth.go` | OAuth + PKCE, token ชั่วคราวใน `.spike/token.json` (0600) ระบบจริงจะเข้ารหัสในฐานข้อมูลตาม D7 |
| `internal/spike/endpoints.json` | รายการ endpoint พร้อม filter ที่ใช้ได้จริง |
| `internal/spike/runner.go` | เรียก, ตามหน้า, บันทึก JSON ดิบ, นับนาทีที่มีชีพจร, เขียน `index.md` |

## แหล่งอ้างอิง

- [Data types](https://developers.google.com/health/data-types) · [Endpoints](https://developers.google.com/health/endpoints) · [dailyRollUp](https://developers.google.com/health/reference/rest/v4/users.dataTypes.dataPoints/dailyRollUp)
- [Sleep](https://developers.google.com/health/data-types/sleep) · [Scopes](https://developers.google.com/health/scopes) · [Setup](https://developers.google.com/health/setup) · [Troubleshooting](https://developers.google.com/health/troubleshooting)
