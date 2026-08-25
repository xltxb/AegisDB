package service

// 导出数据保留策略 —— 定时清理服务器上过期的导出归档。
//
// An export writes real production rows to disk, encrypted, and then they sit
// there forever. Two things follow from that, and this file exists for both:
// the disk fills, and — the one that matters more — a copy of production data
// keeps existing long after anyone needed it. Retention is a security control
// wearing a housekeeping coat.
//
// Default 3 days. `export.retentionDays = 0` keeps archives forever, matching
// how the other export settings spell "no limit" (export.maxRows: 0 = 不限).
//
// The row is NOT deleted with the file. What was exported, by whom, how many
// rows, and when, is exactly what an auditor asks about — and it should outlive
// the bytes. Only the pointer (Files) and the archive password go.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"velagateway/internal/model"
)

// defaultExportRetentionDays is the shipped default: three days on disk.
const defaultExportRetentionDays = 3

// exportSweepBatch bounds one pass. A gateway that has been off for a month
// comes back to thousands of expired jobs; deleting them in one transaction-less
// burst would hold the audit lock for minutes. The sweep runs hourly, so the
// backlog drains over a few passes instead.
const exportSweepBatch = 200

// exportPartRe matches the filenames the exporter itself produces
// (produceExport: "<prefix>-part%02d.zip"). The orphan sweep deletes ONLY files
// of this shape: export.savePath is an operator-chosen directory, and a cleanup
// job that removes files it cannot prove it wrote is a cleanup job that will
// one day eat something else.
var exportPartRe = regexp.MustCompile(`-part\d{2,}\.zip$`)

// ExportRetentionDays is the configured retention in days (0 = keep forever).
func (s *Services) ExportRetentionDays() int {
	d := s.settingInt("export.retentionDays", defaultExportRetentionDays)
	if d < 0 {
		return 0 // a negative retention is not "delete everything"; it is a typo
	}
	return d
}

// SweepExportRetention deletes archives past the retention window and marks
// their jobs expired. Safe to call on a ticker and at boot; it does nothing when
// retention is disabled or nothing has aged out.
func (s *Services) SweepExportRetention() {
	days := s.ExportRetentionDays()
	if days == 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days)

	jobs, err := s.Repo.ExpiredExportJobs(cutoff, exportSweepBatch)
	if err != nil {
		slog.Error("export retention: listing expired jobs failed", "err", err)
		return
	}
	var files, bytes int64
	for _, j := range jobs {
		n, size := s.expireExportJob(j, days)
		files += n
		bytes += size
	}
	orphans, orphanBytes := s.sweepOrphanExports(cutoff)

	if len(jobs) > 0 || orphans > 0 {
		slog.Info("export retention sweep",
			"retentionDays", days, "jobs", len(jobs), "files", files, "bytes", bytes,
			"orphanFiles", orphans, "orphanBytes", orphanBytes)
	}
}

// expireExportJob removes one job's parts and records what happened.
func (s *Services) expireExportJob(j model.ExportJob, days int) (files, bytes int64) {
	for _, f := range strings.Split(j.Files, "\n") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if fi, err := os.Stat(f); err == nil {
			bytes += fi.Size()
		}
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			// A file that will not delete (open handle, permissions) must not stop
			// the row from being marked: leaving it `done` would keep offering a
			// download that may already be half gone.
			slog.Warn("export retention: could not delete part", "job", j.ID, "file", f, "err", err)
			continue
		}
		files++
	}

	if err := s.Repo.UpdateExportJob(j.ID, map[string]any{
		"status": model.ExportExpired,
		// The pointer and the secret go together: a password for an archive that
		// no longer exists protects nothing and leaks if the row is ever dumped.
		"files":    "",
		"password": "",
	}); err != nil {
		slog.Error("export retention: marking job expired failed", "job", j.ID, "err", err)
		return files, bytes
	}

	// Audited against the OWNER, with the sweep as operator — the same shape the
	// audit chain uses everywhere else for "someone else authorised this on your
	// behalf". No webhook event: retention is not a command anyone ran.
	owner, _ := s.Repo.GetUserByID(j.UserID)
	if owner == nil {
		owner = &model.User{ID: j.UserID, Name: "(已删除用户)"}
	}
	conn, _ := s.Repo.GetConnection(j.ConnectionID)
	s.recordAuditBy(owner, "系统 · 导出保留策略", conn,
		fmt.Sprintf("EXPORT-CLEANUP #%d · %s · %d 个分卷 · %s · 保留期 %d 天",
			j.ID, j.Name, j.Parts, humanBytes(int(bytes)), days),
		model.RiskLow, model.ResultExecuted, "", "")
	return files, bytes
}

// sweepOrphanExports removes aged part files that no job row claims.
//
// They come from exports that died mid-write and from restored databases, and
// nothing else will ever collect them — the job-driven pass above only knows
// about files a row still points at. Deliberately conservative: under the export
// directory, matching the exporter's own filename shape, older than the cutoff,
// and claimed by no job.
func (s *Services) sweepOrphanExports(cutoff time.Time) (files, bytes int64) {
	base := s.ExportSavePath()
	if strings.TrimSpace(base) == "" {
		return 0, 0
	}
	if _, err := os.Stat(base); err != nil {
		return 0, 0 // not configured yet, or not mounted — not this sweep's problem
	}
	claimed := map[string]bool{}
	rows, err := s.Repo.AllExportFiles()
	if err != nil {
		// Without the claim set every file looks like an orphan. Refuse to delete
		// on a partial picture.
		slog.Error("export retention: reading claimed files failed, skipping orphan sweep", "err", err)
		return 0, 0
	}
	for _, r := range rows {
		for _, f := range strings.Split(r, "\n") {
			if f = strings.TrimSpace(f); f != "" {
				if abs, aerr := filepath.Abs(filepath.Clean(f)); aerr == nil {
					claimed[abs] = true
				}
			}
		}
	}

	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is skipped, not fatal
		}
		if !exportPartRe.MatchString(d.Name()) {
			return nil // not something this exporter wrote
		}
		abs, aerr := filepath.Abs(path)
		if aerr != nil || claimed[abs] {
			return nil
		}
		fi, serr := d.Info()
		if serr != nil || !fi.ModTime().Before(cutoff) {
			return nil
		}
		size := fi.Size()
		if rerr := os.Remove(path); rerr != nil {
			slog.Warn("export retention: could not delete orphan", "file", path, "err", rerr)
			return nil
		}
		files++
		bytes += size
		return nil
	})
	return files, bytes
}
