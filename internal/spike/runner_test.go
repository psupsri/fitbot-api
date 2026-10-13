package spike

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fitbot/internal/googlehealth"
)

var d = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

func TestDefaultEndpointsValid(t *testing.T) {
	eps, err := LoadEndpoints("")
	if err != nil || len(eps) == 0 {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range eps {
		if names[e.Name] {
			t.Fatalf("ชื่อซ้ำ %s", e.Name)
		}
		names[e.Name] = true
		if _, _, body := e.Request(DayVars(d), ""); body != nil && !json.Valid(body) {
			t.Fatalf("%s: body ไม่ใช่ JSON หลังแทนค่า: %s", e.Name, body)
		}
	}
}

func TestRequestExpansion(t *testing.T) {
	eps, _ := LoadEndpoints("")
	for _, e := range eps {
		if e.Name == "rollup-steps" {
			_, _, body := e.Request(DayVars(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)), "")
			if !strings.Contains(string(body), `"end":{"date":{"year":2027,"month":1,"day":1}}`) {
				t.Fatalf("body = %s", body)
			}
		}
		if e.Name == "list-sleep" {
			_, path, _ := e.Request(DayVars(d), "tok")
			if !strings.Contains(path, "2026-10-12T12%3A00%3A00") || !strings.Contains(path, "pageToken=tok") {
				t.Fatalf("path = %s", path)
			}
		}
	}
}

func TestRunPaginatesAndCountsWearMinutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageToken") == "" {
			io.WriteString(w, `{"dataPoints":[{"heartRate":{"sampleTime":{"physicalTime":"2026-10-12T00:00:05Z"},"beatsPerMinute":"60"}},
				{"heartRate":{"sampleTime":{"physicalTime":"2026-10-12T00:00:50Z"},"beatsPerMinute":"61"}}],"nextPageToken":"p2"}`)
			return
		}
		io.WriteString(w, `{"dataPoints":[{"heartRate":{"sampleTime":{"physicalTime":"2026-10-12T00:01:10Z"},"beatsPerMinute":"62"}}]}`)
	}))
	defer srv.Close()
	c := googlehealth.New(srv.Client())
	c.BaseURL = srv.URL
	dir := t.TempDir()
	res, err := Run(context.Background(), c, []Endpoint{{Name: "list-heart-rate", Method: "GET", Path: "/v4/x", Paginate: true}}, DayVars(d), dir, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	r := res[0]
	if r.Pages != 2 || r.Items != 3 || !strings.Contains(r.Note, "2 นาที") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "list-heart-rate.page02.json")); err != nil {
		t.Fatal(err)
	}
	if err := WriteIndex(filepath.Join(dir, "index.md"), d, res); err != nil {
		t.Fatal(err)
	}
}
