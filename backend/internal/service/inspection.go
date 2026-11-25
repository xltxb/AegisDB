package service

// AI 巡检报告(.scratch/inspection-report)。
//
// AI 巡检系统每巡检完一个实例,就经开放接口推一份报告:结构化摘要 + 完整 HTML。HTML 压缩后
// 存在 inspection.savePath 下(实例名/年/日期.html.gz),摘要进 tbl_inspection_report。
// 同一实例同一天再推一次就覆盖。报告永久保留,没有自动清理。

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/dto"
	"velagateway/internal/model"
)

var (
	// ErrInspectionPathUnset:系统设置里还没配巡检报告保存路径,报告不接收。
	ErrInspectionPathUnset = errors.New("巡检报告保存路径未配置,请先在管理后台 · 系统设置里配置")
	// ErrInspectionUnknownInstance:平台里没有这个实例名。AI 侧不要重试,先对一下实例名。
	ErrInspectionUnknownInstance = errors.New("平台里没有这个实例")
	// ErrInspectionInvalid:请求字段不合法,错误信息里说清是哪个字段。
	ErrInspectionInvalid = errors.New("巡检报告字段不合法")
)

// InspectionMaxFindings 是一份报告里问题条数的上限。
const InspectionMaxFindings = 500

var inspectionDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func invalidInspection(format string, a ...any) error {
	return apimsg.Errorf("%w: %s", ErrInspectionInvalid, apimsg.Errorf(format, a...))
}

// validateInspection 按 PRD 校验请求;返回第一个不合法的字段。
func validateInspection(req *dto.InspectionReportReq, now time.Time) error {
	req.Instance = strings.TrimSpace(req.Instance)
	if req.Instance == "" {
		return invalidInspection("instance 必填")
	}
	if !inspectionDateRe.MatchString(req.Date) {
		return invalidInspection("date 须为 YYYY-MM-DD,实际 %q", req.Date)
	}
	d, err := time.ParseInLocation("2006-01-02", req.Date, now.Location())
	if err != nil {
		return invalidInspection("date 不是有效日期:%q", req.Date)
	}
	// 容一天的时区差;更远的未来日期多半是 AI 侧时钟或格式错了
	if d.After(now.AddDate(0, 0, 1)) {
		return invalidInspection("date 不能晚于明天:%s", req.Date)
	}
	switch req.Level {
	case model.InspectionOK, model.InspectionWarning, model.InspectionCritical:
	default:
		return invalidInspection("level 只能是 ok / warning / critical,实际 %q", req.Level)
	}
	if req.Score != nil && (*req.Score < 0 || *req.Score > 100) {
		return invalidInspection("score 须在 0-100,实际 %d", *req.Score)
	}
	if req.Counts.Critical < 0 || req.Counts.Warning < 0 || req.Counts.Info < 0 {
		return invalidInspection("counts 各项不能为负")
	}
	if len(req.Findings) > InspectionMaxFindings {
		return invalidInspection("findings 最多 %d 条,实际 %d", InspectionMaxFindings, len(req.Findings))
	}
	for i, f := range req.Findings {
		switch f.Severity {
		case "critical", "warning", "info":
		default:
			return invalidInspection("findings[%d].severity 只能是 critical / warning / info,实际 %q", i, f.Severity)
		}
		if strings.TrimSpace(f.Title) == "" {
			return invalidInspection("findings[%d].title 必填", i)
		}
	}
	if strings.TrimSpace(req.HTML) == "" {
		return invalidInspection("html 必填")
	}
	return nil
}

// inspectionPathSegment 把实例名变成安全的目录名:只留字母、数字、点、下划线、连字符,
// 其余换成下划线 —— 实例名里出现 `/`、`..` 也写不出保存目录。
var unsafeSegmentRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func inspectionPathSegment(name string) string {
	s := unsafeSegmentRe.ReplaceAllString(name, "_")
	s = strings.Trim(s, ".")
	if s == "" {
		s = "_"
	}
	return s
}

// inspectionInstanceDir 是一个实例的报告目录:<连接ID>-<实例名>。只用实例名时,非 ASCII 字符都换成
// `_`,同样字数的中文实例名会落到同一目录、同一天的报告互相覆盖(2026-10 安全审计 M10)。带上连接 ID
// 后一定不同;实例名留着方便人在磁盘上认。已有报告按库里记的路径读,不受影响。
func inspectionInstanceDir(connID int64, name string) string {
	return strconv.FormatInt(connID, 10) + "-" + inspectionPathSegment(name)
}

