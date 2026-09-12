package sqlutil

import (
	"regexp"
	"strings"
)

// PostgreSQL 的美元引用:`$$ … $$` 或 `$tag$ … $tag$`。
//
// 它把一整段代码当作一个字符串字面量交给服务端,而那段代码里可以有任何东西。判定层
// 和规范审查都要能看进去 —— 否则 `DO $$ BEGIN DROP TABLE t; END $$` 的首词只是 DO,
// 两边都以为这条语句什么也没做(ADR 0008 那件事的 PG 版本)。
//
// 两处都要用,所以只写一份:判定层判它实际做了什么,审查层把里面的语句逐条过规则。
var dollarTagRe = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

// DollarQuotedBody 返回第一对美元引用之间的正文;没有美元引用时返回 ""。
//
// 带标签的 `$body$ … $body$` 必须照认:它存在的理由正是「正文里也有 $$」,只认光秃秃
// 的 `$$` 等于留着一个换个写法就绕开的口子。配对按**同一个标签**找,所以
// `$$ … $x$ … $x$ … $$` 不会在内层提前收尾。
//
// 没有闭合标签时把剩下的全算作正文:这两个调用方都是"看进去找危险的东西",宁可多看
// 一段,也不要因为一个没写完的块就当它是空的。
func DollarQuotedBody(s string) string {
	loc := dollarTagRe.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	tag := s[loc[0]:loc[1]]
	rest := s[loc[1]:]
	if end := strings.Index(rest, tag); end >= 0 {
		return rest[:end]
	}
	return rest
}
