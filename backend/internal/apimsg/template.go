package apimsg

import (
	"strconv"
	"strings"
)

// ParseVerbs 列出格式串里的每个 fmt 动词(含标志 / 宽度 / 精度,如 "%5.1f"),%% 不算。
// 显式下标 %[n]d 与 * 宽度不支持(项目里没有),遇到返回 ok=false。
func ParseVerbs(format string) (verbs []string, ok bool) {
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		j := i + 1
		for j < len(format) && strings.IndexByte("+-# 0", format[j]) >= 0 {
			j++
		}
		for j < len(format) && (format[j] >= '0' && format[j] <= '9' || format[j] == '.') {
			j++
		}
		if j >= len(format) {
			return nil, false
		}
		switch c := format[j]; {
		case c == '%':
			if j != i+1 {
				return nil, false
			}
		case c == '*' || c == '[':
			return nil, false
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			verbs = append(verbs, format[i:j+1])
		default:
			return nil, false
		}
		i = j
	}
	return verbs, true
}

// intlify 消息语法里有含义的字符:在词条里要写成字面量插值 {'@'}。
// < > 是因为构建期 strictMessage 不许词条里出现像 HTML 的东西。
const special = "{}@|<>%$"

// Template 把一条文案转成 intlify 词条:格式串的动词依次变成 {0} {1} …,其余字符原样,
// 特殊字符转义。前端用 t(key, [参数…]) 渲染,结果与 Go 端 fmt 出来的中文一致。
func Template(e Entry) string {
	var b strings.Builder
	lit := func(s string) {
		for _, r := range s {
			if r < 128 && strings.IndexRune(special, r) >= 0 {
				b.WriteString("{'" + string(r) + "'}")
			} else {
				b.WriteRune(r)
			}
		}
	}
	if !e.Fmt {
		lit(e.Zh)
		return b.String()
	}
	n := 0
	s := e.Zh
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			lit(s)
			return b.String()
		}
		lit(s[:i])
		j := i + 1
		for strings.IndexByte("+-# 0123456789.", s[j]) >= 0 {
			j++
		}
		if s[j] == '%' {
			lit("%")
		} else {
			b.WriteString("{" + strconv.Itoa(n) + "}")
			n++
		}
		s = s[j+1:]
	}
}

// Placeholders 是一条文案的参数个数。
func Placeholders(e Entry) int {
	if !e.Fmt {
		return 0
	}
	v, _ := ParseVerbs(e.Zh)
	return len(v)
}
