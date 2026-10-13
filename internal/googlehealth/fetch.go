package googlehealth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fitbot/internal/model"
)

// ทุก field และรูปแบบ filter ในไฟล์นี้ยืนยันกับข้อมูลจริงแล้วใน M0 (docs/M0-api-spike.md)

// DayData คือข้อมูลของวัน D ที่แปลงแล้ว
type DayData struct {
	Activity *model.Activity      // ไม่เป็น nil ค่าที่ไม่มีเป็น nil รายตัว
	Sessions []model.SleepSession // ช่วงการนอนที่เริ่มใน [sleepFrom, sleepTo) ยังไม่จัดประเภท
	Night    model.NightMetrics   // ค่าที่วัดขณะนอนของคืนวัน D (API ผูกกับวันที่ D+1)
}

const (
	maxSleepPages = 10
	maxHRPages    = 60 // ประมาณ 37,000 sample/วัน หน้าละ 10,000
)

// FetchDay ดึงข้อมูลวัน D (วันที่ตาม timezone loc ของผู้ใช้)
func (c *Client) FetchDay(ctx context.Context, d time.Time, loc *time.Location, sleepFrom, sleepTo time.Time) (*DayData, error) {
	raw := map[string]json.RawMessage{}
	a := &model.Activity{}

	rollups := []struct {
		dataType string
		apply    func(map[string]any)
	}{
		{"steps", func(m map[string]any) { a.Steps = intPtr(num(m, "steps.countSum")) }},
		{"distance", func(m map[string]any) {
			if v := num(m, "distance.millimetersSum"); v != nil {
				a.DistanceM = model.Ptr(*v / 1000)
			}
		}},
		{"total-calories", func(m map[string]any) { a.EnergyKcal = num(m, "totalCalories.kcalSum") }},
		{"active-energy-burned", func(m map[string]any) { a.ActiveEnergyKcal = num(m, "activeEnergyBurned.kcalSum") }},
		{"active-zone-minutes", func(m map[string]any) {
			a.AZMFatBurnMin = intPtr(num(m, "activeZoneMinutes.sumInFatBurnHeartZone"))
			a.AZMCardioMin = intPtr(num(m, "activeZoneMinutes.sumInCardioHeartZone"))
			a.AZMPeakMin = intPtr(num(m, "activeZoneMinutes.sumInPeakHeartZone"))
			// ผลรวมแบบไม่คูณ ตรงกับตัวเลขในแอป (Gate M0 ข้อ 2)
			a.ActiveZoneMin = sumInts(a.AZMFatBurnMin, a.AZMCardioMin, a.AZMPeakMin)
		}},
		{"active-minutes", func(m map[string]any) {
			levels, _ := lookup(m, "activeMinutes.activeMinutesRollupByActivityLevel").([]any)
			for _, l := range levels {
				v := intPtr(num(l, "activeMinutesSum"))
				switch str(l, "activityLevel") {
				case "LIGHT":
					a.ActiveLightMin = v
				case "MODERATE":
					a.ActiveModerateMin = v
				case "VIGOROUS":
					a.ActiveVigorousMin = v
				}
			}
		}},
		{"sedentary-period", func(m map[string]any) {
			if v := num(m, "sedentaryPeriod.durationSum"); v != nil {
				a.SedentaryMin = model.Ptr(int(math.Round(*v / 60)))
			}
		}},
	}
	for _, r := range rollups {
		item, body, err := c.dailyRollUp(ctx, r.dataType, d)
		if err != nil {
			return nil, fmt.Errorf("ดึง %s: %w", r.dataType, err)
		}
		raw[r.dataType] = body
		if item != nil {
			r.apply(item)
		}
	}

	// ชีพจรขณะพักเป็นของวัน D
	rhr, body, err := c.dailyValue(ctx, "daily-resting-heart-rate", "daily_resting_heart_rate", d)
	if err != nil {
		return nil, fmt.Errorf("ดึง daily-resting-heart-rate: %w", err)
	}
	raw["daily-resting-heart-rate"] = body
	a.RestingHR = num(rhr, "dailyRestingHeartRate.beatsPerMinute")

	// ค่าที่วัดขณะนอนของคืนวัน D ถูกผูกกับวันที่ตื่น (D+1)
	wake := d.AddDate(0, 0, 1)
	var night model.NightMetrics
	nightly := []struct {
		dataType, field string
		apply           func(map[string]any)
	}{
		{"daily-heart-rate-variability", "daily_heart_rate_variability", func(m map[string]any) {
			night.HRVms = num(m, "dailyHeartRateVariability.averageHeartRateVariabilityMilliseconds")
		}},
		{"daily-oxygen-saturation", "daily_oxygen_saturation", func(m map[string]any) {
			night.SpO2Avg = num(m, "dailyOxygenSaturation.averagePercentage")
		}},
		{"daily-sleep-temperature-derivations", "daily_sleep_temperature_derivations", func(m map[string]any) {
			nightlyT := num(m, "dailySleepTemperatureDerivations.nightlyTemperatureCelsius")
			base := num(m, "dailySleepTemperatureDerivations.baselineTemperatureCelsius")
			night.SkinTempNightlyC = nightlyT
			if nightlyT != nil && base != nil {
				night.SkinTempDeltaC = model.Ptr(*nightlyT - *base)
			}
		}},
		{"daily-respiratory-rate", "daily_respiratory_rate", func(m map[string]any) {
			night.RespiratoryRate = num(m, "dailyRespiratoryRate.breathsPerMinute")
		}},
	}
	for _, n := range nightly {
		item, body, err := c.dailyValue(ctx, n.dataType, n.field, wake)
		if err != nil {
			return nil, fmt.Errorf("ดึง %s: %w", n.dataType, err)
		}
		raw[n.dataType] = body
		if item != nil {
			n.apply(item)
		}
	}

	wear, err := c.heartRateMinutes(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("ดึง heart-rate (เวลาสวมใส่): %w", err)
	}
	a.WearMinutes = wear

	sessions, sleepRaw, err := c.sleepSessions(ctx, sleepFrom, sleepTo, loc)
	if err != nil {
		return nil, fmt.Errorf("ดึง sleep: %w", err)
	}
	raw["sleep"] = sleepRaw

	a.Raw, _ = json.Marshal(raw)
	return &DayData{Activity: a, Sessions: sessions, Night: night}, nil
}

