package gateway

// SQL*Plus 的绑定变量:VARIABLE / PRINT / :name(sqlplus-compat 工单 06)。
//
// 变量的定义与当前值挂在**终端标签页**上(前端),每次执行随请求带过来(WithBinds);脚本里的
// VARIABLE 由 ScriptFlow 记着。执行时只绑定语句里真的出现、且声明过的那些名字:
//
//   - 值作为**驱动参数**传给 Oracle,绝不拼进 SQL 文本 —— 一个值里写着 `'; DROP TABLE t; --`
//     也只是一个字符串;
//   - PL/SQL 块(BEGIN / DECLARE / CALL)按 IN OUT 绑定,执行完把新值读回来(ExecResult.Binds),
//     `EXEC :n := 42` 之后 PRINT n 就是 42;
//   - 查询与 DML 按 IN 绑定。
//
// 判定、审计看到的都是带 `:name` 的原文 —— 值不是 SQL,不改变语句做什么。
//
// 没声明过的 `:x` 原样留着(触发器体里的 :NEW / :OLD 就是这种),CREATE 语句一律不绑定 ——
// DDL 不接受绑定,而建触发器的文本里满是 :NEW.col。
//
// REFCURSOR 暂不支持(声明时由前端说明)。

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"

	goora "github.com/sijms/go-ora/v2"

	"aegisdb/internal/model"
)

// Bind 是一个绑定变量(定义在 model 里,请求 / 响应的 dto 也用它)。
type Bind = model.BindVar

type bindsKey struct{}

// WithBinds 把这次执行可用的绑定变量挂在 ctx 上。
func WithBinds(ctx context.Context, binds []Bind) context.Context {
	if len(binds) == 0 {
		return ctx
	}
	return context.WithValue(ctx, bindsKey{}, binds)
}

func bindsFrom(ctx context.Context) []Bind {
	b, _ := ctx.Value(bindsKey{}).([]Bind)
	return b
}

// bindRef 是语句里的一处引用:哪个变量、在语句里怎么拼的。go-ora 的命名参数**区分大小写**,
// 语句里写 :n 就必须按 n 传,按 N 传它说 "parameter n is not defined"。
type bindRef struct {
	idx     int
	spelled string
}

