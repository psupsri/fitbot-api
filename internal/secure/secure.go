// Package secure เข้ารหัส token ด้วย AES-256-GCM กุญแจอยู่นอกฐานข้อมูล (D7)
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

type Box struct{ aead cipher.AEAD }

// New รับกุญแจ base64 ขนาด 32 bytes (สร้างด้วย `fitbot keygen`)
func New(keyB64 string) (*Box, error) {
	if keyB64 == "" {
		return nil, errors.New("ต้องตั้งค่า FITBOT_ENCRYPTION_KEY (สร้างด้วย `fitbot keygen`)")
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("FITBOT_ENCRYPTION_KEY ไม่ใช่ base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("FITBOT_ENCRYPTION_KEY ต้องยาว 32 bytes (ได้ %d)", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Encrypt(plain string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (b *Box) Decrypt(sealed []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("ข้อมูลเข้ารหัสสั้นเกินไป")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", errors.New("ถอดรหัส token ไม่ได้ (FITBOT_ENCRYPTION_KEY เปลี่ยนไป? ต้องรัน `fitbot auth` ใหม่)")
	}
	return string(plain), nil
}

func GenerateKey() (string, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k), nil
}