// Profile คืนอายุจาก users/me/profile (FR22)
func (c *Client) Profile(ctx context.Context) (*int, error) {
	resp, err := c.Do(ctx, "GET", "/v4/users/me/profile", nil)
	if err != nil {
		return nil, err
	}
	m, err := decodeMap(resp.Body)
	if err != nil {
		return nil, err
	}
	return intPtr(num(m, "age")), nil
}

// Identity คืน healthUserId ใช้ตรวจว่าบัญชีเชื่อมกับ Google Health แล้ว (ACCOUNT_NOT_LINKED)
func (c *Client) Identity(ctx context.Context) (string, error) {
	resp, err := c.Do(ctx, "GET", "/v4/users/me/identity", nil)
	if err != nil {
		return "", err
	}
	m, err := decodeMap(resp.Body)
	if err != nil {
		return "", err
	}
	return str(m, "healthUserId"), nil
}

// ---------- endpoint ----------

func civilDate(d time.Time) map[string]int {
	return map[string]int{"year": d.Year(), "month": int(d.Month()), "day": d.Day()}
}

func (c *Client) dailyRollUp(ctx context.Context, dataType string, d time.Time) (map[string]any, json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{
		"range":          map[string]any{"start": map[string]any{"date": civilDate(d)}, "end": map[string]any{"date": civilDate(d.AddDate(0, 0, 1))}},
		"windowSizeDays": 1,
	})
	resp, err := c.Do(ctx, "POST", "/v4/users/me/dataTypes/"+dataType+"/dataPoints:dailyRollUp", body)
	if err != nil {
		return nil, nil, err
	}
	m, err := decodeMap(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	items, _ := m["rollupDataPoints"].([]any)
	if len(items) == 0 {
		return nil, resp.Body, nil
	}
	item, _ := items[0].(map[string]any)
	return item, resp.Body, nil
}

