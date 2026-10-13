// Package line คือ LINE Notifier (F11): Push Message แบบข้อความธรรมดาถึงผู้ใช้ทีละคน (D6)
package line

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LINE รับได้ไม่เกิน 5 ข้อความต่อการ push และ 5,000 ตัวอักษรต่อข้อความ
const (
	MaxMessagesPerPush = 5
	MaxRunesPerMessage = 5000
)

type Notifier interface {
	Push(ctx context.Context, to string, messages []string) error
}

type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
	Sleep   func(time.Duration)
}

func New(token, baseURL string) *Client {
	return &Client{Token: token, BaseURL: baseURL, HTTP: &http.Client{Timeout: 15 * time.Second}, Sleep: time.Sleep}
}

// Push ส่งทุกข้อความ ถ้ามีมากกว่า 5 ข้อความแบ่งเป็นหลายครั้ง
// คืน error ถ้าส่งไม่สำเร็จ ผู้เรียกต้องไม่บันทึกว่าส่งแล้ว (AC F11)
func (c *Client) Push(ctx context.Context, to string, messages []string) error {
	if c.Token == "" {
		return errors.New("ต้องตั้งค่า LINE_CHANNEL_ACCESS_TOKEN ใน .env (หรือใช้ --dry-run)")
	}
	if to == "" {
		return errors.New("ผู้ใช้ยังไม่มี LINE user ID (fitbot user update --id N --line-user-id U...)")
	}
	for _, m := range messages {
		if n := len([]rune(m)); n > MaxRunesPerMessage {
			return fmt.Errorf("ข้อความยาว %d ตัวอักษร เกินขีดจำกัดของ LINE (%d)", n, MaxRunesPerMessage)
		}
	}
	for start := 0; start < len(messages); start += MaxMessagesPerPush {
		if err := c.push(ctx, to, messages[start:min(start+MaxMessagesPerPush, len(messages))]); err != nil {
			return err
		}
	}
	return nil
}

type textMessage struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (c *Client) push(ctx context.Context, to string, batch []string) error {
	msgs := make([]textMessage, len(batch))
	for i, t := range batch {
		msgs[i] = textMessage{Type: "text", Text: t}
	}
	body, err := json.Marshal(map[string]any{"to": to, "messages": msgs})
	if err != nil {
		return err
	}
	retryKey := uuid() // key เดียวกันทุกครั้งที่ลองซ้ำ LINE จึงไม่ส่งซ้ำ
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			c.Sleep(time.Duration(attempt*2) * time.Second)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/v2/bot/message/push", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("X-Line-Retry-Key", retryKey)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			return nil
		case resp.StatusCode == http.StatusConflict:
			return nil // ส่งสำเร็จไปแล้วด้วย retry key เดียวกัน
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("LINE HTTP %d: %s", resp.StatusCode, b)
		default:
			return fmt.Errorf("LINE HTTP %d: %s", resp.StatusCode, b)
		}
	}
	return lastErr
}

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// DryRun พิมพ์ข้อความลงหน้าจอแทนการส่งจริง (AC F11)
type DryRun struct{ Out io.Writer }

func (d DryRun) Push(_ context.Context, to string, messages []string) error {
	for i, m := range messages {
		fmt.Fprintf(d.Out, "──── [dry-run] LINE ถึง %s · ข้อความ %d/%d ────\n%s\n", to, i+1, len(messages), m)
	}
	return nil
}
