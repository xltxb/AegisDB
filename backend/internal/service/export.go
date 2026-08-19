package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/pkg/crypto"
	"velagateway/pkg/sqlutil"
)

// exportSQLReadOnly reports whether an export query is a single read-only
// (SELECT-class) statement. It is the gateway-bypass guard for exports: the
// worker executes this SQL against the target DB, so anything that could mutate
// data — a write/DDL/GRANT verb, or a stacked second statement — must be rejected
// so exports cannot be used to run privileged SQL the risk engine would gate.
func exportSQLReadOnly(sql string) bool {
	stmts := sqlutil.SplitStatements(sql)
	if len(stmts) != 1 {
		return false // empty, or stacked queries (e.g. "SELECT 1; DROP TABLE x")
	}
	if writesFileRe.MatchString(gateway.StripComments(stmts[0])) {
		return false // see writesFileRe
	}
	return gateway.MapVerbToCapability(gateway.ParseVerb(stmts[0])) == "select"
}

// writesFileRe matches the MySQL clauses that make a SELECT write a file on the
// database server. They keep the leading verb SELECT, so the verb whitelist
// classifies them as read-only while the statement is really a write primitive
// (EX2) — the check has to be explicit. Comments are stripped first so the
// clause cannot be broken up by an executable comment.
var writesFileRe = regexp.MustCompile(`(?is)\binto\s+(outfile|dumpfile)\b`)

// exportPartSize is the (uncompressed) CSV size at which the export rolls over
// to a new part file — large results split into parts (bounded overall by the
// row/byte caps below).
var exportPartSize int64 = 100 << 20 // 100 MiB

// SetExportPartSize overrides the CSV part-size rollover threshold (tests/config).
func SetExportPartSize(n int64) {
	if n > 0 {
		exportPartSize = n
	}
}

// exportMaxRows caps a single export job's row count so one unbounded query
// (e.g. a cartesian product) can't fill the disk / exhaust memory (M8). 0 = no
// limit. Default 5,000,000 rows — the DEFAULT only: the effective cap is the
// `export.maxRows` setting (系统设置·网关), resolved per job in exportLimits so
// an admin can raise it for a legitimately large export without a rebuild.
var exportMaxRows int64 = 5_000_000

// SetExportMaxRows overrides the default per-job export row cap (tests).
func SetExportMaxRows(n int64) { exportMaxRows = n }

// exportMaxBytes caps a job's cumulative raw (uncompressed) CSV content. The row
// cap alone doesn't bound wide-column results (large BLOB/TEXT), so a few
// thousand fat rows could still exhaust memory/disk (B2). 0 = no limit. ~2 GiB
// default; effective cap is the `export.maxBytes` setting (see exportLimits).
var exportMaxBytes int64 = 2_000_000_000

// SetExportMaxBytes overrides the default per-job export byte cap (tests).
func SetExportMaxBytes(n int64) { exportMaxBytes = n }

// exportLimits resolves the effective per-job caps: the export.maxRows /
// export.maxBytes settings when set, else the built-in defaults. Negative
// values are nonsense and fall back to the default; 0 disables the cap.
func (s *Services) exportLimits() (maxRows, maxBytes int64) {
	maxRows = int64(s.settingInt("export.maxRows", int(exportMaxRows)))
	if maxRows < 0 {
		maxRows = exportMaxRows
	}
	maxBytes = int64(s.settingInt("export.maxBytes", int(exportMaxBytes)))
	if maxBytes < 0 {
		maxBytes = exportMaxBytes
	}
	return maxRows, maxBytes
}

// exportExecTimeout is the whole-job execution budget for a real-DB export,
// configurable via the export.execTimeout setting (seconds). Default 30min —
// bounded below at 1s so a typo can't make every export fail instantly.
func (s *Services) exportExecTimeout() time.Duration {
	sec := s.settingInt("export.execTimeout", 1800)
	if sec < 1 {
		sec = 1800
	}
	return time.Duration(sec) * time.Second
}

