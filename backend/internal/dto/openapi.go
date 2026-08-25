package dto

import "velagateway/internal/model"

// ---- 开放接口凭据(控制台管理) ----

// AllowIPs/Enabled 是指针:控制台的启停开关只 PUT {enabled},编辑表单只 PUT 它改的
// 字段。nil = 没提 = 保持不变;非 nil 的空串/false 才是显式的"清空/停用"。这里被
// 归零的都是安全控制 —— 值类型的 AllowIPs 曾让"停用凭据"顺手抹掉它的 IP 白名单。
type APIClientReq struct {
	Name     string   `json:"name"`
	UserID   int64    `json:"userId"`   // 绑定的服务账号
	AllowIPs *string  `json:"allowIps"` // 逗号分隔 IP/CIDR;空 = 不限来源
	Scopes   []string `json:"scopes"`   // release:create / release:read / review:check
	Enabled  *bool    `json:"enabled"`
}

// ServiceAccountReq creates a machine principal for external integrations
// (升级单系统、CI/CD 平台). Roles decide what its releases may touch, tags narrow
// which instances it can even see — the same two axes a human gets.
type ServiceAccountReq struct {
	Name    string   `json:"name"`
	RoleIDs []int64  `json:"roleIds"`
	Tags    []string `json:"tags"`
	Dept    string   `json:"dept"` // shown in listings, e.g. "发布平台"; optional
}

// ServiceAccountView is one service account with what the binding UI needs.
type ServiceAccountView struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Email   string   `json:"email"` // generated identity, not a mailbox
	Status  string   `json:"status"`
	Dept    string   `json:"dept"`
	Roles   []string `json:"roles"`   // role names, for display
	Tags    []string `json:"tags"`    // directly-granted tags
	Clients int      `json:"clients"` // how many API credentials bind to it
}

// APIClientCreated carries the ONE plaintext copy of the credential. It is
// returned by the create call and never again — the table stores a bcrypt hash.
type APIClientCreated struct {
	Client model.APIClient `json:"client"`
	// Token is `<key>.<secret>`, ready to paste into `Authorization: Bearer`.
	Token string `json:"token"`
}

// ---- 开放接口:提交 SQL 升级单 ----

// OpenReleaseReq is the external contract. Everything addressable by NAME is,
// because a caller integrating over HTTP knows "order-cluster" and "标准发布流程",
// not this gateway's row ids; ids are accepted too and win when both are given.
//
// The body arrives as inline SQL (`sql`), as a script (`script`, or
// `scriptBase64` when the caller would otherwise have to escape a migration into
// JSON), or as a multipart `file`. A script is written to disk and referenced by
// digest, so what executes later is verified to be what was reviewed.
type OpenReleaseReq struct {
	Title string `json:"title"`
	// ExternalRef is the caller's own ticket id (change number, build id). It is
	// what makes a retry idempotent — the same ref from the same client returns
	// the release the first call created instead of raising a second one.
	ExternalRef  string `json:"externalRef"`
	ConnectionID int64  `json:"connectionId"`
	Instance     string `json:"instance"`
	Database     string `json:"database"`
	PipelineID   int64  `json:"pipelineId"`
	Pipeline     string `json:"pipeline"`
	SQL          string `json:"sql"`
	Script       string `json:"script"`
	ScriptBase64 string `json:"scriptBase64"`
	Filename     string `json:"filename"`
	Reason       string `json:"reason"`
	// MfaCode is only needed when the target tier requires a step-up AND the
	// service account has TOTP enrolled — the usual setup for a machine account
	// is no enrolment, so this is normally empty.
	MfaCode string `json:"mfaCode"`
}

// OpenReviewReq asks for a review WITHOUT raising a ticket — the pre-merge gate
// a CI job wants before it ever creates a change.
type OpenReviewReq struct {
	ConnectionID int64  `json:"connectionId"`
	Instance     string `json:"instance"`
	Dialect      string `json:"dialect"`
	SQL          string `json:"sql"`
	Script       string `json:"script"`
	ScriptBase64 string `json:"scriptBase64"`
	Filename     string `json:"filename"`
}

// OpenStage is one stage as an external caller sees it: enough to render a
// progress view, without the internal ids they cannot act on.
type OpenStage struct {
	Order      int    `json:"order"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Log        string `json:"log,omitempty"`
	ApprovalNo string `json:"approvalNo,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// OpenReleaseResp is the stable external shape of a ticket. It is deliberately
// NOT the internal ReleaseView: an integration contract that mirrors internal
// storage breaks every time storage changes.
type OpenReleaseResp struct {
	RelNo       string      `json:"relNo"`
	Title       string      `json:"title"`
	Status      string      `json:"status"` // pending|running|waiting|success|failed|aborted
	Risk        string      `json:"risk"`
	Instance    string      `json:"instance"`
	Database    string      `json:"database,omitempty"`
	Env         string      `json:"env"`
	Pipeline    string      `json:"pipeline"`
	ExternalRef string      `json:"externalRef,omitempty"`
	Error       string      `json:"error,omitempty"`
	CreatedAt   string      `json:"createdAt"`
	FinishedAt  string      `json:"finishedAt,omitempty"`
	Stages      []OpenStage `json:"stages"`
}
