package service

// 导出成 SQL 文件(用户 2026-10 的需求:原先只有 CSV)。
//
// 与 CSV 走同一条链:同一套只读 / 判定 / 敏感导出审批、同样约 100MB 一卷、每卷一个
// AES 加密 zip、同样的行数与字节上限、终止时同样删掉已写的分卷。不同的只有写法 ——
// 结果集写成目标库可以直接执行的 INSERT(见 gateway/sqldump.go),每条 INSERT 带
// 100 行(Oracle 每行一条);单表查询在第一卷开头附上那张表的建表语句。
//
// 值要按类型写(NULL / 数字 / 时间 / 二进制),所以这条路读的是带类型的结果
// (gateway.RealQueryEachCells),不是 CSV 那条每格一个字符串的路。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/pkg/crypto"
	"aegisdb/pkg/sqlutil"
)

const (
	ExportFormatCSV = "csv"
	ExportFormatSQL = "sql"
)

var (
	ErrExportFormat         = errors.New("导出格式只能是 csv 或 sql")
	// 文件名必填(2026-10-29):列表里靠它认出是哪一份导出,下载下来的文件也以它开头。
	ErrExportNameRequired = errors.New("请填写文件名")
	ErrExportNameTooLong  = errors.New("文件名太长,最多 64 个字")
	ErrExportTargetTable    = errors.New("SQL 格式需要目标表名:无法从查询识别出唯一的源表(联表、子查询、CTE 等),请填写目标表名")
	ErrExportTargetTableBad = errors.New("目标表名不合法:只能是 表名 或 schema.表名(字母、数字、下划线)")
)

// exportTableRe is what a target table may be: name or schema.name. Kept to plain
// identifier characters: the name is written into every INSERT the file carries.
var exportTableRe = regexp.MustCompile(`^[\pL_][\pL\pN_$#]*(\.[\pL_][\pL\pN_$#]*)?$`)

// resolveExportFormat normalises the format and, for SQL, settles the target
// table: the one given, else the query's single source table.
func resolveExportFormat(query, format, target string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", ExportFormatCSV:
		return ExportFormatCSV, "", nil
	case ExportFormatSQL:
	default:
		return "", "", ErrExportFormat
	}
	target = strings.Trim(strings.TrimSpace(target), "`\"")
	if target == "" {
		target = exportSourceTable(query)
		if target == "" {
			return "", "", ErrExportTargetTable
		}
	}
	if !exportTableRe.MatchString(target) {
		return "", "", ErrExportTargetTableBad
	}
	return ExportFormatSQL, target, nil
}

// exportSourceTable returns the single table a query reads from ("" when it reads
// several, or its shape is not recognised — a subquery, a CTE).
func exportSourceTable(query string) string {
	stmts := sqlutil.SplitStatements(query)
	if len(stmts) != 1 {
		return ""
	}
	refs := sqlutil.ObjectRefs(stmts[0])
	if !refs.Recognised || len(refs.Requires) != 1 {
		return ""
	}
	r := refs.Requires[0]
	if r.Schema != "" {
		return r.Schema + "." + r.Name
	}
	return r.Name
}

