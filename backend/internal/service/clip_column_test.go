package service

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipColumn_NeverExceedsWidth(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "abc"},
		{"abcd", "abcd"},  // 正好等于列宽:不截
		{"abcde", "abc…"}, // 超一个字符:省略号也算进列宽
		{"中文中文中", "中文中…"}, // 按字符算,不按字节
	}
	for _, c := range cases {
		got := clipColumn(c.in, 4)
		if got != c.want {
			t.Errorf("clipColumn(%q, 4) = %q,应为 %q", c.in, got, c.want)
		}
		if utf8.RuneCountInString(got) > 4 {
			t.Errorf("clipColumn(%q, 4) = %q 超出列宽", c.in, got)
		}
	}
	// 线上的样子:ENUM 的 column_type 超过 tbl_meta_column.data_type 的 128。
	long := "enum('" + strings.Repeat("a", 200) + "')"
	if n := utf8.RuneCountInString(clipColumn(long, 128)); n != 128 {
		t.Errorf("应正好 128 个字符,实际 %d", n)
	}
	// 展示用的 clip 不变:仍然是 n 个字符加省略号。
	if got := clip("abcde", 4); got != "abcd…" {
		t.Errorf("clip 不该变,实际 %q", got)
	}
}

// clip 的宽度正好是常见列宽时,基本都是在往这个宽度的列里写,要用 clipColumn。
// 用错了测试不一定发现(要正好写到超宽的值),只有上线才报错。
func TestExactColumnWidthsUseClipColumn(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	bare := regexp.MustCompile(`[^a-zA-Z_.]clip\(.*,\s*(64|128|255|512)\)`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range bare.FindAll(b, -1) {
			t.Errorf("%s: %s 可能超出列宽,应改用 clipColumn", f, m)
		}
	}
}