// exportExecErr translates a streaming-export failure into something the
// operator can act on. Two cases matter:
//   - our own deadline fired (RealQueryEach rewraps it so errors.Is works even
//     when the driver surfaced the killed connection as a transport error);
//   - the TARGET side dropped the TCP connection mid-stream, which the MySQL
//     driver reports as a bare "unexpected EOF" — accurate but useless without
//     the likely causes (server net_write_timeout/wait_timeout, a proxy idle
//     cutoff, or the statement being killed) and how far the export got.
func exportExecErr(err error, rows int, timeout time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("导出执行超时(%d分钟),已读取 %d 行 — 可在 系统设置·网关 调大导出执行超时,或分批导出", int(timeout.Minutes()), rows)
	}
	msg := err.Error()
	if strings.Contains(msg, "unexpected EOF") || strings.Contains(msg, "invalid connection") ||
		strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") {
		return fmt.Errorf("目标数据库在导出中途断开连接(已读取 %d 行): %v — 常见原因是服务端 net_write_timeout/wait_timeout 过小、中间代理超时或语句被终止,请调大目标库超时参数或分批导出", rows, err)
	}
	return err
}

// exportLimitErr reports a breach of the row or byte cap given a job's running
// totals (rows already written, cumulative raw bytes). Checked before writing
// each row so the job fails fast with a clear message instead of after OOM.
func exportLimitErr(rows int, rawBytes, maxRows, maxBytes int64) error {
	if maxRows > 0 && int64(rows) >= maxRows {
		return fmt.Errorf("导出超过行数上限 %d 行,请缩小查询范围或分批导出(上限可在 系统设置·网关 调整)", maxRows)
	}
	if maxBytes > 0 && rawBytes > maxBytes {
		return fmt.Errorf("导出超过数据量上限 %d 字节,请缩小查询范围或分批导出(上限可在 系统设置·网关 调整)", maxBytes)
	}
	return nil
}

// DefaultExportDir is the built-in export directory (relative to the backend's
// working directory) used when no export.savePath is configured.
const DefaultExportDir = "export"

// maxStoredSQLBytes bounds any user-submitted SQL the gateway persists on a job
// row (export/async). The columns are MEDIUMTEXT — 16MB, migration 0018 — and
// letting the database refuse the INSERT surfaced as a raw driver error the
// operator can't act on ("Error 1406: Data too long for column 'sql'").
const maxStoredSQLBytes = 15 << 20

// ErrSQLTooLong marks a stored-SQL bound refusal so handlers can surface the
// wrapped message instead of collapsing it into a generic error.
var ErrSQLTooLong = errors.New("sql too long")

// storedSQLTooLong is the actionable refusal for SQL past maxStoredSQLBytes.
func storedSQLTooLong(n int) error {
	return fmt.Errorf("SQL 过长(%.1fMB,上限 15MB)——请改用脚本上传通道,或缩短语句(如用临时表替代超长 IN 列表): %w", float64(n)/(1<<20), ErrSQLTooLong)
}

// ExportSavePath returns the data-export directory: the configured
// export.savePath, or the default "export" dir under the backend run dir.
func (s *Services) ExportSavePath() string {
	if p := strings.TrimSpace(s.settingString("export.savePath", "")); p != "" {
		return p
	}
	return DefaultExportDir
}

// UserExportDir returns a user's export directory: <exportPath>/<username>.
func (s *Services) UserExportDir(u *model.User) string {
	return filepath.Join(s.ExportSavePath(), userDirName(u))
}

