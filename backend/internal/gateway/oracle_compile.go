package gateway

// Oracle 存储程序的重新编译。
//
// 一个包在被依赖对象改动后会变成 INVALID —— 表加了列、被引用的过程重建了,
// 包体就失效了。Oracle 会在下次调用时隐式重编译,但那意味着编译错误在业务
// 请求里才第一次出现;DBA 要的是现在就编、现在就看到错误。
//
// 这个功能最容易撒谎的地方:`ALTER … COMPILE` **即使编译失败也返回成功**,
// 对象只是被留成 INVALID。以驱动没报错为准显示"编译成功",就是在骗人 ——
// 所以编译之后必须回读 all_objects.status 与 all_errors,由它们说了算。

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"velagateway/internal/model"
)

// CompileTarget is one compiled unit and what Oracle thinks of it afterwards.
type CompileTarget struct {
	Type   string `json:"type"`   // PACKAGE / PACKAGE BODY / PROCEDURE / …
	Status string `json:"status"` // VALID / INVALID / (缺失时为 MISSING)
}

// CompileDiag is one row of all_errors — the line and column a DBA needs.
type CompileDiag struct {
	Type     string `json:"type"`
	Line     int    `json:"line"`
	Position int    `json:"position"`
	Text     string `json:"text"`
}

// CompileReport is the whole outcome of a compile request.
type CompileReport struct {
	Object   string          `json:"object"`
	Kind     string          `json:"kind"` // package / procedure / …
	Targets  []CompileTarget `json:"targets"`
	Errors   []CompileDiag   `json:"errors"`
	Ms       int             `json:"ms"`
	Warnings []string        `json:"warnings,omitempty"`
}

// OK reports whether the object is genuinely usable now: every compiled unit
// VALID and no diagnostics. An empty report is NOT success — nothing was proven.
func (r *CompileReport) OK() bool {
	if r == nil || len(r.Targets) == 0 || len(r.Errors) > 0 {
		return false
	}
	for _, t := range r.Targets {
		if !strings.EqualFold(t.Status, "VALID") {
			return false
		}
	}
	return true
}

// oracleIdentRe is what may be pasted into a compile statement. Oracle cannot
// bind an identifier, so the name is concatenated into the SQL text — the only
// thing standing between the object browser and an injection is this pattern
// plus the double quotes around it. Quoted-with-special-characters identifiers
// are refused rather than escaped: refusing a rare name beats getting the
// escaping subtly wrong.
var oracleIdentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#]{0,127}$`)

func oracleQuoteIdent(what, s string) (string, error) {
	s = strings.TrimSpace(s)
	if !oracleIdentRe.MatchString(s) {
		return "", fmt.Errorf("%s %q 不是合法的 Oracle 标识符,拒绝拼入编译语句", what, s)
	}
	return `"` + s + `"`, nil
}

// oracleCompileSQL builds the ALTER … COMPILE statements for one object.
//
// A package is TWO units: compiling the spec invalidates the body, so
// "compile this package" that only did the spec would leave the object worse
// than it found it.
func oracleCompileSQL(owner, kind, name string) ([]string, error) {
	qname, err := oracleQuoteIdent("对象名", name)
	if err != nil {
		return nil, err
	}
	if o := strings.TrimSpace(owner); o != "" {
		qowner, oerr := oracleQuoteIdent("schema", o)
		if oerr != nil {
			return nil, oerr
		}
		qname = qowner + "." + qname
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "package":
		return []string{
			"ALTER PACKAGE " + qname + " COMPILE PACKAGE",
			"ALTER PACKAGE " + qname + " COMPILE BODY",
		}, nil
	case "type":
		return []string{
			"ALTER TYPE " + qname + " COMPILE TYPE",
			"ALTER TYPE " + qname + " COMPILE BODY",
		}, nil
	case "procedure":
		return []string{"ALTER PROCEDURE " + qname + " COMPILE"}, nil
	case "function":
		return []string{"ALTER FUNCTION " + qname + " COMPILE"}, nil
	case "trigger":
		return []string{"ALTER TRIGGER " + qname + " COMPILE"}, nil
	}
	return nil, fmt.Errorf("对象类型 %q 不可编译(只有包 / 过程 / 函数 / 触发器 / 类型有编译单元)", kind)
}

// IsOracleEngine reports whether an engine label speaks Oracle — compilation is
// the one capability here with no equivalent elsewhere, so callers check it
// before anything else and say so specifically.
func IsOracleEngine(engine string) bool { return engineFamily(engine) == familyOracle }

