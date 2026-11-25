package gateway

// Oracle 的 CREATE [OR REPLACE] PACKAGE / PACKAGE BODY / PROCEDURE / FUNCTION / TRIGGER / TYPE
// **编译失败也返回成功**:对象照样建出来,只是状态是 INVALID,错误记在 all_errors 里。SQL*Plus
// 会打一句 "Warning: Package Body created with compilation errors.",而驱动什么也不说 —— 网关
// 于是报"执行成功 · 0 行受影响",人回头一查包体是无效的(2026-10 生产反馈,\i PK_AGENT.pck)。
//
// 与 oracle_compile.go 的 ALTER … COMPILE 同一个立场:驱动没报错不算数,建完要回读 all_errors,
// 由它说了算。有错误就当这条语句失败:终端标红、批量 / 脚本停在这一条、审计记 warn。
//
// 只看 ATTRIBUTE = 'ERROR':PLW- 开头的编译警告不让对象失效,不该把一次成功的创建说成失败。

import (
	"context"
	"regexp"
	"strings"

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
)

// oracleUnit 是一条 CREATE 语句建出来的存储程序单元。
type oracleUnit struct {
	Owner string // 空 = 当前 schema
	Name  string
	Type  string // all_errors.type 的写法:PACKAGE BODY / PROCEDURE / …
}

const oraIdent = `"[^"]+"|[A-Za-z][A-Za-z0-9_$#]*`

var reOraCreateUnit = regexp.MustCompile(`(?is)^CREATE\s+(?:OR\s+REPLACE\s+)?(?:(?:NON)?EDITIONABLE\s+)?` +
	`(PACKAGE\s+BODY|PACKAGE|PROCEDURE|FUNCTION|TRIGGER|TYPE\s+BODY|TYPE)\s+(?:IF\s+NOT\s+EXISTS\s+)?` +
	`(` + oraIdent + `)(?:\s*\.\s*(` + oraIdent + `))?`)

// oracleCreatedUnit 认出 CREATE 语句建的是哪个单元;不是这类语句返回 false。
func oracleCreatedUnit(query string) (oracleUnit, bool) {
	s := strings.TrimSpace(query[len(leadingNoise.FindString(query)):])
	m := reOraCreateUnit.FindStringSubmatch(s)
	if m == nil {
		return oracleUnit{}, false
	}
	u := oracleUnit{Type: strings.ToUpper(strings.Join(strings.Fields(m[1]), " "))}
	if m[3] != "" {
		u.Owner, u.Name = oraIdentNorm(m[2]), oraIdentNorm(m[3])
	} else {
		u.Name = oraIdentNorm(m[2])
	}
	return u, true
}

// oraIdentNorm:带引号的原样(去引号),不带的按 Oracle 规则转大写。
func oraIdentNorm(id string) string {
	if strings.HasPrefix(id, `"`) {
		return strings.Trim(id, `"`)
	}
	return strings.ToUpper(id)
}

// maxCompileErrorsShown:报错里列出的错误条数上限,其余只给总数。
const maxCompileErrorsShown = 5

// oracleCreateErrors 回读刚建的单元的编译错误。runner 必须是执行 CREATE 的那个会话:没写 schema 的
// 单元建在当前 schema(可能被 ALTER SESSION SET CURRENT_SCHEMA 改过),只有同一会话知道是哪个。
func oracleCreateErrors(ctx context.Context, runner sqlRunner, u oracleUnit) ([]CompileDiag, error) {
	rows, err := runner.QueryContext(ctx,
		`SELECT line, position, text FROM all_errors
		  WHERE owner = NVL(:1, SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')) AND name = :2 AND type = :3
		    AND attribute = 'ERROR'
		  ORDER BY sequence`, u.Owner, u.Name, u.Type)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CompileDiag
	for rows.Next() {
		d := CompileDiag{Type: u.Type}
		if err := rows.Scan(&d.Line, &d.Position, &d.Text); err != nil {
			return nil, err
		}
		d.Text = strings.TrimSpace(d.Text)
		out = append(out, d)
	}
	return out, rows.Err()
}

// PLSQLCompileError:对象建出来了,但编译有错误(状态 INVALID)。
type PLSQLCompileError struct {
	Unit   oracleUnit
	Errors []CompileDiag
}

func (e *PLSQLCompileError) Error() string { return e.msg().S }

// MsgRef 让界面翻译这句话(apimsg);错误原文(d.Text)原样嵌在里面。
func (e *PLSQLCompileError) MsgRef() *model.RuleRef { return e.msg().Ref }

// msg 逐段拼出那句话:每段都是一句收录的格式串,前一段作为子句嵌进下一段。
func (e *PLSQLCompileError) msg() apimsg.M {
	name := e.Unit.Name
	if e.Unit.Owner != "" {
		name = e.Unit.Owner + "." + name
	}
	m := apimsg.Sprintf("%s %s 已创建,但编译有 %d 个错误(对象为 INVALID):", e.Unit.Type, name, len(e.Errors))
	for i, d := range e.Errors {
		if i == maxCompileErrorsShown {
			m = apimsg.Sprintf("%s …其余 %d 个", m, len(e.Errors)-maxCompileErrorsShown)
			break
		}
		if i > 0 {
			m = apimsg.Sprintf("%s; 第 %d 行第 %d 列 %s", m, d.Line, d.Position, d.Text)
		} else {
			m = apimsg.Sprintf("%s 第 %d 行第 %d 列 %s", m, d.Line, d.Position, d.Text)
		}
	}
	return m
}

// checkOracleCreate:query 是建存储程序的 CREATE 时回读编译错误;有错误返回 *PLSQLCompileError。
// 回读本身出错(权限、超时)不改变结论 —— 不能因为查不到错误就说它编译失败,也不该把一次真实的
// 创建报成失败;原样返回 nil,与从前的行为一致。
func checkOracleCreate(ctx context.Context, runner sqlRunner, engine, query string) error {
	if engineFamily(engine) != familyOracle {
		return nil
	}
	u, ok := oracleCreatedUnit(query)
	if !ok {
		return nil
	}
	diags, err := oracleCreateErrors(ctx, runner, u)
	if err != nil || len(diags) == 0 {
		return nil
	}
	return &PLSQLCompileError{Unit: u, Errors: diags}
}