// referencedBinds:query 里出现的、已声明的变量(按出现顺序、按拼写去重)。字符串、注释、带引号的
// 标识符里的 :x 不算;`::`(PostgreSQL 的类型转换,这里不会出现但防一手)、`a:b` 也不算。
func referencedBinds(query string, binds []Bind) []bindRef {
	if len(binds) == 0 || !strings.Contains(query, ":") {
		return nil
	}
	idx := map[string]int{}
	for i, b := range binds {
		idx[strings.ToUpper(b.Name)] = i
	}
	var out []bindRef
	seen := map[string]bool{}
	n := len(query)
	for i := 0; i < n; i++ {
		c := query[i]
		switch {
		case c == '\'' || c == '"':
			for i++; i < n && query[i] != c; i++ {
			}
		case c == '-' && i+1 < n && query[i+1] == '-':
			for i < n && query[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && query[i+1] == '*':
			if j := strings.Index(query[i+2:], "*/"); j >= 0 {
				i += j + 3
			} else {
				i = n
			}
		case c == ':':
			if i+1 < n && query[i+1] == ':' {
				i++
				continue
			}
			if i > 0 && (identByte(query[i-1]) || query[i-1] == ':') {
				continue
			}
			j := i + 1
			for j < n && identByte(query[j]) {
				j++
			}
			if j == i+1 {
				continue
			}
			name := query[i+1 : j]
			if k, ok := idx[strings.ToUpper(name)]; ok && !seen[name] {
				seen[name] = true
				out = append(out, bindRef{idx: k, spelled: name})
			}
			i = j - 1
		}
	}
	return out
}

func identByte(c byte) bool {
	return c == '_' || c == '$' || c == '#' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

var reCreateStmt = regexp.MustCompile(`(?is)^\s*CREATE\b`)

// oracleBindPlan 是一条语句要用的绑定:args 交给驱动,holders 在 PL/SQL 执行后读回新值。
type oracleBindPlan struct {
	binds   []Bind
	refs    []bindRef
	holders []sql.NullString
	plsql   bool
	// readBack:执行后读回新值(PL/SQL 块、带 RETURNING … INTO 的 DML)。普通 DML 只按 IN 传:
	// 按 IN OUT 绑定的话,驱动执行完会把缓冲区置成 NULL,刚赋的值就被一条 INSERT 冲掉了。
	readBack bool
}

var reReturningInto = regexp.MustCompile(`(?is)\bRETURN(?:ING)?\b.+\bINTO\b`)

// planOracleBinds:这条语句要不要绑定、怎么绑。没有可绑的返回 nil。
func planOracleBinds(ctx context.Context, engine, query string) *oracleBindPlan {
	if engineFamily(engine) != familyOracle || reCreateStmt.MatchString(query) {
		return nil
	}
	binds := bindsFrom(ctx)
	refs := referencedBinds(query, binds)
	if len(refs) == 0 {
		return nil
	}
	plsql := oraclePlsqlBlock(query)
	return &oracleBindPlan{binds: binds, refs: refs, plsql: plsql,
		readBack: plsql || reReturningInto.MatchString(StripComments(query))}
}

var reOraPlsqlHead = regexp.MustCompile(`(?is)^\s*(?:BEGIN|DECLARE|CALL)\b`)

func oraclePlsqlBlock(q string) bool { return reOraPlsqlHead.MatchString(StripComments(q)) }

// queryArgs:查询用的 IN 绑定。
func (p *oracleBindPlan) queryArgs() []any {
	if p == nil {
		return nil
	}
	args := make([]any, 0, len(p.refs))
	for _, r := range p.refs {
		b := p.binds[r.idx]
		var v any = b.Value
		if b.Null {
			v = nil
		}
		args = append(args, sql.Named(r.spelled, v))
	}
	return args
}

// execArgs:Exec 用的绑定。要读回的按 IN OUT,其余按 IN(见 readBack)。
func (p *oracleBindPlan) execArgs() []any {
	if p == nil {
		return nil
	}
	if !p.readBack {
		return p.queryArgs()
	}
	p.holders = make([]sql.NullString, len(p.refs))
	args := make([]any, 0, len(p.refs))
	for i, r := range p.refs {
		b := p.binds[r.idx]
		p.holders[i] = sql.NullString{String: b.Value, Valid: !b.Null}
		args = append(args, sql.Named(r.spelled, goora.Out{Dest: &p.holders[i], Size: bindSize(b.Type), In: true}))
	}
	return args
}

// updated:执行后的全部变量(读回的新值替换旧值)。
func (p *oracleBindPlan) updated() []Bind {
	out := append([]Bind(nil), p.binds...)
	for i, r := range p.refs {
		if i < len(p.holders) {
			out[r.idx].Value, out[r.idx].Null = p.holders[i].String, !p.holders[i].Valid
		}
	}
	return out
}

var reTypeLen = regexp.MustCompile(`\((\d+)`)

// bindSize:OUT 缓冲区要多大。VARCHAR2(n) 按 n 个字符、每字符最多 4 字节;CLOB 按 PL/SQL
// 变量的上限;其余(NUMBER、BINARY_DOUBLE …)转成字符串 100 字节足够。
func bindSize(typ string) int {
	t := strings.ToUpper(typ)
	if strings.Contains(t, "CLOB") {
		return 32767
	}
	if m := reTypeLen.FindStringSubmatch(t); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			if n*4 > 32767 {
				return 32767
			}
			return n * 4
		}
	}
	if strings.Contains(t, "CHAR") {
		return 4 // CHAR / VARCHAR2 不写长度时 SQL*Plus 当 1 个字符
	}
	return 100
}

var reVariable = regexp.MustCompile(`(?is)^([A-Za-z][A-Za-z0-9_$#]*)\s+(NUMBER|BINARY_FLOAT|BINARY_DOUBLE|N?CHAR(?:\s*\(\s*\d+\s*(?:CHAR|BYTE)?\s*\))?|N?VARCHAR2\s*\(\s*\d+\s*(?:CHAR|BYTE)?\s*\)|N?CLOB)\s*(?:=\s*(.*?))?\s*;?\s*$`)

// ParseVariable 解析 VARIABLE 后面的部分:name type [= value]。REFCURSOR / BLOB 等不支持的类型返回 false。
func ParseVariable(s string) (Bind, bool) {
	m := reVariable.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Bind{}, false
	}
	b := Bind{Name: strings.ToUpper(m[1]), Type: strings.ToUpper(strings.Join(strings.Fields(m[2]), "")), Null: true}
	if v := strings.TrimSpace(m[3]); v != "" {
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
		b.Value, b.Null = v, false
	}
	return b, true
}

// DeclareBind 声明(或重新声明)一个变量:同名的替换掉 —— SQL*Plus 重新声明会清空值。
func DeclareBind(binds []Bind, b Bind) []Bind {
	out := make([]Bind, 0, len(binds)+1)
	for _, x := range binds {
		if !strings.EqualFold(x.Name, b.Name) {
			out = append(out, x)
		}
	}
	return append(out, b)
}

// printBinds:PRINT name… 的结果(一行,每个变量一列;不写名字打全部)。没声明的名字返回 bad。
func printBinds(binds []Bind, names []string) (cols []string, row []string, bad string) {
	pick := binds
	if len(names) > 0 {
		pick = nil
		for _, n := range names {
			found := false
			for _, b := range binds {
				if strings.EqualFold(b.Name, n) {
					pick, found = append(pick, b), true
					break
				}
			}
			if !found {
				return nil, nil, n
			}
		}
	}
	for _, b := range pick {
		cols = append(cols, strings.ToUpper(b.Name))
		if b.Null {
			row = append(row, "")
		} else {
			row = append(row, b.Value)
		}
	}
	return cols, row, ""
}
