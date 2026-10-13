package googlehealth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// รูปแบบ response จำลองตามโครงสร้างจริงที่พบใน M0 (ตัวเลขสมมติ ไม่ใช่ข้อมูลจริงของผู้ใช้)
var fake = map[string]string{
	"steps":                `"steps":{"countSum":"8000"}`,
	"distance":             `"distance":{"millimetersSum":"6000000"}`,
	"total-calories":       `"totalCalories":{"kcalSum":2100.4}`,
	"active-energy-burned": `"activeEnergyBurned":{"kcalSum":400.2}`,
	"active-zone-minutes":  `"activeZoneMinutes":{"sumInFatBurnHeartZone":"20","sumInCardioHeartZone":"10","sumInPeakHeartZone":"0"}`,
	"active-minutes":       `"activeMinutes":{"activeMinutesRollupByActivityLevel":[{"activityLevel":"LIGHT","activeMinutesSum":"80"},{"activityLevel":"VIGOROUS","activeMinutesSum":"30"}]}`,
	"sedentary-period":     `"sedentaryPeriod":{"durationSum":"36000s"}`,
}

func rollupBody(field string) string {
	if field == "" {
		return `{}`
	}
	return `{"rollupDataPoints":[{"civilStartTime":{"date":{"year":2026,"month":10,"day":12}},` + field + `}]}`
}

type fakeAPI struct {
	hrCalls int
	filters map[string]string
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	q := r.URL.Query()
	parts := strings.Split(p, "/")
	dataType := ""
	if len(parts) > 5 {
		dataType = parts[5]
	}
	if f.filters == nil {
		f.filters = map[string]string{}
	}
	f.filters[dataType] = q.Get("filter")
	switch {
	case strings.HasSuffix(p, ":dailyRollUp"):
		_, _ = io.ReadAll(r.Body)
		w.Write([]byte(rollupBody(fake[dataType])))
	case dataType == "daily-resting-heart-rate":
		w.Write([]byte(`{"dataPoints":[{"dailyRestingHeartRate":{"date":{"year":2026,"month":10,"day":12},"beatsPerMinute":"62"}}]}`))
	case dataType == "daily-heart-rate-variability":
		w.Write([]byte(`{"dataPoints":[{"dailyHeartRateVariability":{"date":{"year":2026,"month":10,"day":13},"averageHeartRateVariabilityMilliseconds":40.5}}]}`))
	case dataType == "daily-oxygen-saturation":
		w.Write([]byte(`{"dataPoints":[]}`)) // SpO2 มีบางวันเท่านั้น
	case dataType == "daily-sleep-temperature-derivations":
		w.Write([]byte(`{"dataPoints":[{"dailySleepTemperatureDerivations":{"nightlyTemperatureCelsius":34.2,"baselineTemperatureCelsius":34.0}}]}`))
	case dataType == "daily-respiratory-rate":
		w.Write([]byte(`{"dataPoints":[{"dailyRespiratoryRate":{"breathsPerMinute":14.5}}]}`))
	case dataType == "heart-rate":
		f.hrCalls++
		if q.Get("pageToken") == "" {
			w.Write([]byte(`{"dataPoints":[{"heartRate":{"sampleTime":{"physicalTime":"2026-10-12T00:00:05Z"}}},{"heartRate":{"sampleTime":{"physicalTime":"2026-10-12T00:00:45Z"}}}],"nextPageToken":"p2"}`))
			return
		}
		w.Write([]byte(`{"dataPoints":[{"heartRate":{"sampleTime":{"physicalTime":"2026-10-12T00:01:10Z"}}}]}`))
	case dataType == "sleep":
		w.Write([]byte(`{"dataPoints":[
		  {"name":"users/1/dataTypes/sleep/dataPoints/a","sleep":{"interval":{"startTime":"2026-10-12T16:10:00Z","endTime":"2026-10-12T23:40:00Z"},
		   "metadata":{"mainSleep":true},"summary":{"minutesAsleep":"442","minutesAwake":"4",
		   "stagesSummary":[{"type":"AWAKE","minutes":"4"},{"type":"LIGHT","minutes":"250"},{"type":"DEEP","minutes":"74"},{"type":"REM","minutes":"118"}]}}},
		  {"name":"users/1/dataTypes/sleep/dataPoints/old","sleep":{"interval":{"startTime":"2026-10-11T15:20:00Z","endTime":"2026-10-11T22:50:00Z"},"summary":{"minutesAsleep":"442"}}}
		]}`))
	default:
		http.NotFound(w, r)
	}
}

func TestFetchDay(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(api.handler))
	defer srv.Close()
	c := New(srv.Client())
	c.BaseURL = srv.URL
	bkk, _ := time.LoadLocation("Asia/Bangkok")
	d := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	from := time.Date(2026, 10, 12, 12, 0, 0, 0, bkk)
	data, err := c.FetchDay(context.Background(), d, bkk, from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	a := data.Activity
	if *a.Steps != 8000 || *a.DistanceM != 6000 || *a.EnergyKcal != 2100.4 || *a.ActiveEnergyKcal != 400.2 {
		t.Fatalf("activity = %+v", a)
	}
	if *a.ActiveZoneMin != 30 || *a.AZMCardioMin != 10 {
		t.Fatal("AZM ต้องเป็นผลรวมไม่คูณ (Gate M0)")
	}
	if *a.ActiveLightMin != 80 || a.ActiveModerateMin != nil || *a.ActiveVigorousMin != 30 {
		t.Fatal("active minutes ผิด หรือค่าที่ไม่มีไม่เป็น nil")
	}
	if *a.SedentaryMin != 600 || *a.RestingHR != 62 || *a.WearMinutes != 2 || api.hrCalls != 2 {
		t.Fatalf("sedentary=%d rhr=%v wear=%d hrCalls=%d", *a.SedentaryMin, *a.RestingHR, *a.WearMinutes, api.hrCalls)
	}
	n := data.Night
	if *n.HRVms != 40.5 || n.SpO2Avg != nil || *n.RespiratoryRate != 14.5 {
		t.Fatalf("night = %+v", n)
	}
	if d := *n.SkinTempDeltaC; d < 0.199 || d > 0.201 {
		t.Fatalf("skin delta = %v", d)
	}
	if len(data.Sessions) != 1 || *data.Sessions[0].DeepMin != 74 || !*data.Sessions[0].APIMainSleep {
		t.Fatalf("sessions = %+v", data.Sessions)
	}
	// ค่าที่วัดขณะนอนต้องอ่านวันที่ D+1 และ filter ต้องใช้รูปแบบที่ API รับ (>= และ <)
	if f := api.filters["daily-heart-rate-variability"]; f != `daily_heart_rate_variability.date >= "2026-10-13" AND daily_heart_rate_variability.date < "2026-10-14"` {
		t.Fatalf("hrv filter = %s", f)
	}
	if f := api.filters["daily-resting-heart-rate"]; !strings.Contains(f, `>= "2026-10-12"`) {
		t.Fatalf("rhr filter = %s", f)
	}
	if f := api.filters["sleep"]; !strings.HasPrefix(f, `sleep.interval.civil_end_time >= "2026-10-12T12:00:00"`) {
		t.Fatalf("sleep filter = %s", f)
	}
}

func TestNumParsesAllShapes(t *testing.T) {
	m := map[string]any{"a": json.Number("12"), "b": "34", "c": "48720s", "d": nil}
	if *num(m, "a") != 12 || *num(m, "b") != 34 || *num(m, "c") != 48720 || num(m, "d") != nil || num(m, "x") != nil {
		t.Fatal("num ผิด")
	}
}
