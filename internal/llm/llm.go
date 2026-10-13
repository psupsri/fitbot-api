// Package llm คือ LLM Adapter: interface กลาง เรียกแบบ OpenAI-compatible (D5)
// ใช้กับ Ollama บนเครื่องใน MVP และเปลี่ยนเป็น AI บน cloud ได้ด้วยการตั้งค่า (NFR1)
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client interface {
	Chat(ctx context.Context, msgs []Message) (string, error)
}

type Config struct {
	Enabled         bool    `yaml:"enabled"`
	BaseURL         string  `yaml:"base_url"`
	Model           string  `yaml:"model"`
	APIKey          string  `yaml:"-"` // LLM_API_KEY (ว่างได้สำหรับ Ollama)
	Temperature     float64 `yaml:"temperature"`
	TimeoutSeconds  int     `yaml:"timeout_seconds"`
	ReasoningEffort string  `yaml:"reasoning_effort"` // "none" ปิดส่วน thinking (RK8) ว่าง = ไม่ส่ง
	JSONMode        bool    `yaml:"json_mode"`
}

func DefaultConfig() Config {
	return Config{Enabled: true, BaseURL: "http://localhost:11434/v1", Model: "qwen3.5:9b", Temperature: 0.3,
		TimeoutSeconds: 120, ReasoningEffort: "none", JSONMode: true}
}

type OpenAICompatible struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *OpenAICompatible {
	return &OpenAICompatible{cfg: cfg, http: &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}}
}

type chatRequest struct {
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	Temperature     float64        `json:"temperature"`
	Stream          bool           `json:"stream"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	ResponseFormat  map[string]any `json:"response_format,omitempty"`
}

func (c *OpenAICompatible) Chat(ctx context.Context, msgs []Message) (string, error) {
	req := chatRequest{Model: c.cfg.Model, Messages: msgs, Temperature: c.cfg.Temperature, ReasoningEffort: c.cfg.ReasoningEffort}
	if c.cfg.JSONMode {
		req.ResponseFormat = map[string]any{"type": "json_object"}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		return "", fmt.Errorf("เรียก LLM ไม่สำเร็จ (Ollama รันอยู่หรือไม่?): %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	var cr struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &cr); err != nil {
		return "", fmt.Errorf("LLM ตอบกลับไม่ใช่ JSON (HTTP %d)", resp.StatusCode)
	}
	if cr.Error != nil {
		return "", fmt.Errorf("LLM: %s", cr.Error.Message)
	}
	if resp.StatusCode != http.StatusOK || len(cr.Choices) == 0 {
		return "", fmt.Errorf("LLM ตอบกลับผิดปกติ (HTTP %d)", resp.StatusCode)
	}
	out := StripThinking(cr.Choices[0].Message.Content)
	if out == "" {
		return "", errors.New("LLM ตอบกลับว่างเปล่า")
	}
	return out, nil
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

// StripThinking ตัดส่วน thinking ที่อาจหลุดมาในคำตอบ (RK8)
func StripThinking(s string) string {
	s = thinkRe.ReplaceAllString(s, "")
	if i := strings.Index(s, "</think>"); i >= 0 { // เปิดแท็กถูกตัดไปแล้วแต่ยังเหลือแท็กปิด
		s = s[i+len("</think>"):]
	}
	if i := strings.Index(s, "<think>"); i >= 0 { // เปิดแท็กแต่ไม่ปิด
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
