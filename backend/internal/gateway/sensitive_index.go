package gateway

import (
	"strings"
	"unicode/utf8"
)

// sensitiveIndex 是一份规则在一次查询里的查找表。
//
// 打码要回答"这一列 / 这一项表达式碰没碰敏感字段",朴素做法是 列 × 规则 两层循环,
// 规则每多一条,每条查询都多扫一遍选择列表。这里把规则按"小写字段名"建成哈希表,
// 再把每个选择项一次性切成标识符,逐个查表 —— 耗时只跟 SQL 长度有关,跟规则条数无关
// (建表本身是规则条数的一次线性遍历,远小于原来的乘积)。
//
// 等价性:规则字段名若本身全是标识符字符(绝大多数),"在文本里出现且两侧都不是标识符
// 字符"就恰好等于"它是文本里的一个完整标识符",查表与 mentionsColumn 的结论完全一致。
// 字段名里夹着别的字符(空格、点、连字符)的少数规则查不了表,仍逐条走 mentionsColumn。
type sensitiveIndex struct {
	rules []SensitiveRule
	col   []string // 每条规则的小写字段名(TrimSpace 后)
	table []string // 每条规则的小写表名(TrimSpace 后,不去限定符 —— 与原判定口径一致)
	style map[string]string
}

func newSensitiveIndex(rules []SensitiveRule) *sensitiveIndex {
	ix := &sensitiveIndex{
		rules: rules,
		col:   make([]string, len(rules)),
		table: make([]string, len(rules)),
		style: make(map[string]string, len(rules)),
	}
	for i, r := range rules {
		c := strings.ToLower(strings.TrimSpace(r.Column))
		ix.col[i] = c
		ix.table[i] = strings.ToLower(strings.TrimSpace(r.Table))
		// 同一字段配了多条规则时,第一条写了样式的为准(与原 styleFor 一致)
		if _, ok := ix.style[c]; !ok && r.Style != "" {
			ix.style[c] = r.Style
		}
	}
	return ix
}

// activeCols 是这条查询上生效的规则字段:不限表的,或者限定的表在查询里出现了。
// whole 用于按列名比对;ident 是可以按标识符查表的字段,odd 是只能逐条扫的少数。
type activeCols struct {
	whole map[string]bool
	ident map[string]bool
	odd   []string
}

func (ix *sensitiveIndex) active(tables map[string]bool) activeCols {
	a := activeCols{whole: map[string]bool{}, ident: map[string]bool{}}
	for i, c := range ix.col {
		if t := ix.table[i]; !(t == "" || t == "*" || tables[t]) {
			continue
		}
		if a.whole[c] {
			continue
		}
		a.whole[c] = true
		switch {
		case c == "":
			// 空字段名:mentionsColumn 永远不命中,只参与按列名比对
		case isIdentWord(c):
			a.ident[c] = true
		default:
			a.odd = append(a.odd, c)
		}
	}
	return a
}

func (a activeCols) empty() bool { return len(a.whole) == 0 }

// styleFor:字段配置的打码样式,没配的一律 partial。
func (ix *sensitiveIndex) styleFor(col string) string {
	if s, ok := ix.style[strings.ToLower(strings.TrimSpace(col))]; ok {
		return s
	}
	return MaskPartial
}

// isIdentWord:整串都是标识符字符(口径同 isIdentRune)。
func isIdentWord(s string) bool {
	for _, r := range s {
		if !isIdentRune(r) {
			return false
		}
	}
	return s != ""
}

// eachIdent 按出现顺序交出 s 里每个极长的标识符片段;f 返回 false 即停。
func eachIdent(s string, f func(string) bool) {
	start := -1
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if isIdentRune(r) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			if !f(s[start:i]) {
				return
			}
			start = -1
		}
		i += n
	}
	if start >= 0 {
		f(s[start:])
	}
}

// eachMentioned calls f with every active rule field the item computes from. It
// reaches the same verdict as calling mentionsColumn once per active rule, without
// slowing down as rules are added, and names the fields so the caller can pick the
// strictest style among them (see sensitiveMaskPlan).
func (a activeCols) eachMentioned(item string, f func(col string)) {
	if len(a.ident) > 0 {
		eachIdent(strings.ToLower(item), func(w string) bool {
			if a.ident[w] {
				f(w)
			}
			return true
		})
	}
	for _, c := range a.odd {
		if mentionsColumn(item, c) {
			f(c)
		}
	}
}
