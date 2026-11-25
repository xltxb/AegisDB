package dto

import "time"

// InspectionReportReq 是 AI 巡检系统推送一份报告的请求体(POST /open/inspection-reports,
// .scratch/inspection-report)。字段说明与校验见 service.UploadInspectionReport。
type InspectionReportReq struct {
	Instance    string                `json:"instance"`
	Date        string                `json:"date"`
	InspectedAt *time.Time            `json:"inspectedAt"`
	Score       *int                  `json:"score"`
	Level       string                `json:"level"`
	Counts      InspectionCounts      `json:"counts"`
	Findings    []InspectionFindingIn `json:"findings"`
	HTML        string                `json:"html"`
}

// InspectionCounts 各级问题数。
type InspectionCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// InspectionFindingIn 摘要里的一条问题。
type InspectionFindingIn struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Object   string `json:"object"`
	Detail   string `json:"detail"`
}
