package secret

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	b, err := NewBox([]byte("a-test-secret-key-123"))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := b.Encrypt([]byte("db-password"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("db-password")) {
		t.Fatal("ciphertext contains plaintext")
	}
	enc2, _ := b.Encrypt([]byte("db-password"))
	if bytes.Equal(enc, enc2) {
		t.Fatal("nonce must make every ciphertext different")
	}
	dec, err := b.Decrypt(enc)
	if err != nil || string(dec) != "db-password" {
		t.Fatalf("decrypt: %q %v", dec, err)
	}
}

func TestTamperAndWrongKeyFail(t *testing.T) {
	b, _ := NewBox([]byte("a-test-secret-key-123"))
	other, _ := NewBox([]byte("another-secret-key-456"))
	enc, _ := b.Encrypt([]byte("x"))
	if _, err := other.Decrypt(enc); err == nil {
		t.Fatal("decrypt with wrong key succeeded")
	}
	enc[len(enc)-1] ^= 1
	if _, err := b.Decrypt(enc); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err := b.Decrypt([]byte{1, 2}); err == nil {
		t.Fatal("short ciphertext accepted")
	}
}

func TestShortKeyRejected(t *testing.T) {
	if _, err := NewBox([]byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
}
