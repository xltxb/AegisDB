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
	"sync"

	"golang.org/x/crypto/scrypt"
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

// secretKey / secretKeyV2 are the process-wide AES-256 keys used to encrypt stored
// secrets (e.g. database-connection passwords). Set once at bootstrap.
//
// 两代密钥(2026-10 安全审计 L-DEF3):v1 是 SHA-256(口令) —— 没有 KDF,拿到库里密文的人能
// 以每秒上亿次的速度离线猜口令。v2 由 scrypt 派生,每猜一次要几十毫秒。新写的一律 v2;
// 旧的 v1 密文照常解得开,下次保存时变成 v2。注意回退:v2 密文旧版本解不开 —— 升级后
// 又回退到旧包,升级期间保存过的口令要重新填。
var (
	secretKey   []byte // v1:只用来解旧密文
	secretKeyV2 []byte
)

const (
	secretPrefix   = "enc:v1:" // marks a legacy (SHA-256 keyed) ciphertext
	secretPrefixV2 = "enc:v2:" // marks an EncryptSecret ciphertext (scrypt keyed)
)

// scrypt 参数:N=2^15、r=8、p=1,一次约几十毫秒、32MB 内存。盐是固定的用途串:密钥必须
// 每次启动都派生出同一把,而口令本身就是每套部署一份。
var scryptSalt = []byte("vela-gateway/at-rest/v2")

var (
	kdfMu    sync.Mutex
	kdfCache = map[string][]byte{} // 同一口令只派生一次(测试里每个 app 都会调一次 SetSecretKey)
)

func deriveKeyV2(passphrase string) []byte {
	kdfMu.Lock()
	defer kdfMu.Unlock()
	if k, ok := kdfCache[passphrase]; ok {
		return k
	}
	k, err := scrypt.Key([]byte(passphrase), scryptSalt, 1<<15, 8, 1, 32)
	if err != nil {
		panic("scrypt: " + err.Error()) // 只会因参数非法失败,参数是常量
	}
	kdfCache[passphrase] = k
	return k
}

// SetSecretKey derives the at-rest encryption keys from a passphrase (usually the
// configured app secret). Must be called at bootstrap before any Encrypt/Decrypt.
func SetSecretKey(passphrase string) {
	k := sha256.Sum256([]byte(passphrase))
	secretKey = k[:]
	secretKeyV2 = deriveKeyV2(passphrase)
}

// DeriveSalt derives a purpose-scoped byte string from the app secret.
//
// 为什么不直接把密钥当盐用:盐会参与到**对外可见**的产物里(打码值就是给人看的),
// 而密钥不该出现在任何可见物的输入里 —— 哪怕只是间接地。带上用途串,同一把密钥
// 派生出的几个用途之间也互相独立,将来多一个用途不会牵动已有的那些。
func DeriveSalt(passphrase, purpose string) []byte {
	sum := sha256.Sum256([]byte(purpose + "|" + passphrase))
	return sum[:]
}

// EncryptSecret AES-256-GCM encrypts a secret string and returns a prefixed,
// base64-encoded token. Empty input returns "" (nothing to protect).
func EncryptSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if len(secretKeyV2) == 0 {
		return "", errors.New("secret key not initialized")
	}
	block, err := aes.NewCipher(secretKeyV2)
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
	return secretPrefixV2 + base64.StdEncoding.EncodeToString(ct), nil
}

// DecryptSecret reverses EncryptSecret. For backward compatibility, a value
// without the enc prefix is treated as legacy plaintext and returned as-is.
func DecryptSecret(s string) (string, error) {
	var key []byte
	switch {
	case len(s) >= len(secretPrefixV2) && s[:len(secretPrefixV2)] == secretPrefixV2:
		key = secretKeyV2
	case len(s) >= len(secretPrefix) && s[:len(secretPrefix)] == secretPrefix:
		key = secretKey // 旧密文(v1)
	default:
		return s, nil // legacy plaintext
	}
	if len(key) == 0 {
		return "", errors.New("secret key not initialized")
	}
	raw, err := base64.StdEncoding.DecodeString(s[len(secretPrefix):]) // 两代前缀等长
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
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
