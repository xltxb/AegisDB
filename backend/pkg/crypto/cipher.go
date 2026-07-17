package crypto

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

// pwAlphabet excludes visually ambiguous characters (0/O, 1/l/I).
const pwAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// GenPassword returns a random n-char password from an unambiguous alphabet.
// It uses rejection sampling so every alphabet character is equally likely
// (a plain byte%len would bias toward the first 256%len characters).
func GenPassword(n int) string {
	if n <= 0 {
		n = 20
	}
	out := make([]byte, n)
	max := byte(256 - (256 % len(pwAlphabet))) // largest unbiased byte boundary
	buf := make([]byte, 1)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			continue
		}
		if buf[0] >= max {
			continue // reject the biased tail, resample
		}
		out[i] = pwAlphabet[int(buf[0])%len(pwAlphabet)]
		i++
	}
	return string(out)
}

// --------------------------------------------------------- secret at rest

// secretKey is the process-wide AES-256 key used to encrypt stored secrets
// (e.g. database-connection passwords). Set once at bootstrap.
var secretKey []byte

const secretPrefix = "enc:v1:" // marks an EncryptSecret ciphertext

// SetSecretKey derives the at-rest encryption key from a passphrase (usually the
// configured app secret). Must be called at bootstrap before any Encrypt/Decrypt.
func SetSecretKey(passphrase string) {
	k := sha256.Sum256([]byte(passphrase))
	secretKey = k[:]
}

// EncryptSecret AES-256-GCM encrypts a secret string and returns a prefixed,
// base64-encoded token. Empty input returns "" (nothing to protect).
func EncryptSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if len(secretKey) == 0 {
		return "", errors.New("secret key not initialized")
	}
	block, err := aes.NewCipher(secretKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return secretPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// DecryptSecret reverses EncryptSecret. For backward compatibility, a value
// without the enc prefix is treated as legacy plaintext and returned as-is.
func DecryptSecret(s string) (string, error) {
	if s == "" || len(s) < len(secretPrefix) || s[:len(secretPrefix)] != secretPrefix {
		return s, nil // legacy plaintext
	}
	if len(secretKey) == 0 {
		return "", errors.New("secret key not initialized")
	}
	raw, err := base64.StdEncoding.DecodeString(s[len(secretPrefix):])
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(secretKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", io.ErrUnexpectedEOF
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// GzipEncrypt gzip-compresses plain, then AES-256-GCM encrypts it with a key
// derived from password (SHA-256). Output layout: nonce(12) || ciphertext||tag.
//
// To decrypt: key = SHA-256(password); nonce = first 12 bytes; AES-256-GCM Open;
// then gunzip. (openssl-compatible manual steps documented for operators.)
func GzipEncrypt(plain []byte, password string) ([]byte, error) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	key := sha256.Sum256([]byte(password))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, gz.Bytes(), nil), nil
}

// GzipDecrypt reverses GzipEncrypt: AES-256-GCM open then gunzip.
func GzipDecrypt(enc []byte, password string) ([]byte, error) {
	key := sha256.Sum256([]byte(password))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(enc) < ns {
		return nil, io.ErrUnexpectedEOF
	}
	plain, err := gcm.Open(nil, enc[:ns], enc[ns:], nil)
	if err != nil {
		return nil, err
	}
	r, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
