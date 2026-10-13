package spike

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"fitbot/internal/googlehealth"
)

//go:embed endpoints.json
var defaultEndpoints []byte

// Endpoint คือการเรียกหนึ่งรายการใน spike แก้ไขได้ผ่านไฟล์ JSON โดยไม่ต้อง build ใหม่
type Endpoint struct {
	Name     string            `json:"name"`
	Checks   string            `json:"checks"`
	Column   string            `json:"column"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Query    map[string]string `json:"query,omitempty"`
	Body     json.RawMessage   `json:"body,omitempty"`
	Paginate bool              `json:"paginate,omitempty"`
}

func DefaultEndpointsJSON() []byte { return defaultEndpoints }

func LoadEndpoints(path string) ([]Endpoint, error) {
	b := defaultEndpoints
	if path != "" {
		var err error
		if b, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	var eps []Endpoint
	if err := json.Unmarshal(b, &eps); err != nil {
		return nil, fmt.Errorf("อ่านรายการ endpoint: %w", err)
	}
	return eps, nil
}

// Vars คือค่าที่แทนใน path/query/body ของวัน D
type Vars map[string]string

// NumericVars แทนใน body เป็นตัวเลข (ไม่มีเครื่องหมายคำพูด)
var numericVars = []string{"y", "m", "d", "ny", "nm", "nd"}

func DayVars(d time.Time) Vars {
	n := d.AddDate(0, 0, 1)
	return Vars{
		"date": d.Format("2006-01-02"), "next": n.Format("2006-01-02"), "next2": d.AddDate(0, 0, 2).Format("2006-01-02"),
		"y": strconv.Itoa(d.Year()), "m": strconv.Itoa(int(d.Month())), "d": strconv.Itoa(d.Day()),
		"ny": strconv.Itoa(n.Year()), "nm": strconv.Itoa(int(n.Month())), "nd": strconv.Itoa(n.Day()),
	}
}

func (v Vars) expand(s string) string {
	for k, val := range v {
		s = strings.ReplaceAll(s, "{"+k+"}", val)
	}
	return s
}

func (v Vars) expandBody(b json.RawMessage) []byte {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	for _, k := range numericVars {
		s = strings.ReplaceAll(s, `"{`+k+`}"`, v[k])
	}
	out := []byte(v.expand(s))
	var buf bytes.Buffer
	if json.Compact(&buf, out) == nil {
		return buf.Bytes()
	}
	return out
}

// Request สร้าง path พร้อม query string ที่ encode แล้ว
func (e Endpoint) Request(v Vars, pageToken string) (method, path string, body []byte) {
	q := url.Values{}
	for k, val := range e.Query {
		q.Set(k, v.expand(val))
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	path = v.expand(e.Path)
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	method = e.Method
	if method == "" {
		method = "GET"
	}
	return method, path, v.expandBody(e.Body)
}

// Result คือสรุปผลของ endpoint หนึ่งรายการ
type Result struct {
	Endpoint   Endpoint
	Status     int
	Error      string
	Reason     string
	Pages      int
	Items      int
	Fields     []string // field path ที่พบใน item แรก ใช้ทำ mapping
	Note       string   // ข้อสังเกตเพิ่มเติม เช่น จำนวนนาทีที่มีชีพจร
	Files      []string
	DurationMs int64
}

const maxPages = 30

// Run เรียกทุก endpoint บันทึก response ดิบ (รวม error) แล้วคืนสรุป
func Run(ctx context.Context, c *googlehealth.Client, eps []Endpoint, v Vars, outDir string, loc *time.Location) ([]Result, error) {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, err
	}
	var results []Result
	for _, e := range eps {
		r := Result{Endpoint: e}
		var all []any
		token := ""
		for page := 1; page <= maxPages; page++ {
			method, path, body := e.Request(v, token)
			resp, err := c.Do(ctx, method, path, body)
			if resp != nil {
				r.Status = resp.Status
				r.DurationMs += resp.Duration.Milliseconds()
				name := e.Name + ".json"
				if page > 1 {
					name = fmt.Sprintf("%s.page%02d.json", e.Name, page)
				}
				if werr := writePretty(filepath.Join(outDir, name), resp.Body); werr != nil {
					return results, werr
				}
				r.Files = append(r.Files, name)
			}
			if err != nil {
				r.Error = err.Error()
				var ae *googlehealth.APIError
				if errors.As(err, &ae) {
					r.Reason = ae.Reason
				}
				break
			}
			r.Pages = page
			doc, _ := decode(resp.Body)
			items := firstArray(doc)
			all = append(all, items...)
			next, _ := lookup(doc, "nextPageToken").(string)
			if !e.Paginate || next == "" {
				if page == 1 && len(items) == 0 {
					r.Fields = flatten(doc, "", 6)
				}
				break
			}
			token = next
		}
		r.Items = len(all)
		if len(all) > 0 {
			r.Fields = flatten(all[0], "", 6)
		}
		if strings.Contains(e.Name, "heart-rate") && strings.HasPrefix(e.Name, "list-heart-rate") && len(all) > 0 {
			mins, first, last := distinctMinutes(all, loc)
			r.Note = fmt.Sprintf("มีค่าชีพจร %d นาทีที่ไม่ซ้ำ (%.0f%% ของวัน) ช่วง %s–%s", mins, float64(mins)/14.4, first, last)
		}
		results = append(results, r)
	}
	return results, nil
}

func writePretty(path string, b []byte) error {
	var buf bytes.Buffer
	if json.Indent(&buf, b, "", "  ") != nil {
		buf.Reset()
		buf.Write(b)
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func decode(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
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

// firstArray คืน array แรกในระดับบนสุด (เช่น dataPoints หรือ rollupDataPoints)
func firstArray(doc any) []any {
	m, ok := doc.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if arr, ok := m[k].([]any); ok {
			return arr
		}
	}
	return nil
}

// flatten แสดง field path ทั้งหมดพร้อมตัวอย่างค่า เช่น "steps.count = 1234"
func flatten(v any, prefix string, depth int) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if depth <= 0 {
				out = append(out, p+" = …")
				continue
			}
			out = append(out, flatten(t[k], p, depth-1)...)
		}
	case []any:
		if len(t) == 0 {
			return []string{prefix + "[] (ว่าง)"}
		}
		out = append(out, flatten(t[0], prefix+"[0]", depth-1)...)
		if len(t) > 1 {
			out = append(out, fmt.Sprintf("%s[] (%d รายการ)", prefix, len(t)))
		}
	default:
		out = append(out, fmt.Sprintf("%s = %v", prefix, t))
	}
	return out
}

// distinctMinutes นับนาทีที่ไม่ซ้ำที่มีค่าชีพจร ใช้เป็นตัวแทนเวลาที่สวมใส่ (RK6)
func distinctMinutes(items []any, loc *time.Location) (int, string, string) {
	seen := map[int64]bool{}
	var first, last time.Time
	for _, it := range items {
		s, _ := lookup(it, "heartRate.sampleTime.physicalTime").(string)
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			continue
		}
		seen[t.Unix()/60] = true
		if first.IsZero() || t.Before(first) {
			first = t
		}
		if t.After(last) {
			last = t
		}
	}
	if len(seen) == 0 {
		return 0, "-", "-"
	}
	return len(seen), first.In(loc).Format("15:04"), last.In(loc).Format("15:04")
}

// WriteIndex เขียนสรุปผลเป็น Markdown ไว้คู่กับไฟล์ JSON
func WriteIndex(path string, d time.Time, results []Result) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# ผล F0 API Spike ของวันที่ %s\n\nสร้างเมื่อ %s\n\n", d.Format("2006-01-02"), time.Now().Format(time.RFC3339))
	b.WriteString("| endpoint | ตรวจอะไร | คอลัมน์ปลายทาง | HTTP | รายการ | หมายเหตุ |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, r := range results {
		note := r.Note
		if r.Error != "" {
			note = strings.ReplaceAll(r.Error, "|", "/")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %s |\n", r.Endpoint.Name, r.Endpoint.Checks, r.Endpoint.Column, r.Status, r.Items, note)
	}
	b.WriteString("\n## Field ที่พบใน item แรก\n")
	for _, r := range results {
		if len(r.Fields) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n```\n%s\n```\n", r.Endpoint.Name, strings.Join(r.Fields, "\n"))
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
