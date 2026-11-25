package dto

import (
	"time"

	"velagateway/internal/model"
)

// QuerySourceView 是一个业务查询源(可授权的实例)。
type QuerySourceView struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Engine string `json:"engine"`
	Env    string `json:"env"`
	Status string `json:"status"`
}

// QueryGrantReq 是外部审批系统下发 / 更新一条查询授权。
//
// 按 (email, sourceId, database) 定位:同一个人对同一个库再次下发即更新。
type QueryGrantReq struct {
	ExternalID string `json:"externalId"` // 外部审批单号,必填
	Email      string `json:"email"`      // 被授权人,必填
	Name       string `json:"name"`       // 被授权人姓名(自动建号时用)
	Dept       string `json:"dept"`       // 被授权人部门(自动建号时用)
	SourceID   int64  `json:"sourceId"`   // 业务查询源 id,必填
	Database   string `json:"database"`   // 库名,必填,必须是该实例上真实存在的库

	MaxRows     int        `json:"maxRows"`    // 0 = 全局默认;不能超过全局上限
	TimeoutSec  int        `json:"timeoutSec"` // 0 = 全局默认;不能超过全局上限
	AllowExport bool       `json:"allowExport"`
	StartsAt    *time.Time `json:"startsAt"` // 不填 = 现在
	EndsAt      *time.Time `json:"endsAt"`   // 必填

	Approver string `json:"approver"`
	Reason   string `json:"reason"`
}

// QueryGrantRevokeReq 撤销时附带的原因(可选)。
type QueryGrantRevokeReq struct {
	Reason string `json:"reason"`
}

// QueryGrantView 是一条查询授权。Status 是**生效状态**:active / pending(未到开始时间)
// / expired / revoked。
type QueryGrantView struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	UserName string `json:"userName"`
	Dept     string `json:"dept"`
	SourceID int64  `json:"sourceId"`
	Source   string `json:"source"`
	Database string `json:"database"`

	MaxRows             int  `json:"maxRows"`
	TimeoutSec          int  `json:"timeoutSec"`
	EffectiveMaxRows    int  `json:"effectiveMaxRows"`
	EffectiveTimeoutSec int  `json:"effectiveTimeoutSec"`
	AllowExport         bool `json:"allowExport"`
	// CanExport:此刻能不能导出(全局开关、授权「允许导出」、实例分层的数据查询导出限制合起来算)
	CanExport bool `json:"canExport"`

	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
	Status   string    `json:"status"`

	ExternalID string     `json:"externalId"`
	Client     string     `json:"client"`
	Approver   string     `json:"approver"`
	Reason     string     `json:"reason"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	RevokedBy  string     `json:"revokedBy,omitempty"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// BizQueryReq 业务方执行一条查询。
// BizTablesResp 授权库里的表与视图(数据查询页的"表"下拉)。Error 非空 = 读不到目录,不是拒绝。
type BizTablesResp struct {
	Tables    []string `json:"tables"`
	Truncated bool     `json:"truncated"`
	Error     string   `json:"error,omitempty"`
	// Quote:这台库的标识符引号(MySQL 系反引号,其余双引号),数据查询页补全插入要加引号的名字时用
	Quote string `json:"quote,omitempty"`
}

// BizColumnsResp 是数据查询页补全用的"这张表有哪些列"。读不到时 Columns 为空、Error 说原因。
type BizColumnsResp struct {
	Columns []string `json:"columns"`
	Error   string   `json:"error,omitempty"`
}

type BizQueryReq struct {
	SourceID int64  `json:"sourceId"`
	Database string `json:"database"`
	SQL      string `json:"sql"`
}

// BizQueryResp 一次业务查询的结果。执行层面的失败(超时、库报错)也用它回:Error 非空,
// 业务码仍是 0 —— 判定层面的拒绝(没授权、不是只读语句)才是业务错误码。
type BizQueryResp struct {
	Columns       []string   `json:"columns"`
	Rows          [][]string `json:"rows"`
	Truncated     bool       `json:"truncated"`
	MaxRows       int        `json:"maxRows"`
	TimeoutSec    int        `json:"timeoutSec"`
	Ms            int        `json:"ms"`
	TimedOut      bool       `json:"timedOut"`
	MaskedColumns []string   `json:"maskedColumns,omitempty"`
	Simulated     bool       `json:"simulated,omitempty"`
	Error         string     `json:"error,omitempty"`
	// ErrorRef 是 Error 的机器可读版本(code + 参数),前端按读者语言渲染;见 model.RenderBizReason。
	ErrorRef *model.RuleRef `json:"errorRef,omitempty"`
	// Served 这次由谁提供:replica = 从库;primaryFallback = 配了从库但连不上,回落主库;
	// 空 = 这个数据源没配从库。ReplicaError 是回落时从库连不上的原因。
	Served       string `json:"served,omitempty"`
	ReplicaError string `json:"replicaError,omitempty"`
}
