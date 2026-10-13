package secure

import "testing"

func TestRoundTripAndWrongKey(t *testing.T) {
	k, _ := GenerateKey()
	b, err := New(k)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := b.Encrypt("refresh-token")
	if got, err := b.Decrypt(enc); err != nil || got != "refresh-token" {
		t.Fatal(got, err)
	}
	k2, _ := GenerateKey()
	other, _ := New(k2)
	if _, err := other.Decrypt(enc); err == nil {
		t.Fatal("กุญแจผิดต้องถอดรหัสไม่ได้")
	}
	if _, err := New(""); err == nil {
		t.Fatal("ไม่มีกุญแจต้อง error")
	}
}