// UploadInspectionReport 接收一份巡检报告:校验 → 压缩落盘 → 入库(同实例同日覆盖)→ 审计。
func (s *Services) UploadInspectionReport(client *model.APIClient, actor *model.User, req dto.InspectionReportReq) (*model.InspectionReport, bool, error) {
	now := time.Now()
	if err := validateInspection(&req, now); err != nil {
		return nil, false, err
	}
	base := s.InspectionSavePath()
	if base == "" {
		return nil, false, ErrInspectionPathUnset
	}
	conn, err := s.Repo.GetConnectionByName(req.Instance)
	if err != nil {
		return nil, false, fmt.Errorf("%w:%s", ErrInspectionUnknownInstance, req.Instance)
	}

	rel := filepath.Join(inspectionInstanceDir(conn.ID, conn.Name), req.Date[:4], req.Date+".html.gz")
	full := filepath.Join(base, rel)
	// 双保险:拼出来的路径必须还在保存目录里
	if r, err := filepath.Rel(base, full); err != nil || strings.HasPrefix(r, "..") {
		return nil, false, invalidInspection("instance 不能用作目录名:%q", req.Instance)
	}
	sum := sha256.Sum256([]byte(req.HTML))
	if err := writeGzipAtomic(full, []byte(req.HTML)); err != nil {
		return nil, false, apimsg.Errorf("保存巡检报告失败: %w", err)
	}

	findings := "[]"
	if len(req.Findings) > 0 {
		b, _ := json.Marshal(req.Findings)
		findings = string(b)
	}
	var clientID int64
	if client != nil {
		clientID = client.ID
	}
	rep := &model.InspectionReport{
		ConnectionID: conn.ID, InstanceName: conn.Name, ReportDate: req.Date, InspectedAt: req.InspectedAt,
		Score: req.Score, Level: req.Level,
		CriticalCount: req.Counts.Critical, WarningCount: req.Counts.Warning, InfoCount: req.Counts.Info,
		Findings: findings, FilePath: filepath.ToSlash(rel), SizeBytes: int64(len(req.HTML)),
		SHA256: hex.EncodeToString(sum[:]), APIClientID: clientID,
	}
	replaced, err := s.Repo.UpsertInspectionReport(rep)
	if err != nil {
		return nil, false, err
	}
	verb := "上传"
	if replaced {
		verb = "覆盖"
	}
	clientName := ""
	if client != nil {
		clientName = client.Name
	}
	s.recordAuditBy(actor, apiOperator(client), conn, fmt.Sprintf(
		"inspection.upload %s巡检报告 #%d:%s %s · 结论 %s · 严重 %d / 警告 %d / 提示 %d · 外部系统 %s",
		verb, rep.ID, conn.Name, req.Date, req.Level, req.Counts.Critical, req.Counts.Warning, req.Counts.Info, orDash(clientName)),
		model.RiskLow, model.ResultExecuted, "", "")
	return rep, replaced, nil
}

// writeGzipAtomic 压缩写到临时文件再改名,读的人永远看不到写了一半的文件。
func writeGzipAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // 改名成功后它已经不在了,删除失败无妨
	zw := gzip.NewWriter(tmp)
	if _, err := zw.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadInspectionHTML 读回一份报告解压后的 HTML。