// produceSQLExport is produceExport for the SQL format: same parts, password,
// limits and failure clean-up; INSERT statements instead of CSV rows.
func (s *Services) produceSQLExport(ctx context.Context, u *model.User, conn *model.Connection, job *model.ExportJob, raw bool) ([]string, string, int, int64, error) {
	query := job.SQL
	if stmts := sqlutil.SplitStatements(query); len(stmts) == 1 {
		query = stmts[0] // the single normalised statement, as produceExport does
	}
	dir := s.UserExportDir(u)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", 0, 0, err
	}
	name := strings.TrimSpace(job.Name)
	if name == "" {
		name = "export"
	}
	now := time.Now()
	w := &sqlPartWriter{
		dir:      dir,
		prefix:   sanitizeFilename(name) + "-" + now.Format("20060102-150405.000"),
		password: crypto.GenPassword(20),
		engine:   conn.Engine,
		table:    job.TargetTable,
	}
	w.preamble = s.sqlExportPreamble(conn, job, query, raw, now)
	w.ddl = s.sqlExportDDL(conn, query, job.TargetTable)

	fail := func(err error) ([]string, string, int, int64, error) {
		w.discard()
		return nil, "", 0, 0, err
	}
	maxRows, maxBytes := s.exportLimits()
	var rawBytes int64
	writeRow := func(cells []gateway.Cell) error {
		for _, c := range cells {
			rawBytes += int64(len(c.Text)) + 1
		}
		if err := exportLimitErr(w.rows+len(w.batch), rawBytes, maxRows, maxBytes); err != nil {
			return err
		}
		return w.writeRow(cells)
	}
	setHeader := func(cols []string) error { w.cols = append([]string(nil), cols...); return nil }

	if gateway.RealExecSupported(conn) {
		timeout := s.exportExecTimeout()
		read := gateway.RealQueryEachCells
		if raw {
			read = gateway.RealQueryEachCellsRaw // 已批准的敏感字段导出 —— 与 CSV 同一道旁路
		}
		if err := read(ctx, conn, query, timeout, setHeader, writeRow); err != nil {
			return fail(exportExecErr(err, w.rows, timeout))
		}
	} else {
		// 模拟连接(无凭据):与 CSV 同样合成一份结果集
		w.cols = exportColumns(query)
		// 执行层拒绝了(引擎不支持、生产不返回模拟数据):任务失败,不编一份文件出来。
		res := s.Executor.Run(context.Background(), conn, query, s.execTimeout())
		if res.Err != nil {
			return fail(res.Err)
		}
		n := res.Rows
		if n <= 0 {
			n = 200
		}
		for i := 0; i < n; i++ {
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			cells := make([]gateway.Cell, len(w.cols))
			for j, col := range w.cols {
				cells[j] = gateway.Cell{Text: exportCell(col, i)}
			}
			if err := writeRow(cells); err != nil {
				return fail(err)
			}
		}
	}
	if err := w.finish(); err != nil {
		return fail(err)
	}
	return w.files, w.password, w.rows, w.bytes, nil
}

// sqlExportPreamble is the comment block that opens every part: where the data
// came from, and what a reader importing it must know.
func (s *Services) sqlExportPreamble(conn *model.Connection, job *model.ExportJob, query string, raw bool, now time.Time) string {
	q := strings.Join(strings.Fields(query), " ")
	if r := []rune(q); len(r) > 200 {
		q = string(r[:200]) + " …"
	}
	var b strings.Builder
	// 名称里不会有换行(NormalizeBrandName 拒绝控制字符),不会把注释行断开。
	fmt.Fprintf(&b, "-- %s · 数据导出(SQL)\n", s.Brand().Text(BrandDefaultLegacy, false))
	fmt.Fprintf(&b, "-- 来源:%s · %s · 库 %s\n", job.Instance, conn.Engine, effectiveDatabase(conn))
	fmt.Fprintf(&b, "-- 查询:%s\n", q)
	fmt.Fprintf(&b, "-- 目标表:%s · 生成于 %s\n", job.TargetTable, now.Format("2006-01-02 15:04:05"))
	if !raw && len(s.SensitiveRulesForGateway()) > 0 && s.tierMasks(conn) {
		b.WriteString("-- 注意:敏感字段已按脱敏规则打码,不是原值(原值导出需审批)\n")
	}
	if gateway.IsOracleEngine(conn.Engine) {
		b.WriteString("-- 用 SQL*Plus / SQL Developer 执行时,若数据含 & 请先执行 SET DEFINE OFF\n")
	}
	// 文件是 UTF-8。导入端连接字符集若是别的(GBK 之类的多字节编码),字面量里的转义可能被
	// 吞进前一个字符,字符串提前结束 —— 数据错乱,甚至后半截被当成语句(2026-10 安全审计 L-API3)。
	switch gateway.EngineFamilyOf(conn.Engine) {
	case "mysql":
		b.WriteString("SET NAMES utf8mb4;\n")
	case "postgres":
		b.WriteString("SET client_encoding = 'UTF8';\n")
	}
	return b.String()
}