// TargetDatabaseSwitchable reports whether a per-request "target database" may
// replace Connection.Database.
//
// For every other engine that field names a namespace you can switch to. For
// ORACLE it names the SERVICE (or sid/…) — a connection coordinate, not a
// namespace: Oracle qualifies objects by OWNER instead. Substituting an owner
// there does not select a schema, it asks the listener for a service by that
// name, which is how browsing the object tree produced
//
//	(CONNECT_DATA=(SERVICE_NAME=G04)) … TNS-12514
//
// against an instance whose service is g04_h01.
func TargetDatabaseSwitchable(engine string) bool { return !IsOracleEngine(engine) }

// OracleCompileStatements exposes the statements a compile would run, so the
// service layer can judge EXACTLY what it is about to execute — the judged text
// and the executed text must be the same bytes, not two constructions of it.
func OracleCompileStatements(owner, kind, name string) ([]string, error) {
	return oracleCompileSQL(owner, kind, name)
}

// oracleUnitTypes are the all_objects.object_type values one compile touches.
func oracleUnitTypes(kind string) []string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "package":
		return []string{"PACKAGE", "PACKAGE BODY"}
	case "type":
		return []string{"TYPE", "TYPE BODY"}
	case "procedure":
		return []string{"PROCEDURE"}
	case "function":
		return []string{"FUNCTION"}
	case "trigger":
		return []string{"TRIGGER"}
	}
	return nil
}

// RealCompileObject recompiles a stored program and then asks Oracle what it
// actually thinks of it. Only the second half is trustworthy — see the file
// comment on why ALTER … COMPILE returning nil proves nothing.
func RealCompileObject(conn *model.Connection, owner, kind, name string) (*CompileReport, error) {
	if engineFamily(conn.Engine) != familyOracle {
		return nil, fmt.Errorf("包编译是 Oracle 专有能力,当前实例引擎为 %s", conn.Engine)
	}
	stmts, err := oracleCompileSQL(owner, kind, name)
	if err != nil {
		return nil, err
	}
	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	rep := &CompileReport{Object: strings.TrimSpace(name), Kind: strings.ToLower(strings.TrimSpace(kind))}
	start := time.Now()
	for _, st := range stmts {
		// A missing body (spec-only package) makes its COMPILE BODY fail; that is
		// information, not a reason to abandon the whole request — the spec's own
		// result still matters. Collected as a warning and judged by status below.
		if _, execErr := db.ExecContext(ctx, st); execErr != nil {
			rep.Warnings = append(rep.Warnings, strings.TrimSpace(execErr.Error()))
		}
	}
	rep.Ms = int(time.Since(start).Milliseconds())

	// The owner used for the catalogue lookups: an empty owner means "current
	// schema", which all_objects cannot express — resolve it once.
	lookupOwner := strings.ToUpper(strings.TrimSpace(owner))
	if lookupOwner == "" {
		if err := db.QueryRowContext(ctx, `SELECT USER FROM dual`).Scan(&lookupOwner); err != nil {
			return nil, fmt.Errorf("无法确定当前 schema: %w", err)
		}
	}
	upperName := strings.ToUpper(strings.TrimSpace(name))

	for _, ut := range oracleUnitTypes(kind) {
		var status string
		err := db.QueryRowContext(ctx,
			`SELECT status FROM all_objects WHERE owner = :1 AND object_name = :2 AND object_type = :3`,
			lookupOwner, upperName, ut).Scan(&status)
		if err != nil {
			// A package with no body is normal; report it as absent rather than
			// inventing a status for a unit that does not exist.
			continue
		}
		rep.Targets = append(rep.Targets, CompileTarget{Type: ut, Status: strings.ToUpper(status)})
	}

	rows, err := db.QueryContext(ctx,
		`SELECT type, line, position, text FROM all_errors
		 WHERE owner = :1 AND name = :2 ORDER BY sequence`, lookupOwner, upperName)
	if err != nil {
		return nil, fmt.Errorf("读取编译错误失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d CompileDiag
		if err := rows.Scan(&d.Type, &d.Line, &d.Position, &d.Text); err != nil {
			return nil, err
		}
		d.Text = strings.TrimSpace(d.Text)
		rep.Errors = append(rep.Errors, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(rep.Targets) == 0 {
		return nil, fmt.Errorf("在 schema %s 下找不到 %s %s", lookupOwner, strings.ToUpper(kind), upperName)
	}
	return rep, nil
}
