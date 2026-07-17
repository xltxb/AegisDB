// Package totp implements RFC 6238 time-based one-time passwords (SHA1, 6
// digits, 30s period) — enough to interoperate with Google Authenticator, 1Password,
// Authy, etc. Pure standard library, no external dependencies.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	period = 30
	digits = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a fresh random base32 secret (~160 bits).
func GenerateSecret() string {
	buf := make([]byte, 20)
	_, _ = rand.Read(buf)
	return b32.EncodeToString(buf)
}

// Code returns the TOTP for secret at time t (empty string on a bad secret).
func Code(secret string, t time.Time) string {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return ""
	}
	counter := uint64(t.Unix()) / period
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	h := hmac.New(sha1.New, key)
	h.Write(msg[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%0*d", digits, bin%1_000_000)
}

// Validate reports whether code matches secret within ±1 step of clock skew.
func Validate(secret, code string, t time.Time) bool {
	ok, _ := ValidateWithCounter(secret, code, t)
	return ok
}

// ValidateWithCounter is like Validate but also returns the matched time-step
// counter, so callers can persist it and reject a later reuse of the same code
// (anti-replay). counter is 0 when ok is false.
func ValidateWithCounter(secret, code string, t time.Time) (ok bool, counter uint64) {
	code = strings.TrimSpace(code)
	if secret == "" || len(code) != digits {
		return false, 0
	}
	for _, skew := range []time.Duration{0, -period * time.Second, period * time.Second} {
		tt := t.Add(skew)
		if c := Code(secret, tt); c != "" && c == code {
			return true, uint64(tt.Unix()) / period
		}
	}
	return false, 0
}

// URI builds an otpauth:// provisioning URI for authenticator-app enrollment.
func URI(secret, account, issuer string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", digits))
	q.Set("period", fmt.Sprintf("%d", period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
