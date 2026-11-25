package crypto

import (
	"bytes"
	"io"

	yzip "github.com/yeka/zip"
)

// ZipEncrypt builds a password-protected ZIP archive holding a single entry
// named `entry` with the given content. It uses WinZip AES-256 encryption — the
// legacy ZipCrypto scheme is trivially broken (known-plaintext attack) and is
// unacceptable for exported production data. AES-256 zips open in 7-Zip, WinRAR,
// macOS Archive Utility, and Windows 11's built-in extractor.
func ZipEncrypt(content []byte, entry, password string) ([]byte, error) {
	var buf bytes.Buffer
	zw := yzip.NewWriter(&buf)
	w, err := zw.Encrypt(entry, password, yzip.AES256Encryption)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(content); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ZipDecrypt opens a password-protected ZIP produced by ZipEncrypt and returns
// the first entry's decompressed content (used for verification / tests).
func ZipDecrypt(zipBytes []byte, password string) ([]byte, error) {
	zr, err := yzip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, err
	}
	if len(zr.File) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	f := zr.File[0]
	if f.IsEncrypted() {
		f.SetPassword(password)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
