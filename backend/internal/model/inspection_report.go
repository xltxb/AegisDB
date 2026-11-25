package model

import "time"

// InspectionReport 是 AI 巡检系统推送来的一份实例巡检报告(.scratch/inspection-report)。
//
// 一个实例一天一份:同一实例同一巡检日期再推一次就覆盖(唯一索引 uk_inspection_report)。
// 完整 HTML 压缩后存在 inspection.savePath 下的文件里(FilePath 是相对路径),这里只存元数据与
// AI 给的结构化摘要 —— 列表、总览、趋势都从这张表出,不用去解析 HTML。永久保留。
type InspectionReport struct {
	ID           int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	ConnectionID int64 `gorm:"not null;uniqueIndex:uk_inspection_report,priority:1" json:"connectionId"`
	// InstanceName 是上传时的实例名快照:实例以后改名、被删,历史报告仍认得出是谁的。
	InstanceName string `gorm:"size:64;not null" json:"instanceName"`
	// ReportDate 巡检日期,YYYY-MM-DD(按巡检所在时区,由 AI 给出)。字符串而不是 DATE:两种库行为
	// 一致、按字典序排就是按日期排、没有时区换算。
	ReportDate  string     `gorm:"size:10;not null;uniqueIndex:uk_inspection_report,priority:2;index:idx_inspection_date" json:"reportDate"`
	InspectedAt *time.Time `json:"inspectedAt,omitempty"`
	// Score 健康分 0-100,AI 没给时为空。
	Score *int `json:"score,omitempty"`
	// Level 总体结论:InspectionOK / InspectionWarning / InspectionCritical。
	Level         string `gorm:"size:16;not null;index:idx_inspection_level" json:"level"`
	CriticalCount int    `gorm:"not null;default:0" json:"criticalCount"`
	WarningCount  int    `gorm:"not null;default:0" json:"warningCount"`
	InfoCount     int    `gorm:"not null;default:0" json:"infoCount"`
	// Findings 是 AI 给的问题列表(JSON 数组,见 InspectionFinding),原样存。
	Findings string `gorm:"type:text" json:"-"`
	// FilePath 相对 inspection.savePath 的路径(实例名/年/日期.html.gz);SizeBytes 是解压后的原始大小。
	FilePath    string    `gorm:"size:512;not null" json:"-"`
	SizeBytes   int64     `gorm:"not null;default:0" json:"sizeBytes"`
	SHA256      string    `gorm:"column:sha256;size:64;not null" json:"sha256"`
	APIClientID int64     `gorm:"not null;default:0" json:"apiClientId"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (InspectionReport) TableName() string { return "tbl_inspection_report" }

// InspectionReport.Level
const (
	InspectionOK       = "ok"
	InspectionWarning  = "warning"
	InspectionCritical = "critical"
)

// InspectionFinding 是摘要里的一条问题。
type InspectionFinding struct {
	Severity string `json:"severity"` // critical | warning | info
	Category string `json:"category,omitempty"`
	Title    string `json:"title"`
	Object   string `json:"object,omitempty"`
	Detail   string `json:"detail,omitempty"`
}