// EnqueueExport creates an asynchronous export job and returns immediately. The
// job is processed by the worker pool (see service.New) so multiple exports run
// in parallel; poll ListExportJobs for status and the finished download.
func (s *Services) EnqueueExport(u *model.User, connID int64, sql, name, database string) (*model.ExportJob, error) {
	if s.ExportSavePath() == "" {
		return nil, ErrExportPathUnset
	}
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	// SECURITY (gateway bypass): the export worker runs this SQL against the target
	// DB via a query path that still EXECUTES mutations. Without this guard a
	// terminal user (even a read-only role) could smuggle DELETE/UPDATE/DROP/… past
	// the capability matrix and risk dictionary by submitting it as an "export".
	// Exports are data reads by definition, so require a single read-only statement.
	if !exportSQLReadOnly(sql) {
		return nil, ErrExportNotReadOnly
	}
	if len(sql) > maxStoredSQLBytes {
		return nil, storedSQLTooLong(len(sql))
	}
	// FR-CONN-04: maintenance-state instances restrict operations. The worker
	// opens a real connection, so the export channel has to honour this too.
	if conn.Status == "maint" {
		return nil, ErrForbidden
	}
	// The worker runs this SQL against the target DB, so an export is an
	// EXECUTION channel and carries the same gate as the terminal. Being
	// read-only is not sufficient authorisation: a role explicitly denied
	// `select` on an environment was still able to pull whole tables out through
	// /export while /terminal/exec refused the identical statement (EX1).
	// Anything the engine does not outright allow is refused — an export has no
	// approval flow to route an `approve` verdict into, so the user is sent to
	// the terminal for that.
	tier, err := s.tierCodeOf(conn)
	if err != nil {
		return nil, ErrBadRequest // unresolvable tier — see tierOf; not judged as allow
	}
	if v := s.Engine.EvaluateFor(s.Repo.EffectiveRoleIDs(u), conn.Engine, tier, sql); v.Action != gateway.ActionAllow {
		s.recordAudit(u, conn, sql, v.Risk, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden
	}
	// Fail fast with a clear message when neither a target database was chosen nor
	// the connection carries a default one — otherwise an unqualified query fails
	// async with a cryptic driver error ("No database selected"). Real execution
	// only; a simulated (credential-less) connection doesn't touch a real schema.
	db := strings.TrimSpace(database)
	if db == "" && strings.TrimSpace(conn.Database) == "" && gateway.RealExecSupported(conn) {
		return nil, ErrNoDatabase
	}
	job := &model.ExportJob{
		UserID: u.ID, ConnectionID: connID, Instance: conn.Env + "-" + conn.Name,
		Database: db,
		SQL:      sql, Name: strings.TrimSpace(name), Status: model.ExportPending,
	}
	if err := s.Repo.CreateExportJob(job); err != nil {
		return nil, err
	}
	// The SUBMISSION is the auditable event — it is the moment someone asked for
	// the data, and it is the only point guaranteed to be reached (a job can fail,
	// be queued away or be reconciled by a restart). Auditing only completions
	// meant a probing loop of failing exports left no trace at all (EX5). The full
	// query is recorded, not a clip: a truncated query cannot show what ran.
	s.recordAudit(u, conn, "EXPORT "+sql, model.RiskLow, model.ResultPending, "", "exec")
	// Non-blocking enqueue: if the buffered queue is full, fail fast instead of
	// leaking a goroutine that blocks until a slot frees (L8).
	select {
	case s.exportQueue <- job.ID:
	default:
		s.failExport(job.ID, "导出队列已满,请稍后重试")
	}
	return job, nil
}

// ListExportJobs returns a user's export jobs (newest first).
func (s *Services) ListExportJobs(u *model.User, limit int) []model.ExportJob {
	if u == nil {
		return []model.ExportJob{}
	}
	js, _ := s.Repo.ListExportJobs(u.ID, limit)
	if js == nil {
		js = []model.ExportJob{}
	}
	// Decrypt the at-rest archive password for display (R25). Legacy plaintext
	// rows pass through unchanged (DecryptSecret returns non-prefixed values as-is).
	// On failure (e.g. the app secret was rotated) show a placeholder instead of
	// leaking the ciphertext as if it were the password (R-verify).
	for i := range js {
		if js[i].Password == "" {
			continue
		}
		if pw, err := crypto.DecryptSecret(js[i].Password); err == nil {
			js[i].Password = pw
		} else {
			js[i].Password = "(口令不可用·密钥已轮换)"
		}
	}
	return js
}

// runExportJob performs one export (invoked by a worker goroutine).
func (s *Services) runExportJob(id int64) {
	// Only run a still-pending job. A job that was already failed/done/cancelled —
	// e.g. reconciled after a restart, or double-enqueued — must NOT be scheduled
	// again; the atomic claim also stops two workers running the same job.
	if claimed, err := s.Repo.ClaimExportJob(id); err != nil || !claimed {
		return
	}
	job, err := s.Repo.GetExportJob(id)
	if err != nil {
		return
	}
	conn, err := s.Repo.GetConnection(job.ConnectionID)
	if err != nil {
		s.failExport(id, "连接不存在")
		return
	}
	// Target the database chosen at submit time (the job persists it so the async
	// worker runs against the same schema the user selected).
	if job.Database != "" {
		conn.Database = job.Database
	}
	u, err := s.Repo.GetUserByID(job.UserID)
	if err != nil {
		s.failExport(id, "用户不存在")
		return
	}
	// Defense in depth: never run a non read-only export, even if the row was
	// crafted to skip the enqueue-time guard.
	if !exportSQLReadOnly(job.SQL) {
		s.failExport(id, "导出仅允许单条只读查询语句")
		return
	}
	// simulate real export latency so parallel jobs are observable in the UI
	time.Sleep(time.Duration(700+rand.Intn(1600)) * time.Millisecond)
	files, password, rows, size, err := s.produceExport(u, conn, job.SQL, job.Name)
	if err != nil {
		s.failExport(id, err.Error())
		return
	}
	now := time.Now()
	// Encrypt the archive password at rest so a DB dump doesn't hand over both the
	// AES-encrypted export and its password (R25). Decrypted in ListExportJobs.
	encPw, encErr := crypto.EncryptSecret(password)
	if encErr != nil {
		s.failExport(id, "加密导出口令失败: "+encErr.Error())
		return
	}
	// Don't swallow the completion write: if it fails (e.g. a too-narrow column on
	// MySQL), the job would otherwise be stuck "running" forever. Surface it as a
	// failed job with the reason instead.
	if uerr := s.Repo.UpdateExportJob(id, map[string]any{
		"status": model.ExportDone, "files": strings.Join(files, "\n"), "parts": len(files),
		"password": encPw, "rows": rows, "bytes": size, "finished_at": now,
	}); uerr != nil {
		slog.Error("export job completed but marking it done failed", "id", id, "err", uerr)
		s.failExport(id, "导出已生成但写回状态失败: "+uerr.Error())
		return
	}
	s.recordAudit(u, conn, "EXPORT "+job.SQL, model.RiskLow, model.ResultExecuted, "", "exec")
}

func (s *Services) failExport(id int64, msg string) {
	now := time.Now()
	// The error column is VARCHAR(255); clip so marking the job failed can't itself
	// fail (which would leave it stuck "running").
	if uerr := s.Repo.UpdateExportJob(id, map[string]any{
		"status": model.ExportFailed, "error": clip(msg, 250), "finished_at": now,
	}); uerr != nil {
		slog.Error("failed to mark export job failed", "id", id, "err", uerr)
	}
}

// produceExport streams the result set into rolling ~100MB CSV parts, each
// gzip-compressed + AES-256-GCM encrypted with one shared password, archived
// under <exportPath>/<username>/<name>-<timestamp>-part<n>.csv.gz.enc.
// Returns the list of part files (all download-able), the password, total rows,
// and total encrypted size, bounded by the export.maxRows/maxBytes caps.
// On ANY failure the parts already flushed to disk are deleted: a failed job
// records no files, so they would be neither downloadable (ownsExportFile
// matches recorded files only) nor ever cleaned up — a big export failing at
// the cap otherwise stranded gigabytes of undownloadable archives (B3).
func (s *Services) produceExport(u *model.User, conn *model.Connection, sql, name string) ([]string, string, int, int64, error) {
	now := time.Now()
	dir := s.UserExportDir(u)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", 0, 0, err
	}
	if strings.TrimSpace(name) == "" {
		name = "export"
	}
	pw := &partWriter{
		dir:      dir,
		prefix:   sanitizeFilename(name) + "-" + now.Format("20060102-150405.000"), // ms avoids same-second collisions
		password: crypto.GenPassword(20),
	}
	setHeader := func(cols []string) error { pw.header = cols; return nil }

	// A failed job leaves no files behind — see the doc comment (B3).
	fail := func(err error) ([]string, string, int, int64, error) {
		pw.discard()
		return nil, "", 0, 0, err
	}

	// Enforce the per-job row AND byte caps: reject before writing the row that
	// would exceed a limit, failing the job with a clear message (M8/B2).
	maxRows, maxBytes := s.exportLimits()
	var rawBytes int64
	writeRow := func(rec []string) error {
		for _, c := range rec {
			rawBytes += int64(len(c)) + 1 // +1 approximates the field separator
		}
		if err := exportLimitErr(pw.rows, rawBytes, maxRows, maxBytes); err != nil {
			return err
		}
		return pw.writeRow(rec)
	}

	if gateway.RealExecSupported(conn) {
		timeout := s.exportExecTimeout()
		if err := gateway.RealQueryEach(conn, sql, timeout, setHeader, writeRow); err != nil {
			return fail(exportExecErr(err, pw.rows, timeout)) // translated — see exportExecErr
		}
	} else {
		// simulated demo data (no real target): synthesize a result set
		cols := exportColumns(sql)
		pw.header = cols
		n := s.Executor.Run(conn, sql, s.execTimeout()).Rows
		if n <= 0 {
			n = 200
		}
		rec := make([]string, len(cols))
		for i := 0; i < n; i++ {
			for j, col := range cols {
				rec[j] = exportCell(col, i)
			}
			if err := writeRow(rec); err != nil {
				return fail(err)
			}
		}
	}
	if err := pw.flush(); err != nil { // flush the trailing partial part
		return fail(err)
	}
	if len(pw.files) == 0 { // empty result → still emit a header-only file
		if err := pw.startPart(); err != nil {
			return fail(err)
		}
		if err := pw.flush(); err != nil {
			return fail(err)
		}
	}
	return pw.files, pw.password, pw.rows, pw.bytes, nil
}

