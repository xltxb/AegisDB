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
	// PipelineID 绑定该凭据建单走的发布流程;nil = 不变,0 = 解绑(走分层默认)。
	PipelineID *int64 `json:"pipelineId"`
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
	// ChangeType declares what kind of change this is: dml or ddl. Empty lets
	// the gateway infer it from the statements; either way the two kinds may
	// never share one ticket.
	ChangeType string `json:"changeType"`
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
	// ChangeType:这段 SQL 要进的升级单类型(dml / ddl),声明了就核对内容是否相符;空 = 只查同单。
	ChangeType string `json:"changeType"`
}

// OpenPrecheck 是对外的检查结果形状。
//
// 它和内部的 service.PrecheckResult 是两份结构,理由与 OpenReleaseResp 相同:对接方
// 的契约不该跟着内部存储变。两边字段今天一样,但它们可以各自演进。
type OpenPrecheck struct {
	Statements int `json:"statements"`
	Errors     int `json:"errors"`
	Warnings   int `json:"warnings"`
	// SyntaxChecked / SyntaxSkipped 必须一起给:只给"没发现语法错"会让人以为每条都
	// 验过了,而 DDL 根本没法在不执行的前提下验(见 gateway/precheck.go)。
	SyntaxChecked int `json:"syntaxChecked"`
	SyntaxSkipped int `json:"syntaxSkipped"`
	// ObjectSource 说明对象结论是**怎么知道的**:cache(本地缓存,可能过期)/
	// live(当场查的目标库)/ none(没能核对)。cache 时 CacheSyncedAt 给出那份
	// 缓存的时间 —— 一份三天前的缓存说"表不存在",很可能只是那张表是昨天建的。
	ObjectSource  string `json:"objectSource"`
	CacheSyncedAt string `json:"cacheSyncedAt,omitempty"`
	Note          string `json:"note,omitempty"`
	Issues        []OpenPrecheckIssue `json:"issues"`
}

// OpenPrecheckIssue 是一条结论。kind 为 skipped 的是"没做的检查",它和"检查通过"
// 必须分得开 —— 结果里的沉默最容易被读成通过。
type OpenPrecheckIssue struct {
	Stmt    int    `json:"stmt"`
	Level   string `json:"level"`  // error | warn | info
	Kind    string `json:"kind"`   // syntax | object | skipped
	Object  string `json:"object,omitempty"`
	Message string `json:"message"`
	Source  string `json:"source,omitempty"`
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
	// Approvers 是可以放行这一步的人,任一人通过即生效,不是依次全部通过。
	//   - approve:生成了审批单后才有;审批接口的 approver 填其中一人的 account。
	//   - manual:只在正停着等人时有,是此刻能放行的人(status=active);确认接口的 confirmer 填它。
	// execute 阶段不给:执行变更只能在网关上确认。
	Approvers  []OpenApprover `json:"approvers,omitempty"`
	StartedAt  string         `json:"startedAt,omitempty"`
	FinishedAt string         `json:"finishedAt,omitempty"`
}

// OpenApprover 是审批链上的一个人。
type OpenApprover struct {
	Name    string `json:"name"`
	Account string `json:"account"` // 网关账号邮箱,审批接口的 approver 填它
	Status  string `json:"status"`  // waiting|active|approved|rejected
	ActedAt string `json:"actedAt,omitempty"`
}

// OpenDecideReq 是外部审批系统替审批人做的一次决定。
type OpenDecideReq struct {
	// Approver 是审批人的网关账号(邮箱)。必须在这张单的审批链上、且不是发起人。
	Approver string `json:"approver"`
	Action   string `json:"action"` // approve | reject
	Comment  string `json:"comment"`
}

// OpenDecideResp 报这一次决定之后的状态。AlreadyDecided = 这张单在本次请求之前已经被
// 决定过(控制台或别的调用抢先了),Status 是那次的结论,本次请求没有改变任何东西。
type OpenDecideResp struct {
	RelNo          string `json:"relNo"`
	ApprovalNo     string `json:"approvalNo"`
	Status         string `json:"status"` // approved|rejected|expired|pending
	AlreadyDecided bool   `json:"alreadyDecided"`
	ReleaseStatus  string `json:"releaseStatus"`
}

// OpenConfirmReq 是外部平台替确认人放行发布单上的「人工确认」。执行变更不在此列,只能在网关上点。
type OpenConfirmReq struct {
	// Confirmer 是确认人的网关账号(邮箱)。能不能放行与控制台「继续」按钮同一条规则。
	Confirmer string `json:"confirmer"`
	Comment   string `json:"comment"`
}

// OpenConfirmResp 报放行之后的状态。AlreadyConfirmed = 这道闸在本次请求之前已经被放行
// (控制台或别的调用抢先了),本次请求没有改变任何东西。
type OpenConfirmResp struct {
	RelNo            string `json:"relNo"`
	Stage            string `json:"stage"`
	AlreadyConfirmed bool   `json:"alreadyConfirmed"`
	ReleaseStatus    string `json:"releaseStatus"`
	// ExecutePending:发布单此刻停在「执行变更」等人确认。那一步开放接口不能放行,须在网关上点。
	ExecutePending bool `json:"executePending"`
}

// OpenReleaseResp is the stable external shape of a ticket. It is deliberately
// NOT the internal ReleaseView: an integration contract that mirrors internal
// storage breaks every time storage changes.
type OpenReleaseResp struct {
	RelNo       string      `json:"relNo"`
	Title       string      `json:"title"`
	Status      string      `json:"status"` // pending|running|waiting|success|failed|aborted
	ChangeType  string      `json:"changeType"` // dml|ddl
	Risk        string      `json:"risk"`
	Instance    string      `json:"instance"`
	Database    string      `json:"database,omitempty"`
	Env         string      `json:"env"`
	Pipeline    string      `json:"pipeline"`
	ExternalRef string      `json:"externalRef,omitempty"`
	Error       string      `json:"error,omitempty"`
	// Precheck 是收单那一刻的语法与对象检查结果。提单方拿到响应就知道这批 SQL 有没有
	// 明显问题,不必等发布跑起来 —— 对 CI 来说,那之间可能隔着一次人工审批。
	//
	// 它**不代表发布被拒**:检查不拦单(见 service/precheck.go)。errors > 0 的单子照样
	// 是 pending。调用方要不要据此让流水线红,是调用方的决定。
	Precheck    *OpenPrecheck `json:"precheck,omitempty"`
	CreatedAt   string      `json:"createdAt"`
	FinishedAt  string      `json:"finishedAt,omitempty"`
	Stages      []OpenStage `json:"stages"`
}
