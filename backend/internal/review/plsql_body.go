package review

// 看进 PL/SQL 块里。
//
// 分句器把一个块整体交回来,因为那正是 Oracle 作为一条语句执行的单元。对**执行**
// 来说这是对的;对**审查**来说恰恰相反 —— 包体里的一条 DROP 仍然是 DROP,而所有
// 检查器都锚在语句开头,合并块的开头是 BEGIN,于是它们一条都不触发:
//
//	DECLARE v NUMBER; BEGIN DELETE FROM t_orders; END;   → 审查通过
//	DELETE FROM t_orders;                                → 拦截
//
// 同一条 DELETE,包一层块就过了。高危字典没有这个问题(它对整块文本做正则扫描,
// 照样命中),但规范审查是逐语句判的,看不进去就是真的看不见。
//
// 所以审查在块上多做一步:把块体拆开,让里面的语句也各自过一遍规则。
//
// 这里过度拆分是**免费**的 —— 审查只判不跑,拆多了最多是多报一条,而漏掉的是
// 真的漏掉。这和分句器的取舍方向相反,原因也正是这个:那边拆开的东西要拿去执行。

import (
	"regexp"
	"strings"

	"velagateway/pkg/sqlutil"
)

// blockScaffolding are the words that open or close a PL/SQL construct. They sit
// in front of the real verb once the body is cut on semicolons, and stripping
// them is what lets `BEGIN DELETE FROM t` be seen as the DELETE it contains.
var blockScaffolding = map[string]bool{
	"DECLARE": true, "BEGIN": true, "END": true, "EXCEPTION": true,
	"THEN": true, "ELSE": true, "ELSIF": true, "LOOP": true, "IS": true, "AS": true,
}

// innerStatements returns the statements written INSIDE a PL/SQL block, so the
// review rules can judge them one by one.
//
// It is deliberately coarse: the body is cut on semicolons and each fragment has
// its leading block scaffolding stripped until a real SQL verb surfaces. A
// fragment that never reaches one (a variable assignment, a control keyword) is
// dropped rather than guessed at — reporting on `v := 1` would only teach people
// to ignore the review.
func innerStatements(block string) []string {
	trimmed := strings.TrimSpace(block)
	body := ""
	switch {
	case sqlutil.IsPLSQLBlock(trimmed):
		// 先切掉块头。`CREATE OR REPLACE PROCEDURE p IS BEGIN DROP TABLE t;` 按分号切,
		// 第一段是 "CREATE … BEGIN DROP TABLE t" —— 首词是 CREATE,DROP 埋在中间,规则
		// 一条都不触发。块体从第一个 BEGIN 之后开始:它前面是声明区(变量、游标),里面
		// 没有要审的语句。
		body = afterFirstBegin(trimmed)
	case strings.Contains(trimmed, ";"):
		// 分句器交回来的一条语句里居然还带分号,只有一种来路:MySQL 的 DELIMITER
		// 把分号让给了语句体。那么这一条里可能并着好几条真语句,审查同样要看进去
		// —— 否则 `DELIMITER //` 就成了绕过规则的口子:
		//
		//     DROP TABLE t;                              → 拦截
		//     DELIMITER //  SELECT 1; DROP TABLE t //     → 若不看进去,通过
		body = trimmed
	default:
		return nil
	}
	if body == "" {
		return nil // 没有 BEGIN 的块(如 CREATE TYPE)没有块体可审
	}
	var out []string
	for _, frag := range strings.Split(body, ";") {
		if s := stripScaffolding(frag); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// afterFirstBegin returns everything past the first top-level BEGIN keyword.
//
// 取第一个而不是最后一个:嵌套块里的语句也要审,而它们全都在第一个 BEGIN 之后。
var beginWordRe = regexp.MustCompile(`(?is)\bBEGIN\b`)

func afterFirstBegin(s string) string {
	loc := beginWordRe.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	return s[loc[1]:]
}

// stripScaffolding drops leading PL/SQL keywords and returns the fragment only
// when what remains starts with a real SQL verb.
func stripScaffolding(frag string) string {
	s := strings.TrimSpace(frag)
	for {
		w := leadingWord(s)
		if w == "" {
			return ""
		}
		up := strings.ToUpper(w)
		if !blockScaffolding[up] {
			// 只有落在真正的 SQL 动词上才交出去 —— 变量赋值、游标声明这些
			// 报出来只会让人学会忽略整个审查。
			if innerVerbs[up] {
				return s
			}
			return ""
		}
		s = strings.TrimSpace(s[len(w):])
	}
}

// innerVerbs are the statements worth judging when they appear inside a block.
// Data and structure changes are the whole point; the control flow around them
// is not.
var innerVerbs = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true,
	"DROP": true, "CREATE": true, "ALTER": true, "TRUNCATE": true, "RENAME": true,
	"GRANT": true, "REVOKE": true, "CALL": true, "EXECUTE": true,
}

func leadingWord(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' {
			continue
		}
		return s[:i]
	}
	return s
}
