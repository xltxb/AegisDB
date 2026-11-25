package chat

import (
	"fmt"
	"strings"
	"unicode"
)

// Visible 让看不见的字符露出来:双向控制符能让语句显示成另一句话,零宽字符能藏东西。换行与制表保留,
// 零宽连接符也保留(表情和一些文字靠它们组合)。
func Visible(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\r\n", "\n")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '‌' || r == '‍':
			b.WriteRune(r)
		case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// OneLine 用于只该占一行的值:里面的换行能伪造出卡片上的下一行。
func OneLine(s string) string {
	return Visible(strings.Join(strings.Fields(strings.ToValidUTF8(s, "�")), " "))
}