// partWriter streams CSV rows into ~exportPartSize files, each gzip+encrypted.
type partWriter struct {
	dir, prefix, password string
	header                []string
	buf                   bytes.Buffer
	w                     *csv.Writer
	idx                   int
	started               bool
	files                 []string
	rows                  int
	bytes                 int64
}

// utf8BOM prefixes every CSV part.
//
// Excel on a non-English Windows does not detect UTF-8: shown a CSV with no byte
// order mark it decodes the bytes with the system ANSI code page (GBK on a
// Chinese install), so every Chinese value in the export opens as mojibake.
// Nothing is wrong with the file — the reader guessed — but the operator has a
// broken export and no way to tell why. The mark is how a CSV says which
// encoding it is in, and it is what the audit export already writes (handler/
// admin.go), so the two agree.
//
// The cost is that a strict parser reads the first header name with the mark
// still attached unless it asks for utf-8-sig. That is the accepted trade: these
// files are opened in a spreadsheet, and every spreadsheet handles the mark
// while none of them detect its absence.
const utf8BOM = "\uFEFF"

func (p *partWriter) startPart() error {
	p.idx++
	p.buf.Reset()
	// Before the csv.Writer, so the mark lands at byte 0 of the file. Each part is
	// a standalone .csv opened on its own, so each one needs its own.
	p.buf.WriteString(utf8BOM)
	p.w = csv.NewWriter(&p.buf)
	p.started = true
	// Sanitize the header too — a user-chosen column alias (SELECT x AS "=EVIL")
	// is otherwise a formula-injection vector on the first row (R25).
	head := make([]string, len(p.header))
	for i, h := range p.header {
		head[i] = csvSanitize(h)
	}
	return p.w.Write(head) // every part repeats the header row
}

