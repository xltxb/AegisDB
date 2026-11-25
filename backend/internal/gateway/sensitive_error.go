package gateway

import (
	"regexp"
	"strings"
)

// 报错原文里的敏感值(2026-10 安全审计 H5)。
//
// 打码管的是结果集,报错是另一条出口:对敏感列做类型转换、比较,转换失败时数据库会把那一格的
// 原值写进报错 —— PG 的 invalid input syntax for type integer: "1101…"、MySQL 严格模式下的
// Truncated incorrect DOUBLE value: '1101…'。终端、业务查询、导出都把报错原文回给用户,
// 于是一条按规则该打码的身份证号从报错里原样出去了。
//
// 做法与 MaskEvasionColumn 同一个思路:不去解析报错格式(每个引擎、每个版本都不一样),
// 只问"这条语句提没提到生效的敏感列"。提到了,报错里引号括起的片段一律抹成 '***' ——
// 值总是被引号括着出现的;代价是这一类报错里的标识符也一并看不到,而说明部分仍在。

var errQuotedRe = regexp.MustCompile(`'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"`)

// maskedError 是抹过敏感值的报错;Unwrap 保留原错误,超时 / 取消的判定(errors.Is)照常。
type maskedError struct {
	msg string
	err error
}

func (e *maskedError) Error() string { return e.msg }
func (e *maskedError) Unwrap() error { return e.err }

// maskErrorText:query 提到了 rules 里生效的敏感列时,抹掉 err 里引号括起的片段。
//
// 只对**读语句**(SELECT / WITH / VALUES / TABLE 且不写数据)做(2026-10-26 讨论 01)。从前对所有语句都做,
// 写语句、DDL、建包的编译错误里只要语句提到敏感列,表名、列名、对象名一起变成 '***'
// (`Duplicate key name '***'`、`PLS-00201: identifier '***' must be declared`),排错看不出是哪个对象。
// H5 要防的是读的时候对敏感列做转换、比较,报错把那一格的原值带出来 —— 那是读语句的事。
func maskErrorText(query string, err error, rules []SensitiveRule) error {
	if err == nil || len(rules) == 0 {
		return err
	}
	switch ParseVerb(query) {
	case "SELECT", "WITH", "VALUES", "TABLE":
		if !IsRead(query) {
			return err
		}
	default:
		return err
	}
	mentioned := false
	for _, r := range sqlReadings(query) { // 两种字面量理解都看,见 backslash.go
		if sensitiveMentioned(r, rules) != "" {
			mentioned = true
			break
		}
	}
	if !mentioned {
		return err
	}
	msg := err.Error()
	scrubbed := errQuotedRe.ReplaceAllString(msg, "'***'")
	if scrubbed == msg {
		return err
	}
	return &maskedError{msg: scrubbed, err: err}
}

// sensitiveMentioned 返回语句文本里提到的、在所查的表上生效的敏感字段名;没有则为空串。
// 字面量抹掉、标识符保留:'id_card' 只是一段文本,引号里的 id_card 才是那一列。
func sensitiveMentioned(sql string, rules []SensitiveRule) string {
	text := blankStringLiterals(StripComments(sql))
	tables := referencedTables(text)
	for _, r := range rules {
		if !mentionsColumn(text, r.Column) {
			continue
		}
		t := strings.ToLower(bareName(strings.TrimSpace(r.Table)))
		if t == "" || t == "*" || tables[t] {
			return strings.TrimSpace(r.Column)
		}
	}
	return ""
}