// sqlExportDDL fetches the CREATE statement for a single-table export, so the
// file can be loaded into an empty database. Only when the target IS the source
// table: renamed, the source's structure would describe a different table, and a
// join has no single structure to give. A failure to fetch leaves a note rather
// than failing the export — the data is still the point.
func (s *Services) sqlExportDDL(conn *model.Connection, query, target string) string {
	src := exportSourceTable(query)
	if src == "" || !strings.EqualFold(src, target) {
		return "-- (未附建表语句:目标表不是查询的唯一源表)\n\n"
	}
	scope, name := "", src
	if i := strings.LastIndexByte(src, '.'); i >= 0 {
		scope, name = src[:i], src[i+1:]
	} else if gateway.EngineFamilyOf(conn.Engine) == "mysql" {
		// 只有 MySQL 系按库名找表;PG / Oracle 用会话所在的 schema
		scope = effectiveDatabase(conn)
	}
	ddl, err := gateway.RealObjectSource(conn, scope, "table", name)
	if err != nil || strings.TrimSpace(ddl) == "" {
		msg := "取不到表结构"
		if err != nil {
			msg = err.Error()
		}
		return "-- (未附建表语句:" + strings.ReplaceAll(msg, "\n", " ") + ")\n\n"
	}
	ddl = strings.TrimRight(strings.TrimSpace(ddl), ";")
	return ddl + ";\n\n"
}

// sqlPartWriter writes INSERT statements into ~exportPartSize parts, each a
// password-protected zip — partWriter's twin for the SQL format. A part only ever
// ends between statements, so every part runs on its own.
type sqlPartWriter struct {
	dir, prefix, password string
	engine, table         string
	cols                  []string
	preamble, ddl         string
	buf                   bytes.Buffer
	batch                 [][]gateway.Cell
	idx, rows             int
	started               bool
	files                 []string
	bytes                 int64
}

func (w *sqlPartWriter) startPart() {
	w.idx++
	w.buf.Reset()
	w.buf.WriteString(w.preamble)
	fmt.Fprintf(&w.buf, "-- 第 %d 卷\n\n", w.idx)
	if w.idx == 1 {
		w.buf.WriteString(w.ddl) // 建表语句只在第一卷:后面的卷接着往同一张表里插
	}
	w.started = true
}

func (w *sqlPartWriter) writeRow(cells []gateway.Cell) error {
	w.batch = append(w.batch, append([]gateway.Cell(nil), cells...))
	if len(w.batch) < gateway.SQLInsertBatch(w.engine) {
		return nil
	}
	return w.emitBatch()
}

func (w *sqlPartWriter) emitBatch() error {
	if len(w.batch) == 0 {
		return nil
	}
	if !w.started {
		w.startPart()
	}
	w.buf.WriteString(gateway.InsertStatements(w.engine, w.table, w.cols, w.batch))
	w.rows += len(w.batch)
	w.batch = w.batch[:0]
	if int64(w.buf.Len()) >= exportPartSize {
		return w.flush()
	}
	return nil
}

// finish writes the trailing batch and part; an empty result still yields one
// part (preamble + DDL), as the CSV export yields a header-only file.
func (w *sqlPartWriter) finish() error {
	if err := w.emitBatch(); err != nil {
		return err
	}
	if !w.started && len(w.files) == 0 {
		w.startPart()
	}
	return w.flush()
}

func (w *sqlPartWriter) flush() error {
	if !w.started {
		return nil
	}
	entry := fmt.Sprintf("%s-part%02d.sql", w.prefix, w.idx)
	enc, err := crypto.ZipEncrypt(w.buf.Bytes(), entry, w.password)
	if err != nil {
		return err
	}
	full := filepath.Join(w.dir, fmt.Sprintf("%s-part%02d.zip", w.prefix, w.idx))
	if err := os.WriteFile(full, enc, 0o644); err != nil {
		return err
	}
	w.files = append(w.files, full)
	w.bytes += int64(len(enc))
	w.buf.Reset()
	w.started = false
	return nil
}

// discard removes every part already written — a failed job leaves no files (B3).
func (w *sqlPartWriter) discard() {
	pw := partWriter{files: w.files}
	pw.discard()
	w.files = nil
}