// dailyValue คืน data point ของวันที่ date (API รับเฉพาะ >= และ < ใน filter)
func (c *Client) dailyValue(ctx context.Context, dataType, field string, date time.Time) (map[string]any, json.RawMessage, error) {
	filter := fmt.Sprintf(`%s.date >= "%s" AND %s.date < "%s"`, field, date.Format("2006-01-02"), field, date.AddDate(0, 0, 1).Format("2006-01-02"))
	resp, err := c.Do(ctx, "GET", "/v4/users/me/dataTypes/"+dataType+"/dataPoints?"+url.Values{"filter": {filter}}.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	m, err := decodeMap(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	items, _ := m["dataPoints"].([]any)
	if len(items) == 0 {
		return nil, resp.Body, nil
	}
	item, _ := items[0].(map[string]any)
	return item, resp.Body, nil
}

// list ดึงทุกหน้าของ list endpoint
func (c *Client) list(ctx context.Context, dataType string, q url.Values, maxPages int) ([]any, error) {
	var all []any
	for page := 0; page < maxPages; page++ {
		resp, err := c.Do(ctx, "GET", "/v4/users/me/dataTypes/"+dataType+"/dataPoints?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		m, err := decodeMap(resp.Body)
		if err != nil {
			return nil, err
		}
		items, _ := m["dataPoints"].([]any)
		all = append(all, items...)
		next, _ := m["nextPageToken"].(string)
		if next == "" {
			return all, nil
		}
		q.Set("pageToken", next)
	}
	return nil, fmt.Errorf("%s มีมากกว่า %d หน้า", dataType, maxPages)
}

// heartRateMinutes นับนาทีที่มีค่าชีพจรของวัน D ใช้เป็นเวลาที่สวมใส่ (OQ5.1 ทางเลือก A)
func (c *Client) heartRateMinutes(ctx context.Context, d time.Time) (*int, error) {
	filter := fmt.Sprintf(`heart_rate.sample_time.civil_time >= "%sT00:00:00" AND heart_rate.sample_time.civil_time < "%sT00:00:00"`,
		d.Format("2006-01-02"), d.AddDate(0, 0, 1).Format("2006-01-02"))
	items, err := c.list(ctx, "heart-rate", url.Values{"filter": {filter}, "pageSize": {"10000"}}, maxHRPages)
	if err != nil {
		return nil, err
	}
	return model.Ptr(DistinctMinutes(items)), nil
}

// DistinctMinutes นับนาทีที่ไม่ซ้ำจาก heartRate.sampleTime.physicalTime
func DistinctMinutes(items []any) int {
	seen := map[int64]bool{}
	for _, it := range items {
		if t, err := time.Parse(time.RFC3339Nano, str(it, "heartRate.sampleTime.physicalTime")); err == nil {
			seen[t.Unix()/60] = true
		}
	}
	return len(seen)
}

// sleepSessions ดึงช่วงการนอนที่เริ่มใน [from, to)
// API filter ได้เฉพาะ civil_end_time จึงดึงช่วงที่จบหลัง from และก่อน to+12 ชม. แล้วคัดตามเวลาเริ่ม
func (c *Client) sleepSessions(ctx context.Context, from, to time.Time, loc *time.Location) ([]model.SleepSession, json.RawMessage, error) {
	const civil = "2006-01-02T15:04:05"
	filter := fmt.Sprintf(`sleep.interval.civil_end_time >= "%s" AND sleep.interval.civil_end_time < "%s"`,
		from.In(loc).Format(civil), to.Add(12*time.Hour).In(loc).Format(civil))
	items, err := c.list(ctx, "sleep", url.Values{"filter": {filter}, "pageSize": {"25"}}, maxSleepPages)
	if err != nil {
		return nil, nil, err
	}
	raw, _ := json.Marshal(items)
	var out []model.SleepSession
	for _, it := range items {
		s, ok := ParseSleep(it)
		if ok && !s.Start.Before(from) && s.Start.Before(to) {
			out = append(out, s)
		}
	}
	return out, raw, nil
}

// ParseSleep แปลง data point ของ sleep เป็น SleepSession
func ParseSleep(it any) (model.SleepSession, bool) {
	start, err1 := time.Parse(time.RFC3339Nano, str(it, "sleep.interval.startTime"))
	end, err2 := time.Parse(time.RFC3339Nano, str(it, "sleep.interval.endTime"))
	if err1 != nil || err2 != nil || !end.After(start) {
		return model.SleepSession{}, false
	}
	id := str(it, "name")
	if id == "" {
		id = fmt.Sprintf("start-%d", start.Unix())
	}
	s := model.SleepSession{
		ExternalID: id, Start: start, End: end,
		AsleepMin: intPtr(num(it, "sleep.summary.minutesAsleep")),
		AwakeMin:  intPtr(num(it, "sleep.summary.minutesAwake")),
	}
	if v, ok := lookup(it, "sleep.metadata.mainSleep").(bool); ok {
		s.APIMainSleep = &v
	}
	stages, _ := lookup(it, "sleep.summary.stagesSummary").([]any)
	for _, st := range stages {
		v := intPtr(num(st, "minutes"))
		switch str(st, "type") {
		case "DEEP":
			s.DeepMin = v
		case "LIGHT":
			s.LightMin = v
		case "REM":
			s.RemMin = v
		}
	}
	s.Raw, _ = json.Marshal(it)
	return s, true
}

// ---------- แปลงค่า ----------

func decodeMap(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("แปลง JSON: %w", err)
	}
	return m, nil
}

func lookup(v any, path string) any {
	for _, p := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func str(v any, path string) string {
	s, _ := lookup(v, path).(string)
	return s
}

// num อ่านตัวเลขที่อาจเป็น number, string ("7350") หรือ duration ("48720s") ไม่มีค่าคืน nil
func num(v any, path string) *float64 {
	switch t := lookup(v, path).(type) {
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return &f
		}
	case string:
		s := strings.TrimSuffix(strings.TrimSpace(t), "s")
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return &f
		}
	}
	return nil
}

func intPtr(f *float64) *int {
	if f == nil {
		return nil
	}
	return model.Ptr(int(math.Round(*f)))
}

func sumInts(vs ...*int) *int {
	var total int
	found := false
	for _, v := range vs {
		if v != nil {
			total += *v
			found = true
		}
	}
	if !found {
		return nil
	}
	return &total
}
