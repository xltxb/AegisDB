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

// The at-rest-encrypted export password must fit tbl_export_job.password. A 20-char
// password encrypts to ~71 chars, which overflowed the original VARCHAR(64) and
// left MySQL exports stuck "running" (the failed UPDATE was swallowed). Guard the
// column-width assumption so it can't silently regress again.
func TestEncryptSecret_FitsExportPasswordColumn(t *testing.T) {
	const exportPasswordColumn = 128
	SetSecretKey("test-secret-key-for-export-password")
	enc, err := EncryptSecret(GenPassword(20))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(enc) > exportPasswordColumn {
		t.Errorf("encrypted export password is %d chars, exceeds the %d-char column", len(enc), exportPasswordColumn)
	}
}
