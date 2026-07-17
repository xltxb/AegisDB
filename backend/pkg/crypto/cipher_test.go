package crypto

import (
	"bytes"
	"testing"
)

func TestGzipEncryptRoundTrip(t *testing.T) {
	plain := []byte("id,name\n1001,Alice Chen\n1002,Bob Li\n")
	pw := GenPassword(20)
	if len(pw) != 20 {
		t.Fatalf("GenPassword len = %d, want 20", len(pw))
	}
	enc, err := GzipEncrypt(plain, pw)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(enc, plain) {
		t.Error("ciphertext still contains plaintext")
	}
	got, err := GzipDecrypt(enc, pw)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round-trip mismatch: got %q", got)
	}
	// wrong password must fail (GCM auth)
	if _, err := GzipDecrypt(enc, "wrong-password-xxxx"); err == nil {
		t.Error("decrypt with wrong password should fail")
	}
}
