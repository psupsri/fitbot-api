// Package anomaly ประเมินกฎค่าผิดปกติจากไฟล์ตั้งค่า (F8) กฎตัวเลขเป็นตัวตัดสิน (FR17)
// ทุกการเตือนบอกค่า เกณฑ์ และแหล่งอ้างอิง LLM ได้รับรายการที่ตัดสินแล้วเพื่ออธิบายเท่านั้น
package anomaly

import (
	_ "embed"
	"fmt"
	"math"
	"os"

	"gopkg.in/yaml.v3"

	"fitbot/internal/metrics"
)

const (
	LevelWatch   = "watch"   // ค่าที่ควรเฝ้าระวัง (section 6)
	LevelImprove = "improve" // ควรปรับปรุง (section 5)
)

//go:embed rules.yaml
var defaultRules []byte

type Rule struct {
	ID        string  `yaml:"id"`
	Metric    string  `yaml:"metric"`
	Label     string  `yaml:"label"`
	Level     string  `yaml:"level"`
	Kind      string  `yaml:"kind"`
	Stat      string  `yaml:"stat"`
	Op        string  `yaml:"op"`
	Threshold float64 `yaml:"threshold"`
	Criterion string  `yaml:"criterion"`
	Reference string  `yaml:"reference"`
	// personal
	Direction    string  `yaml:"direction"`
	SDMultiplier float64 `yaml:"sd_multiplier"`
	BaselineDays int     `yaml:"baseline_days"`
	Consecutive  int     `yaml:"consecutive"`
}

// LoadRules อ่านกฎจากไฟล์ (path ว่าง = กฎตั้งต้นที่ฝังใน binary) และตรวจความถูกต้อง
func LoadRules(path string) ([]Rule, error) {
	b := defaultRules
	if path != "" {
		var err error
		if b, err = os.ReadFile(path); err != nil {
			return nil, fmt.Errorf("อ่านไฟล์กฎ %s: %w", path, err)
		}
	}
	var f struct {
		Rules []Rule `yaml:"rules"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("แปลงไฟล์กฎ: %w", err)
	}
	for _, r := range f.Rules {
		if err := validate(r); err != nil {
			return nil, fmt.Errorf("กฎ %s: %w", r.ID, err)
		}
	}
	return f.Rules, nil
}

func validate(r Rule) error {
	if _, ok := metrics.DefByKey(r.Metric); !ok {
		return fmt.Errorf("ไม่รู้จักตัวชี้วัด %q", r.Metric)
	}
	if r.Level != LevelWatch && r.Level != LevelImprove {
		return fmt.Errorf("level ต้องเป็น watch หรือ improve")
	}
	if r.Reference == "" {
		return fmt.Errorf("ต้องมี reference")
	}
	switch r.Kind {
	case "threshold":
		if r.Stat != "today" && r.Stat != "avg7" {
			return fmt.Errorf("stat ต้องเป็น today หรือ avg7")
		}
		if _, err := compare(0, r.Op, 0); err != nil {
			return err
		}
	case "personal":
		if r.Consecutive <= 0 || r.BaselineDays <= 0 || r.SDMultiplier <= 0 {
			return fmt.Errorf("personal ต้องมี consecutive, baseline_days และ sd_multiplier มากกว่า 0")
		}
	default:
		return fmt.Errorf("kind ต้องเป็น threshold หรือ personal")
	}
	return nil
}

type Alert struct {
	RuleID    string
	Level     string
	Metric    string
	Label     string
	Value     string // ค่าที่พบ (จัดรูปแบบแล้ว)
	Criterion string // เกณฑ์
	Reference string // แหล่งอ้างอิง
}

// Evaluate ประเมินทุกกฎตามลำดับในไฟล์
func Evaluate(res *metrics.Result, rules []Rule, baselineMinDays int) []Alert {
	var out []Alert
	for _, r := range rules {
		def, _ := metrics.DefByKey(r.Metric)
		label := r.Label
		if label == "" {
			label = def.Label
		}
		var a *Alert
		if r.Kind == "personal" {
			a = personal(res, r, def, baselineMinDays)
		} else {
			a = threshold(res, r, def)
		}
		if a != nil {
			a.RuleID, a.Level, a.Metric, a.Label, a.Reference = r.ID, r.Level, r.Metric, label, r.Reference
			out = append(out, *a)
		}
	}
	return out
}

func Split(alerts []Alert) (watch, improve []Alert) {
	for _, a := range alerts {
		if a.Level == LevelImprove {
			improve = append(improve, a)
		} else {
			watch = append(watch, a)
		}
	}
	return
}

func threshold(res *metrics.Result, r Rule, def metrics.Def) *Alert {
	s := res.Get(r.Metric)
	v, prefix := s.Today, ""
	if r.Stat == "avg7" {
		v, prefix = s.Window(7).Avg, "เฉลี่ย 7 วัน " // nil ถ้าข้อมูลไม่ถึงขั้นต่ำ
	}
	if v == nil {
		return nil
	}
	if hit, _ := compare(*v, r.Op, r.Threshold); !hit {
		return nil
	}
	crit := r.Criterion
	if crit == "" {
		crit = fmt.Sprintf("%s %s", r.Op, def.Format(r.Threshold))
	}
	return &Alert{Value: prefix + def.Format(*v), Criterion: crit}
}

func compare(v float64, op string, t float64) (bool, error) {
	switch op {
	case "<":
		return v < t, nil
	case "<=":
		return v <= t, nil
	case ">":
		return v > t, nil
	case ">=":
		return v >= t, nil
	}
	return false, fmt.Errorf("op ไม่รู้จัก %q", op)
}

// personal: เบี่ยงจากค่าปกติของผู้ใช้เกิน sd_multiplier × SD ติดกัน consecutive วันล่าสุด (FR20)
// ค่าปกติคิดจาก baseline_days วันก่อนช่วงที่ตรวจ ต้องมีข้อมูลอย่างน้อย baselineMinDays วัน
func personal(res *metrics.Result, r Rule, def metrics.Def, baselineMinDays int) *Alert {
	vals := metrics.Values(res.Rows, r.Metric)
	n := len(vals)
	if n < r.Consecutive+1 {
		return nil
	}
	recent := vals[n-r.Consecutive:]
	mean, sd, count := metrics.MeanSD(vals[max(0, n-r.Consecutive-r.BaselineDays) : n-r.Consecutive])
	if count < baselineMinDays || sd == 0 {
		return nil
	}
	limit := r.SDMultiplier * sd
	for _, v := range recent {
		if v == nil {
			return nil
		}
		dev := *v - mean
		switch r.Direction {
		case "below":
			if dev >= -limit {
				return nil
			}
		case "above":
			if dev <= limit {
				return nil
			}
		default:
			if math.Abs(dev) <= limit {
				return nil
			}
		}
	}
	dir := map[string]string{"below": "ต่ำกว่า", "above": "สูงกว่า"}[r.Direction]
	if dir == "" {
		dir = "เบี่ยงจาก"
	}
	return &Alert{
		Value:     fmt.Sprintf("ล่าสุด %s (ค่าปกติ %s ± %s)", def.Format(*recent[len(recent)-1]), def.Format(mean), def.Format(sd)),
		Criterion: fmt.Sprintf("%sค่าเฉลี่ย %d วันเกิน %.0f เท่าของส่วนเบี่ยงเบนมาตรฐาน %d วันติดกัน", dir, r.BaselineDays, r.SDMultiplier, r.Consecutive),
	}
}
