// Package crypto holds password hashing, HMAC signing and the audit hash-chain helper.
package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword reports whether pw matches the bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// HMACSHA256 computes the hex X-Vela-Signature for a webhook payload.
func HMACSHA256(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// ChainHash returns SHA256(prevHash + payload) for the tamper-proof audit chain.
func ChainHash(prevHash string, payload []byte) string {
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
