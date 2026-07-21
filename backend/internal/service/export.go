package service

import (
	"bytes"
	"encoding/csv"
	"fmt"
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
)

// exportPartSize is the (uncompressed) CSV size at which the export rolls over
// to a new part file. There is no row limit — large results split into parts.
var exportPartSize int64 = 100 << 20 // 100 MiB

// SetExportPartSize overrides the CSV part-size rollover threshold (tests/config).
func SetExportPartSize(n int64) {
	if n > 0 {
		exportPartSize = n
	}
}

// exportMaxRows caps a single export job's row count so one unbounded query
// (e.g. a cartesian product) can't fill the disk / exhaust memory (M8). 0 = no
// limit. Default 5,000,000 rows.
var exportMaxRows int64 = 5_000_000

// SetExportMaxRows overrides the per-job export row cap (tests/config).
func SetExportMaxRows(n int64) { exportMaxRows = n }

// exportMaxBytes caps a job's cumulative raw (uncompressed) CSV content. The row
// cap alone doesn't bound wide-column results (large BLOB/TEXT), so a few
// thousand fat rows could still exhaust memory/disk (B2). 0 = no limit. ~2 GiB.
var exportMaxBytes int64 = 2_000_000_000

// SetExportMaxBytes overrides the per-job export byte cap (tests/config).
func SetExportMaxBytes(n int64) { exportMaxBytes = n }

// exportLimitErr reports a breach of the row or byte cap given a job's running
// totals (rows already written, cumulative raw bytes). Checked before writing
// each row so the job fails fast with a clear message instead of after OOM.
func exportLimitErr(rows int, rawBytes int64) error {
	if exportMaxRows > 0 && int64(rows) >= exportMaxRows {
		return fmt.Errorf("导出超过行数上限 %d 行,请缩小查询范围或分批导出", exportMaxRows)
	}
	if exportMaxBytes > 0 && rawBytes > exportMaxBytes {
		return fmt.Errorf("导出超过数据量上限 %d 字节,请缩小查询范围或分批导出", exportMaxBytes)
	}
	return nil
}

// DefaultExportDir is the built-in export directory (relative to the backend's
// working directory) used when no export.savePath is configured.
const DefaultExportDir = "export"

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
	job := &model.ExportJob{
		UserID: u.ID, ConnectionID: connID, Instance: conn.Env + "-" + conn.Name,
		Database: strings.TrimSpace(database),
		SQL:      sql, Name: strings.TrimSpace(name), Status: model.ExportPending,
	}
	if err := s.Repo.CreateExportJob(job); err != nil {
		return nil, err
	}
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
	job, err := s.Repo.GetExportJob(id)
	if err != nil {
		return
	}
	_ = s.Repo.UpdateExportJob(id, map[string]any{"status": model.ExportRunning})
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
	_ = s.Repo.UpdateExportJob(id, map[string]any{
		"status": model.ExportDone, "files": strings.Join(files, "\n"), "parts": len(files),
		"password": encPw, "rows": rows, "bytes": size, "finished_at": now,
	})
	s.recordAudit(u, conn, "EXPORT "+clip(job.SQL, 80), model.RiskMid, model.ResultExecuted, "", "exec")
}

func (s *Services) failExport(id int64, msg string) {
	now := time.Now()
	_ = s.Repo.UpdateExportJob(id, map[string]any{"status": model.ExportFailed, "error": msg, "finished_at": now})
}

// produceExport streams the result set into rolling ~100MB CSV parts, each
// gzip-compressed + AES-256-GCM encrypted with one shared password, archived
// under <exportPath>/<username>/<name>-<timestamp>-part<n>.csv.gz.enc.
// Returns the list of part files (all download-able), the password, total rows,
// and total encrypted size. There is no row limit.
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

	// Enforce the per-job row AND byte caps: reject before writing the row that
	// would exceed a limit, failing the job with a clear message (M8/B2).
	var rawBytes int64
	writeRow := func(rec []string) error {
		for _, c := range rec {
			rawBytes += int64(len(c)) + 1 // +1 approximates the field separator
		}
		if err := exportLimitErr(pw.rows, rawBytes); err != nil {
			return err
		}
		return pw.writeRow(rec)
	}

	if gateway.RealExecSupported(conn) {
		if err := gateway.RealQueryEach(conn, sql, setHeader, writeRow); err != nil {
			return nil, "", 0, 0, err // job fails with the real DB error
		}
	} else {
		// simulated demo data (no real target): synthesize a result set
		cols := exportColumns(sql)
		pw.header = cols
		n := s.Executor.Run(conn, sql).Rows
		if n <= 0 {
			n = 200
		}
		rec := make([]string, len(cols))
		for i := 0; i < n; i++ {
			for j, col := range cols {
				rec[j] = exportCell(col, i)
			}
			if err := writeRow(rec); err != nil {
				return nil, "", 0, 0, err
			}
		}
	}
	if err := pw.flush(); err != nil { // flush the trailing partial part
		return nil, "", 0, 0, err
	}
	if len(pw.files) == 0 { // empty result → still emit a header-only file
		if err := pw.startPart(); err != nil {
			return nil, "", 0, 0, err
		}
		if err := pw.flush(); err != nil {
			return nil, "", 0, 0, err
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

func (p *partWriter) startPart() error {
	p.idx++
	p.buf.Reset()
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