func (p *partWriter) writeRow(rec []string) error {
	if !p.started {
		if err := p.startPart(); err != nil {
			return err
		}
	}
	// Defuse spreadsheet formula injection on every exported cell (M9).
	for i := range rec {
		rec[i] = csvSanitize(rec[i])
	}
	if err := p.w.Write(rec); err != nil {
		return err
	}
	p.rows++
	p.w.Flush()
	if int64(p.buf.Len()) >= exportPartSize {
		return p.flush()
	}
	return nil
}

func (p *partWriter) flush() error {
	if !p.started {
		return nil
	}
	p.w.Flush()
	// Password-protected ZIP (AES-256): the CSV lives inside as one entry.
	entry := fmt.Sprintf("%s-part%02d.csv", p.prefix, p.idx)
	enc, err := crypto.ZipEncrypt(p.buf.Bytes(), entry, p.password)
	if err != nil {
		return err
	}
	full := filepath.Join(p.dir, fmt.Sprintf("%s-part%02d.zip", p.prefix, p.idx))
	if err := os.WriteFile(full, enc, 0o644); err != nil {
		return err
	}
	p.files = append(p.files, full)
	p.bytes += int64(len(enc))
	p.buf.Reset()
	p.started = false
	return nil
}

// discard removes every part already written to disk. Called when the job
// fails: nothing references the files any more, so leaving them stranded only
// eats disk (B3). Removal failures are logged, not fatal — the job is failing
// with its own, more useful error.
func (p *partWriter) discard() {
	for _, f := range p.files {
		if err := os.Remove(f); err != nil {
			slog.Warn("failed export: could not remove orphaned part", "file", f, "err", err)
		}
	}
	p.files = nil
}

