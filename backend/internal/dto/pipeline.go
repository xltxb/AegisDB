package dto

import (
	"velagateway/internal/model"
	"velagateway/internal/review"
)

// ---- 数据库规范审查 (SQL review) ----

// ReviewRuleReq creates or edits one rule. Code/Category/Dialect are only read
// when creating (or when editing a custom rule) — a builtin rule's identity is
// what binds it to its checker.
type ReviewRuleReq struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Dialect   string `json:"dialect"`
	Category  string `json:"category"`
	Level     string `json:"level"`
	Enabled   bool   `json:"enabled"`
	Params    string `json:"params"`
	Message   string `json:"message"`
	SortOrder int    `json:"sortOrder"`
	// 规范出处。用指针:缺席 = 不改(内置规则的出处不该被一次改级别顺手清空),
	// 显式传空串 = 标为"平台内置,规范未覆盖"。
	Spec    *string `json:"spec,omitempty"`
	SpecRef *string `json:"specRef,omitempty"`
}

// ReviewCheckReq asks for a review. ConnectionID wins over Dialect when both are
// present: the target instance decides which rules can possibly apply.
type ReviewCheckReq struct {
	ConnectionID int64  `json:"connectionId"`
	Dialect      string `json:"dialect"`
	SQL          string `json:"sql" binding:"required"`
}

type ReviewCheckResp struct {
	Instance string        `json:"instance"`
	Result   review.Result `json:"result"`
}

// ReviewCatalogResp is the vocabulary the console renders its pickers from.
type ReviewCatalogResp struct {
	Dialects   []string `json:"dialects"`
	Categories []string `json:"categories"`
	Levels     []string `json:"levels"`
	// Specs:规范分级的展示顺序。空字符串(平台内置)不在其中 —— 它是"没有分级",
	// 不是第四级。
	Specs      []string `json:"specs"`
}

// ---- 发布流程 (CI/CD) ----

type PipelineStageReq struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Config    string `json:"config"`
	OnFailure string `json:"onFailure"`
}

type PipelineReq struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	TierCode    string             `json:"tierCode"`
	Enabled     bool               `json:"enabled"`
	IsDefault   bool               `json:"isDefault"`
	Stages      []PipelineStageReq `json:"stages"`
}

// PipelineDetail is a template together with its ordered stages — the two are
// never useful apart, so they travel as one.
type PipelineDetail struct {
	model.Pipeline
	Stages []model.PipelineStage `json:"stages"`
}

// ReleaseReq submits a change for release.
type ReleaseReq struct {
	Title          string `json:"title"`
	PipelineID     int64  `json:"pipelineId"` // 0 = the default flow for the target's tier
	ConnectionID   int64  `json:"connectionId" binding:"required"`
	Database       string `json:"database"`
	SQL            string `json:"sql"`
	ChangeType     string `json:"changeType"` // dml|ddl;空 = 由内容推断
	ScriptUploadID int64  `json:"scriptUploadId"`
	Reason         string `json:"reason"`
	MfaCode        string `json:"mfaCode"`
}

// ReleaseView is one run with the stages the pipeline view draws.
type ReleaseView struct {
	model.Release
	Stages []model.ReleaseStage `json:"stages"`
}

type ReleasePage struct {
	Items    []ReleaseView `json:"items"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
}
