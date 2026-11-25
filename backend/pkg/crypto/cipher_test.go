package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"strings"
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

// L-DEF3(2026-10 安全审计):新密文由 scrypt 派生的密钥加密(enc:v2:);升级前写下的
// enc:v1:(SHA-256 密钥)照常解得开。
func TestSecret_V2ForNewAndV1StillReadable(t *testing.T) {
	SetSecretKey("unit-test-passphrase")
	enc, err := EncryptSecret("db-password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "enc:v2:") {
		t.Fatalf("新密文应是 v2,实际 %q", enc[:7])
	}
	if got, err := DecryptSecret(enc); err != nil || got != "db-password" {
		t.Fatalf("v2 往返失败:%q %v", got, err)
	}

	// 照旧版的写法造一条 v1 密文:key = SHA-256(口令)
	k := sha256.Sum256([]byte("unit-test-passphrase"))
	block, _ := aes.NewCipher(k[:])
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	legacy := "enc:v1:" + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte("old-password"), nil))
	if got, err := DecryptSecret(legacy); err != nil || got != "old-password" {
		t.Fatalf("v1 旧密文应仍可解:%q %v", got, err)
	}
	// 换了口令,两代都解不开(不会误把别的口令下的密文解成乱码)
	SetSecretKey("another-passphrase")
	if _, err := DecryptSecret(enc); err == nil {
		t.Error("换口令后 v2 应解不开")
	}
}