// ResolveExportFile validates that a requested download path is inside the
// configured export directory (blocks path traversal), exists, and belongs to
// the requesting user (a user may only download their own export files).
func (s *Services) ResolveExportFile(u *model.User, file string) (string, error) {
	base := s.ExportSavePath()
	if base == "" {
		return "", ErrExportPathUnset
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absFile, err := filepath.Abs(filepath.Clean(file))
	if err != nil {
		return "", err
	}
	if absFile != absBase && !strings.HasPrefix(absFile, absBase+string(os.PathSeparator)) {
		return "", ErrForbidden
	}
	if !s.ownsExportFile(u, absFile) {
		return "", ErrForbidden
	}
	if fi, err := os.Stat(absFile); err != nil || fi.IsDir() {
		return "", ErrNotFound
	}
	return absFile, nil
}

// ownsExportFile reports whether the file is a part of one of the user's jobs.
func (s *Services) ownsExportFile(u *model.User, absFile string) bool {
	if u == nil {
		return false
	}
	jobs, _ := s.Repo.ListExportJobs(u.ID, 0)
	for _, j := range jobs {
		for _, f := range strings.Split(j.Files, "\n") {
			if f = strings.TrimSpace(f); f == "" {
				continue
			}
			if abs, err := filepath.Abs(filepath.Clean(f)); err == nil && abs == absFile {
				return true
			}
		}
	}
	return false
}

// ---- CSV synthesis (mirrors the terminal's demo table generation) ----

var (
	exportSelectRe = regexp.MustCompile(`(?is)^\s*select\s+(.+?)\s+from\b`)
	exportAsRe     = regexp.MustCompile("(?i)\\s+as\\s+([`\"\\w]+)\\s*$")
	exportNames    = []string{"Alice Chen", "Bob Li", "Carol Wu", "David Zhao", "Eve Sun", "Frank Ma", "Grace Xu", "Henry Guo"}
	exportStatuses = []string{"active", "paused", "pending", "closed"}
	exportDates    = []string{"2026-06-28", "2026-06-29", "2026-06-30", "2026-07-01"}
)

func exportColumns(sql string) []string {
	generic := []string{"id", "name", "status", "created_at"}
	m := exportSelectRe.FindStringSubmatch(sql)
	if m == nil {
		return generic
	}
	list := strings.TrimSpace(m[1])
	if list == "" || strings.Contains(list, "*") {
		return generic
	}
	out := []string{}
	depth := 0
	var cur strings.Builder
	flush := func() {
		p := strings.TrimSpace(cur.String())
		cur.Reset()
		if p == "" {
			return
		}
		if as := exportAsRe.FindStringSubmatch(p); as != nil {
			p = as[1]
		} else if dot := strings.LastIndex(p, "."); dot >= 0 {
			p = p[dot+1:]
		}
		p = strings.Trim(strings.TrimSpace(p), "`\"")
		if p != "" {
			out = append(out, p)
		}
	}
	for _, r := range list {
		switch r {
		case '(':
			depth++
			cur.WriteRune(r)
		case ')':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case ',':
			if depth == 0 {
				flush()
			} else {
				cur.WriteRune(r)
			}
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	if len(out) == 0 {
		return generic
	}
	return out
}

func exportCell(col string, i int) string {
	k := strings.ToLower(col)
	switch {
	case k == "id" || strings.HasSuffix(k, "id"):
		return strconv.Itoa(1001 + i)
	case strings.Contains(k, "email"):
		return strings.ToLower(strings.Split(exportNames[i%len(exportNames)], " ")[0]) + "@vela.io"
	case strings.Contains(k, "name") || k == "user" || k == "actor":
		return exportNames[i%len(exportNames)]
	case strings.Contains(k, "status") || strings.Contains(k, "state"):
		return exportStatuses[i%len(exportStatuses)]
	case strings.HasSuffix(k, "_at") || strings.Contains(k, "date") || strings.Contains(k, "time") || strings.Contains(k, "created") || strings.Contains(k, "updated"):
		return exportDates[i%len(exportDates)]
	case strings.Contains(k, "amount") || strings.Contains(k, "price") || strings.Contains(k, "total") || strings.Contains(k, "qty") || strings.Contains(k, "count") || strings.Contains(k, "num") || strings.Contains(k, "score"):
		return strconv.Itoa((i+1)*128 + 7)
	default:
		return col + "_" + strconv.Itoa(i+1)
	}
}