func (s *Services) ReadInspectionHTML(rep *model.InspectionReport) ([]byte, error) {
	base := s.InspectionSavePath()
	if base == "" {
		return nil, ErrInspectionPathUnset
	}
	f, err := os.Open(filepath.Join(base, filepath.FromSlash(rep.FilePath)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, zr); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- 查看(工单 03) ----

// InspectionReportView 是一份报告的摘要(列表、历史用);Findings 只在详情里带。
type InspectionReportView struct {
	ID            int64                     `json:"id"`
	ConnectionID  int64                     `json:"connectionId"`
	InstanceName  string                    `json:"instanceName"`
	ReportDate    string                    `json:"reportDate"`
	InspectedAt   *time.Time                `json:"inspectedAt,omitempty"`
	Score         *int                      `json:"score,omitempty"`
	Level         string                    `json:"level"`
	CriticalCount int                       `json:"criticalCount"`
	WarningCount  int                       `json:"warningCount"`
	InfoCount     int                       `json:"infoCount"`
	SizeBytes     int64                     `json:"sizeBytes"`
	UpdatedAt     time.Time                 `json:"updatedAt"`
	Findings      []model.InspectionFinding `json:"findings,omitempty"`
	// TopFinding 最严重的那条问题的标题:最新列表里带上,总览与实例标记的悬停提示用,不必再取详情。
	TopFinding string `json:"topFinding,omitempty"`
}

func inspectionView(r *model.InspectionReport, withFindings bool) InspectionReportView {
	v := InspectionReportView{
		ID: r.ID, ConnectionID: r.ConnectionID, InstanceName: r.InstanceName, ReportDate: r.ReportDate,
		InspectedAt: r.InspectedAt, Score: r.Score, Level: r.Level,
		CriticalCount: r.CriticalCount, WarningCount: r.WarningCount, InfoCount: r.InfoCount,
		SizeBytes: r.SizeBytes, UpdatedAt: r.UpdatedAt,
	}
	if withFindings {
		_ = json.Unmarshal([]byte(r.Findings), &v.Findings)
		if v.Findings == nil {
			v.Findings = []model.InspectionFinding{}
		}
	}
	return v
}

// InspectionLatestRow 是实例列表的一行:实例本身 + 它最近一份报告(没有就是 nil)。
type InspectionLatestRow struct {
	ConnectionID int64                 `json:"connectionId"`
	Instance     string                `json:"instance"`
	Env          string                `json:"env"`
	Engine       string                `json:"engine"`
	Report       *InspectionReportView `json:"report"`
}

// LatestInspectionReports:每个实例一行(没有任何报告的实例也在,Report 为 nil),外加服务器的
// "今天" —— 前端据此标出今天还没收到报告的实例。
func (s *Services) LatestInspectionReports() ([]InspectionLatestRow, string, error) {
	conns, err := s.Repo.ListConnections()
	if err != nil {
		return nil, "", err
	}
	reps, err := s.Repo.LatestInspectionReports()
	if err != nil {
		return nil, "", err
	}
	byConn := map[int64]*model.InspectionReport{}
	for i := range reps {
		byConn[reps[i].ConnectionID] = &reps[i]
	}
	out := make([]InspectionLatestRow, 0, len(conns))
	for _, c := range conns {
		row := InspectionLatestRow{ConnectionID: c.ID, Instance: c.Name, Env: c.Env, Engine: c.Engine}
		if r := byConn[c.ID]; r != nil {
			v := inspectionView(r, false)
			v.TopFinding = topFinding(r.Findings)
			row.Report = &v
		}
		out = append(out, row)
	}
	return out, time.Now().Format("2006-01-02"), nil
}

// topFinding 取问题列表里最严重的那条的标题(严重 > 警告 > 提示,同级取第一条)。
func topFinding(raw string) string {
	var fs []model.InspectionFinding
	if json.Unmarshal([]byte(raw), &fs) != nil {
		return ""
	}
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	best, bestRank := "", 99
	for _, f := range fs {
		if r, ok := rank[f.Severity]; ok && r < bestRank {
			best, bestRank = f.Title, r
		}
	}
	return best
}

// ListInspectionReports:某实例的报告历史,按巡检日期倒序。
func (s *Services) ListInspectionReports(connID int64, page, pageSize int) ([]InspectionReportView, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 30
	}
	reps, total, err := s.Repo.ListInspectionReports(connID, (page-1)*pageSize, pageSize)
	if err != nil {
		return nil, 0, err
	}
	out := make([]InspectionReportView, 0, len(reps))
	for i := range reps {
		out = append(out, inspectionView(&reps[i], false))
	}
	return out, total, nil
}

// GetInspectionReport:单份报告的摘要 + 问题列表。
func (s *Services) GetInspectionReport(id int64) (*InspectionReportView, error) {
	r, err := s.Repo.GetInspectionReport(id)
	if err != nil {
		return nil, ErrNotFound
	}
	v := inspectionView(r, true)
	return &v, nil
}

// ErrInspectionFileMissing:元数据在,报告文件却不见了(被人手工删了、保存路径改了)。
var ErrInspectionFileMissing = errors.New("报告文件不存在(可能被删除,或保存路径已更改)")

// InspectionReportHTML 读回完整 HTML,并记一条查看审计(谁、什么时候、看了哪个实例哪天的报告)。
func (s *Services) InspectionReportHTML(u *model.User, id int64) (*model.InspectionReport, []byte, error) {
	r, err := s.Repo.GetInspectionReport(id)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	html, err := s.ReadInspectionHTML(r)
	if err != nil {
		if errors.Is(err, ErrInspectionPathUnset) {
			return nil, nil, err
		}
		return nil, nil, ErrInspectionFileMissing
	}
	conn, _ := s.Repo.GetConnection(r.ConnectionID)
	s.recordAudit(u, conn, fmt.Sprintf("inspection.view 查看巡检报告 #%d:%s %s", r.ID, r.InstanceName, r.ReportDate),
		model.RiskLow, model.ResultExecuted, "", "")
	return r, html, nil
}
