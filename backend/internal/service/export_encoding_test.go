package service

import (
	"bytes"
	"encoding/csv"
	"os"
	"strings"
	"testing"

	"velagateway/pkg/crypto"
)

// Chinese in an exported CSV opened as mojibake.
//
// The file was always correct UTF-8 — the reader guessed wrong. Excel on a
// non-English Windows does not detect UTF-8: with no byte order mark it decodes
// with the system ANSI code page (GBK on a Chinese install), and every Chinese
// value comes out as garbage. There is nothing in the file to correct, which is
// exactly why it is hard to diagnose from the operator's side.
//
// So every part starts with the mark. These tests pin both halves of that: the
// mark is at byte 0 of each part, and the content behind it is untouched UTF-8.

func writePart(t *testing.T, header []string, rows [][]string) []byte {
	t.Helper()
	p := &partWriter{dir: t.TempDir(), prefix: "enc", password: "pw", header: header}
	for _, r := range rows {
		if err := p.writeRow(append([]string(nil), r...)); err != nil {
			t.Fatalf("write row: %v", err)
		}
	}
	if err := p.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(p.files) != 1 {
		t.Fatalf("expected one part, got %d", len(p.files))
	}
	zipped, err := os.ReadFile(p.files[0])
	if err != nil {
		t.Fatalf("read part: %v", err)
	}
	out, err := crypto.ZipDecrypt(zipped, p.password)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	return out
}

func TestExportCSV_StartsWithTheByteOrderMark(t *testing.T) {
	got := writePart(t, []string{"id", "名称"}, [][]string{{"1", "订单中心"}})

	// The three bytes have to be first — before the header, before anything.
	// Anywhere else and a spreadsheet reads them as data.
	if !bytes.HasPrefix(got, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("part does not start with the UTF-8 BOM: % x", got[:min(8, len(got))])
	}
}

func TestExportCSV_KeepsChineseIntact(t *testing.T) {
	rows := [][]string{
		{"1", "订单中心", "已支付"},
		{"2", "结算网关", "退款中"},
		{"3", "履约·仓储", "已完成"},
	}
	got := writePart(t, []string{"id", "系统", "状态"}, rows)

	// Byte-level: the values are still the UTF-8 they went in as. A re-encoding
	// (to GBK, or a lossy round trip) would change these bytes while the file
	// still looked like a plausible CSV.
	for _, r := range rows {
		for _, cell := range r[1:] {
			if !bytes.Contains(got, []byte(cell)) {
				t.Errorf("value %q is not in the exported bytes as UTF-8", cell)
			}
		}
	}

	// Parse-level: still valid CSV, with the mark on the first header field —
	// which is the documented cost of carrying it (see utf8BOM).
	recs, err := csv.NewReader(bytes.NewReader(got)).ReadAll()
	if err != nil {
		t.Fatalf("exported part is not valid CSV: %v", err)
	}
	if len(recs) != len(rows)+1 {
		t.Fatalf("got %d records, want %d", len(recs), len(rows)+1)
	}
	if recs[0][0] != "\uFEFFid" {
		t.Errorf("first header field = %q, want the BOM followed by the name", recs[0][0])
	}
	if recs[0][1] != "系统" {
		t.Errorf("Chinese column name = %q, want 系统", recs[0][1])
	}
	if recs[1][1] != "订单中心" {
		t.Errorf("Chinese value = %q, want 订单中心", recs[1][1])
	}
	// Reading it the way a spreadsheet does — mark consumed, then plain UTF-8.
	trimmed := strings.TrimPrefix(string(got), "\uFEFF")
	if !strings.HasPrefix(trimmed, "id,系统,状态") {
		t.Errorf("after the mark the header should read normally, got %q", strings.SplitN(trimmed, "\n", 2)[0])
	}
}

// A rolled-over export writes several parts, each opened separately. One without
// the mark would open as mojibake while its siblings read fine — the confusing
// half-broken case.
func TestExportCSV_EveryPartCarriesTheMark(t *testing.T) {
	defer SetExportPartSize(exportPartSize)
	SetExportPartSize(1) // roll over after every row

	p := &partWriter{dir: t.TempDir(), prefix: "enc", password: "pw", header: []string{"id", "名称"}}
	for _, r := range [][]string{{"1", "订单中心"}, {"2", "结算网关"}, {"3", "履约仓储"}} {
		if err := p.writeRow(append([]string(nil), r...)); err != nil {
			t.Fatalf("write row: %v", err)
		}
	}
	if err := p.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(p.files) < 2 {
		t.Fatalf("expected the export to roll over into several parts, got %d", len(p.files))
	}
	for _, f := range p.files {
		zipped, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		out, derr := crypto.ZipDecrypt(zipped, p.password)
		if derr != nil {
			t.Fatalf("decrypt %s: %v", f, derr)
		}
		if !bytes.HasPrefix(out, []byte{0xEF, 0xBB, 0xBF}) {
			t.Errorf("part %s does not start with the UTF-8 BOM", f)
		}
	}
}
