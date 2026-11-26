package model

import "time"

// QueryGrant 查询授权:某人,在某段时间内,可以只读查询某个业务查询源上的某个库。
//
// 业务方的查询权**只来自这里**,不来自角色标签(见 .scratch/biz-data-query/PRD.md)。
// 授权由外部审批系统审批完,经开放接口下发(ScopeGrantWrite);网关不审批,只执行。
//
// 一个人对一个库只有一行(uk_query_grant),这样"某人现在能不能查某个库"永远只有一个答案。
// 再次下发时按审批单(externalId)区分(见 QueryGrantApproval):同一张单重发什么都不改;
// 新单**更新**这一行 —— 上限、有效期、导出开关、外部单号都换成新的,已撤销 / 已过期的也会
// 重新生效;已被取代的旧单拒绝。
type QueryGrant struct {
	ID           int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID       int64  `gorm:"uniqueIndex:uk_query_grant,priority:1;not null" json:"userId"`
	ConnectionID int64  `gorm:"uniqueIndex:uk_query_grant,priority:2;not null" json:"connectionId"`
	DBName       string `gorm:"column:db_name;uniqueIndex:uk_query_grant,priority:3;size:128;not null" json:"database"`

	// MaxRows / TimeoutSec 为 0 表示用全局默认(bizQuery.defaultMaxRows / defaultTimeoutSec)。
	MaxRows     int  `gorm:"not null;default:0" json:"maxRows"`
	TimeoutSec  int  `gorm:"column:timeout_sec;not null;default:0" json:"timeoutSec"`
	AllowExport bool `gorm:"not null;default:false" json:"allowExport"`

	StartsAt time.Time `gorm:"not null" json:"startsAt"`
	EndsAt   time.Time `gorm:"index:idx_query_grant_ends;not null" json:"endsAt"`
	// Status 是**记录下来**的状态:active / revoked / expired。判定时另外再比一次有效期,
	// 不依赖过期任务是否已经跑过 —— 到点就失效,不等任务。
	Status string `gorm:"size:16;not null;default:active" json:"status"`

	// 来源:哪个外部系统(API 凭据)、哪张外部审批单、谁批的、为什么。
	ClientID   int64  `gorm:"not null;default:0" json:"clientId"`
	ClientName string `gorm:"size:64;not null;default:''" json:"clientName"`
	ExternalID string `gorm:"column:external_id;index:idx_query_grant_ext;size:128;not null;default:''" json:"externalId"`
	Approver   string `gorm:"size:128;not null;default:''" json:"approver"`
	Reason     string `gorm:"size:512;not null;default:''" json:"reason"`

	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	RevokedBy string     `gorm:"size:128;not null;default:''" json:"revokedBy,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func (QueryGrant) TableName() string { return "tbl_query_grant" }

// QueryGrantApproval 记下"哪个外部系统的哪张审批单,作用过哪条授权"。
//
// 授权行上只存当前那张单号,认不出"旧单重放":审批系统一次延迟到达的重试,会把新批的条件
// 改回旧的、把刚撤销的授权复活。有了这张表,下发时能分清三种情况 —— 同一张单重发(幂等,
// 不改)、新单(更新)、已被取代的旧单(拒绝)。一张审批单可以批多个人 / 多个库,所以键里带授权。
type QueryGrantApproval struct {
	ID         int64     `gorm:"primaryKey;autoIncrement"`
	ClientID   int64     `gorm:"uniqueIndex:uk_query_grant_approval,priority:1;not null"`
	ExternalID string    `gorm:"column:external_id;uniqueIndex:uk_query_grant_approval,priority:2;size:128;not null"`
	GrantID    int64     `gorm:"uniqueIndex:uk_query_grant_approval,priority:3;not null"`
	CreatedAt  time.Time
}

func (QueryGrantApproval) TableName() string { return "tbl_query_grant_approval" }

// 查询授权的状态。前三个会存进 status 列;GrantPending 只出现在**生效状态**里(列表、我的授权):
// 记为 active、但还没到 StartsAt。
const (
	GrantActive  = "active"
	GrantRevoked = "revoked"
	GrantExpired = "expired"
	GrantPending = "pending"
)

// LiveAt 报告这条授权在 t 时刻是否生效:状态是 active,且 t 落在 [StartsAt, EndsAt) 内。
func (g *QueryGrant) LiveAt(t time.Time) bool {
	return g != nil && g.Status == GrantActive && !t.Before(g.StartsAt) && t.Before(g.EndsAt)
}

// RoleCodeBizQuery 是业务查询角色的代码。授权下发时,被授权人没有这个角色就自动加上。
const RoleCodeBizQuery = "bizquery"

// BizQueryNoTag 是业务查询角色挂的占位标签。
//
// 标签范围的规则是:一个角色**没有**标签就不限实例(TagsForRoles)。业务查询角色要是
// 不挂标签,它的成员就能从任何只校验标签的入口看到全部实例。挂一个没有任何实例会带的
// 标签,这个角色本身就不开放任何实例 —— 业务方的查询权只来自 QueryGrant。
const BizQueryNoTag = "__bizquery_none__"

// RoleNoInstanceTag 是新建自定义角色挂的占位标签,道理与 BizQueryNoTag 相同:新角色什么都不给,实例
// 范围也一样。不挂的话它就是"不限实例",而权限取并集 —— 一个受限用户加入它就能碰全部实例(2026-11
// 审计 V-03)。管理员在权限页给角色选了标签,这一行就被替换掉。
const RoleNoInstanceTag = "__no_instance__"

// IsPlaceholderTag 说明一个角色标签是不是占位:它只用来表示"不开放实例",不该显示给人看。
func IsPlaceholderTag(tag string) bool { return tag == BizQueryNoTag || tag == RoleNoInstanceTag }

// QueryGrantRequest 是一次在网关里提交的数据查询权限申请(#173,迁移 0066)。
//
// 它不单独记状态:状态就是 ApprovalID 那张审批单的状态。撤回、超时、驳回都走审批单原有的路,
// 不必在每条路上再同步一遍这里。批准后生成的授权记在 GrantID;批准时重新校验没通过的原因记在 Error。
type QueryGrantRequest struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID       int64     `gorm:"index:idx_qgr_user;not null" json:"userId"`
	ConnectionID int64     `gorm:"not null" json:"connectionId"`
	DBName       string    `gorm:"column:db_name;size:128;not null" json:"database"`
	Days         int       `gorm:"not null" json:"days"`
	AllowExport  bool      `gorm:"not null;default:false" json:"allowExport"`
	Reason       string    `gorm:"size:512;not null;default:''" json:"reason"`
	ApprovalID   int64     `gorm:"index:idx_qgr_approval;not null;default:0" json:"approvalId"`
	GrantID      int64     `gorm:"not null;default:0" json:"grantId"`
	Error        string    `gorm:"size:512;not null;default:''" json:"error"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (QueryGrantRequest) TableName() string { return "tbl_query_grant_request" }
