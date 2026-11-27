package service

// 在网关里申请、审批、直接开通数据查询授权(#173)。
//
// 授权原先只有一个来源:外部审批系统经开放接口下发(query_grant.go 的 UpsertQueryGrant)。没有
// 外部审批系统的部署里,业务方无处申请。这里补上两条路:
//
//   - 申请:业务方在数据查询页提交,生成一张审批单(类型 grant),走默认审批链 —— 谁能批、
//     超时怎么处理、能不能自审批,全部沿用审批单那一套,不长出第二套规则。批准后按当时的全局
//     上限重新校验,再生成 / 更新授权。
//   - 直接开通:管理员在【权限 · 查询授权】给已有账号开通。
//
// 两条路写授权都走 writeGatewayGrant,和开放接口共用 checkGrantLimits 与 ensureBizQueryRole:
// 哪条路批出来的授权,都不能比全局上限更宽,也都会给被授权人补上业务查询角色。

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
)

// 网关自己下发的授权在 client_name 上写什么。client_id 为 0:不属于任何外部系统。
const (
	grantViaApproval = "网关审批"
	grantViaConsole  = "控制台"
)

// ErrApplyExternal:申请方式是外部审批系统,网关里不收申请。
var ErrApplyExternal = apimsg.Errorf("%w: 数据查询权限需在外部审批系统申请", ErrBadRequest)

// ---------------------------------------------------------------- 申请人能选什么

// ApplySources 列出可以申请的业务查询源。不按调用者的标签过滤:申请本来就是为了拿到还没有的
// 访问权;能看到的只是实例名、引擎、环境,不含地址与凭据。
func (s *Services) ApplySources() []dto.QuerySourceView {
	conns, err := s.Repo.BizQuerySources()
	out := []dto.QuerySourceView{}
	if err != nil {
		return out
	}
	for _, c := range conns {
		out = append(out, dto.QuerySourceView{ID: c.ID, Name: c.Name, Engine: c.Engine, Env: c.Env, Status: c.Status})
	}
	return out
}

// bizSource 取一个业务查询源,不查调用者的访问范围(见 ApplySources)。
func (s *Services) bizSource(id int64) (*model.Connection, error) {
	conn, err := s.Repo.GetConnection(id)
	if err != nil {
		return nil, ErrNotFound
	}
	if !conn.BizQuery {
		return nil, grantInvalid("实例 %s 不是业务查询源,不能申请", conn.Name)
	}
	return conn, nil
}

// ApplySourceDatabases 列出一个业务查询源上的库名,供申请时选择。
func (s *Services) ApplySourceDatabases(id int64) ([]string, error) {
	conn, err := s.bizSource(id)
	if err != nil {
		return nil, err
	}
	return s.sourceDatabasesOf(conn)
}

// sourceDatabasesOf 是 SourceDatabases 去掉访问范围检查之后的那一半:列出一个业务查询源上的库名。
// 真实实例现查(失败退回缓存,见 liveSourceDatabases);没有凭据的开发连接用元数据缓存或种子树。
func (s *Services) sourceDatabasesOf(conn *model.Connection) ([]string, error) {
	if gateway.RealExecSupported(conn) {
		return s.liveSourceDatabases(conn)
	}
	if names, err := s.Repo.MetaDatabaseNames(conn.ID); err == nil && len(names) > 0 {
		return names, nil
	}
	// SIMULATED-PATH: schema-tree(与 ConnectionSchema 同一处):生产上不返回演示数据。
	if !gateway.SimulationAllowed() {
		return nil, gateway.ErrSimulationDisabled
	}
	objs, _ := s.Repo.SchemaForConnection(conn.ID)
	out := []string{}
	for _, o := range objs {
		if !containsStr(out, o.Database) {
			out = append(out, o.Database)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- 申请

// grantReasonMaxRunes 是申请理由最多能填多少字(前端输入框的 maxLength 与它一致)。比列宽
// model.GrantReasonSize 留一点余量:那是存储的硬上限,其它来源的理由写入时按它截断。
const grantReasonMaxRunes = 500

// ApplyQueryGrant 提交一次申请:生成申请记录与一张审批单。
func (s *Services) ApplyQueryGrant(u *model.User, req dto.QueryGrantApplyReq) (*dto.QueryGrantRequestView, error) {
	lim := s.BizQueryLimits()
	if lim.ApplyMode != BizApplyInternal {
		return nil, ErrApplyExternal
	}
	if u == nil || u.Kind == model.UserKindService {
		return nil, ErrForbidden
	}
	conn, err := s.bizSource(req.SourceID)
	if err != nil {
		return nil, err
	}
	dbName := strings.TrimSpace(req.Database)
	reason := strings.TrimSpace(req.Reason)
	switch {
	case dbName == "":
		return nil, grantInvalid("请选择库")
	case reason == "":
		return nil, grantInvalid("请填写申请理由")
	case len([]rune(reason)) > grantReasonMaxRunes:
		return nil, grantInvalid("申请理由不能超过 %d 个字", grantReasonMaxRunes)
	}
	if err := s.checkGrantTarget(u.ID, conn, dbName, req.Days, req.AllowExport, formGrantWords(func() error {
		return grantInvalid("数据查询的导出功能未开启,不能申请导出")
	})); err != nil {
		return nil, err
	}
	if _, ap, err := s.Repo.PendingQueryGrantRequest(u.ID, conn.ID, dbName); err != nil {
		return nil, err
	} else if ap != nil {
		return nil, grantInvalid("你对 %s/%s 已有一张待审批的申请 %s,请等它处理完或先撤回", conn.Name, dbName, ap.ApNo)
	}

	q := &model.QueryGrantRequest{
		UserID: u.ID, ConnectionID: conn.ID, DBName: dbName, Days: req.Days,
		AllowExport: req.AllowExport, Reason: fitRunes(reason, model.GrantReasonSize),
	}
	if err := s.Repo.CreateQueryGrantRequest(q); err != nil {
		return nil, err
	}
	ap, err := s.raiseGrantApproval(u, conn, q)
	if err != nil {
		// 没有单可批的申请永远不会有结果,人却看不出为什么:整次申请算失败。
		_ = s.Repo.DeleteQueryGrantRequest(q.ID)
		return nil, err
	}
	v := s.requestView(q, conn, ap)
	return &v, nil
}

// grantOwnedByLiveClient:这条授权由仍在启用的外部系统下发时,网关里不能改它 —— 否则外部系统
// 从此既列不出也撤不掉它(与开放接口里 M3 同一条规则)。
func (s *Services) grantOwnedByLiveClient(userID, connID int64, db string) error {
	g, err := s.Repo.GetQueryGrantByKey(userID, connID, db)
	if repository.IsNotFound(err) || (err == nil && g.ClientID == 0) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner, oerr := s.Repo.GetAPIClient(g.ClientID); oerr == nil && owner.Enabled {
		return grantInvalid("这条授权(#%d)由外部系统 %s 下发,只能在那里申请修改", g.ID, owner.Name)
	}
	return nil
}

// grantTargetWords 是 checkGrantTarget 查不过时对人说的话。三条路查的是同一组条件,说法不同:
// 申请人与管理员改了表单再提交,审批人则应当驳回。
type grantTargetWords struct {
	days    func(max int) error
	export  func() error
	listDBs func(conn *model.Connection, db string, err error) error
	noDB    func(conn *model.Connection, db string) error
}

// formGrantWords 是表单上(申请、管理员直接开通)的说法,只有导出那句两边不同。
func formGrantWords(export func() error) grantTargetWords {
	return grantTargetWords{
		days:    func(max int) error { return grantInvalid("有效期须在 1 到 %d 天之间", max) },
		export:  export,
		listDBs: func(_ *model.Connection, _ string, err error) error { return err },
		noDB: func(c *model.Connection, db string) error {
			return grantInvalid("实例 %s 上没有库 %s", c.Name, db)
		},
	}
}

// checkGrantTarget 是网关里三条路(申请、批准前预检、管理员直接开通)在给 (用户, 实例, 库) 下发
// 授权之前都要过的检查。集中在一处,预检才永远查得和申请时一样多(#184):有效期上限、导出开关、
// 库还在不在、这条授权是不是归仍启用的外部系统。行数 / 超时上限由 writeGatewayGrant 写入时再查。
func (s *Services) checkGrantTarget(userID int64, conn *model.Connection, db string, days int, allowExport bool, w grantTargetWords) error {
	lim := s.BizQueryLimits()
	if days < 1 || days > lim.MaxGrantDays {
		return w.days(lim.MaxGrantDays)
	}
	if allowExport && !lim.ExportEnabled {
		return w.export()
	}
	dbs, err := s.sourceDatabasesOf(conn)
	if err != nil {
		return w.listDBs(conn, db, err)
	}
	if !containsStr(dbs, db) {
		return w.noDB(conn, db)
	}
	return s.grantOwnedByLiveClient(userID, conn.ID, db)
}

func grantApprovalRef(conn *model.Connection, q *model.QueryGrantRequest) *model.RuleRef {
	code := model.ApGrantApply
	if q.AllowExport {
		code = model.ApGrantApplyExport
	}
	return model.NewRuleRef(code, "scope", conn.Name, "db", q.DBName, "days", strconv.Itoa(q.Days), "reason", q.Reason)
}

// raiseGrantApproval 为一次申请建审批单,并把单号记回申请。
func (s *Services) raiseGrantApproval(u *model.User, conn *model.Connection, q *model.QueryGrantRequest) (*model.Approval, error) {
	tierCode := ""
	if t, terr := s.tierOf(conn); terr == nil {
		tierCode = t.Code
	}
	ref := grantApprovalRef(conn, q)
	ap := &model.Approval{
		ApNo: s.nextApNo(), ConnectionID: conn.ID, Env: conn.Env, TierCode: tierCode,
		Instance: conn.Name, Database: fitRunes(q.DBName, 128),
		Command: model.RenderApText(ref), CommandRef: ref,
		Keyword: "QUERY-GRANT",
		// 申请人是发起人;审批链、超时策略、自审批开关全部复用既有那一套。
		InitiatorID: u.ID, Initiator: u.Name, Reason: q.Reason,
		// 中风险:它不改任何数据,但批下来的是一段时间里读这个库的权限。
		RiskLevel: model.RiskMid, Status: model.StatusPending, AuditID: s.nextAuditID(),
		QueryGrantReqID: q.ID,
	}
	steps := s.defaultChainSteps()
	if len(steps) > 0 {
		steps[0].Status = "active"
	}
	if err := s.Repo.CreateApproval(ap, steps); err != nil {
		slog.Error("数据查询权限申请建单失败", "request", q.ID, "err", err)
		return nil, err
	}
	if err := s.Repo.UpdateQueryGrantRequest(q.ID, map[string]any{"approval_id": ap.ID}); err != nil {
		return nil, err
	}
	q.ApprovalID = ap.ID
	s.recordAudit(u, conn, ap.Command, model.RiskMid, model.ResultPending, ap.ApNo, "intercept")
	s.Webhook.SendLarkApproval(ap)
	s.queueChatCards(ap, false)
	return ap, nil
}

// CancelQueryGrantRequest 申请人撤回自己还在等审批的申请。走审批单的撤回(CancelApproval),
// 规则与其它单子一样;另开这个入口只因为撤回审批单的接口挂在终端菜单下,业务方没有那个菜单。
func (s *Services) CancelQueryGrantRequest(u *model.User, reqID int64) error {
	q, err := s.Repo.GetQueryGrantRequest(reqID)
	if err != nil || q.UserID != u.ID || q.ApprovalID == 0 {
		return ErrNotFound
	}
	return s.CancelApproval(u, q.ApprovalID)
}

// MyQueryGrantRequests 列出自己最近的申请。
func (s *Services) MyQueryGrantRequests(u *model.User) []dto.QueryGrantRequestView {
	out := []dto.QueryGrantRequestView{}
	qs, err := s.Repo.ListQueryGrantRequests(u.ID, 20)
	if err != nil {
		return out
	}
	for i := range qs {
		conn, _ := s.Repo.GetConnection(qs[i].ConnectionID)
		ap, _ := s.Repo.GetApproval(qs[i].ApprovalID)
		out = append(out, s.requestView(&qs[i], conn, ap))
	}
	return out
}

func (s *Services) requestView(q *model.QueryGrantRequest, conn *model.Connection, ap *model.Approval) dto.QueryGrantRequestView {
	v := dto.QueryGrantRequestView{
		ID: q.ID, SourceID: q.ConnectionID, Database: q.DBName, Days: q.Days, AllowExport: q.AllowExport,
		Reason: q.Reason, GrantID: q.GrantID, Error: q.Error, CreatedAt: q.CreatedAt, Status: model.StatusPending,
	}
	if conn != nil {
		v.Source = conn.Name
	} else {
		v.Source = "#" + strconv.FormatInt(q.ConnectionID, 10)
	}
	if ap != nil {
		v.ApNo, v.Status = ap.ApNo, ap.Status
	}
	if v.Status == model.StatusApproved && q.GrantID == 0 && q.Error != "" {
		v.Status = "failed"
	}
	return v
}

// ---------------------------------------------------------------- 批准 → 生成授权

// grantRequestOf 取一张 grant 单对应的申请与实例。
func (s *Services) grantRequestOf(ap *model.Approval) (*model.QueryGrantRequest, *model.Connection, error) {
	q, err := s.Repo.GetQueryGrantRequest(ap.QueryGrantReqID)
	if err != nil {
		return nil, nil, err
	}
	conn, err := s.bizSource(q.ConnectionID)
	if err != nil {
		return q, nil, err
	}
	return q, conn, nil
}

// precheckGrantApproval 在批准**之前**按此刻的设置把申请重新校验一遍:提交到审批之间,实例可能
// 被取消了业务查询标记、库可能没了、全局上限可能改小了、导出可能关了。校验不过就不让批 ——
// 审批人看到原因,应当驳回;不能出现"批准了、却没有授权"的单子。
func (s *Services) precheckGrantApproval(ap *model.Approval) error {
	q, conn, err := s.grantRequestOf(ap)
	if err != nil {
		return err
	}
	// 申请方式改成外部审批系统之后,授权归外部系统管;这时批出来的授权归网关,外部系统既列不出
	// 也撤不掉它(#186)。
	if s.BizQueryLimits().ApplyMode != BizApplyInternal {
		return grantInvalid("数据查询权限已改为在外部审批系统申请,网关里的这张申请不能再批准,请驳回")
	}
	return s.checkGrantTarget(q.UserID, conn, q.DBName, q.Days, q.AllowExport, grantTargetWords{
		days: func(max int) error {
			return grantInvalid("申请的有效期 %d 天已超过当前上限 %d 天,请驳回后让申请人重新申请", q.Days, max)
		},
		export: func() error {
			return grantInvalid("数据查询的导出功能已关闭,这张含导出的申请不能批准,请驳回")
		},
		// 列不出库时同样不让批:核实不了不等于还在,批下去可能给一个已经没了的库写授权
		listDBs: func(c *model.Connection, db string, err error) error {
			return grantInvalid("暂时无法核实实例 %s 上的库 %s 是否还在(%v),请稍后再批", c.Name, db, err)
		},
		noDB: func(c *model.Connection, db string) error {
			return grantInvalid("实例 %s 上已没有库 %s,请驳回", c.Name, db)
		},
	})
}

// grantApprovedRequest 在审批单批准之后生成授权。审批状态已经落库;这里失败(极少:预检之后、
// 写入之前的一瞬间情况变了)就把原因记在申请上,并告诉申请人。
func (s *Services) grantApprovedRequest(ap *model.Approval, approverName string) {
	fail := func(err error) {
		slog.Error("数据查询权限申请已批准,生成授权失败", "apNo", ap.ApNo, "err", err)
		_ = s.Repo.UpdateQueryGrantRequest(ap.QueryGrantReqID, map[string]any{"error": fitRunes(err.Error(), 512)})
		// 单子已经批准:按"通过了的工单执行失败"归类,不能报成驳回
		s.notify(ap.InitiatorID, model.NotifExecFailed, apimsg.T("数据查询权限未能生效"),
			apimsg.Sprintf("申请 %s 已批准,但生成授权时失败:%s", ap.ApNo, err.Error()), ap.ApNo)
	}
	q, conn, err := s.grantRequestOf(ap)
	if err != nil {
		fail(err)
		return
	}
	user, err := s.Repo.GetUserByID(q.UserID)
	if err != nil {
		fail(err)
		return
	}
	now := time.Now()
	g, err := s.writeGatewayGrant(user, user, conn, q.DBName, gatewayGrant{
		Via: grantViaApproval, ExternalID: ap.ApNo, Approver: approverName, Reason: q.Reason,
		AllowExport: q.AllowExport, Starts: now, Ends: now.Add(time.Duration(q.Days) * 24 * time.Hour),
	})
	if err != nil {
		fail(err)
		return
	}
	_ = s.Repo.UpdateQueryGrantRequest(q.ID, map[string]any{"grant_id": g.ID, "error": ""})
}

// ---------------------------------------------------------------- 管理员直接开通

// AdminCreateQueryGrant 管理员在控制台给一个已有账号直接开通授权(不自动建号)。
func (s *Services) AdminCreateQueryGrant(actor *model.User, req dto.AdminQueryGrantReq) (*dto.QueryGrantView, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	user, err := s.Repo.FindUserByEmailCI(email)
	if repository.IsNotFound(err) {
		return nil, grantInvalid("网关里没有账号 %s,请先在【权限 · 用户】新建或邀请", email)
	} else if err != nil {
		return nil, err
	}
	conn, err := s.bizSource(req.SourceID)
	if err != nil {
		return nil, err
	}
	dbName := strings.TrimSpace(req.Database)
	if err := s.checkGrantTarget(user.ID, conn, dbName, req.Days, req.AllowExport, formGrantWords(func() error {
		return grantInvalid("数据查询的导出功能未开启,不能开通导出")
	})); err != nil {
		return nil, err
	}
	lim := s.BizQueryLimits()
	now := time.Now()
	g, err := s.writeGatewayGrant(actor, user, conn, dbName, gatewayGrant{
		Via: grantViaConsole, ExternalID: "console-" + s.nextAuditID(), Approver: actor.Name,
		Reason: strings.TrimSpace(req.Reason), MaxRows: req.MaxRows, TimeoutSec: req.TimeoutSec,
		AllowExport: req.AllowExport, Starts: now, Ends: now.Add(time.Duration(req.Days) * 24 * time.Hour),
	})
	if err != nil {
		return nil, err
	}
	v := s.grantView(g, user, conn, lim, now)
	return &v, nil
}

// ---------------------------------------------------------------- 共用的写入

// gatewayGrant 是网关自己(审批通过 / 管理员在控制台直接开通)下发一条授权时的条件与来源。
type gatewayGrant struct {
	Via                 string // grantViaApproval / grantViaConsole,写进 client_name
	ExternalID          string // 审批单号,或 console-<审计号>
	Approver, Reason    string
	MaxRows, TimeoutSec int
	AllowExport         bool
	Starts, Ends        time.Time
}

// writeGatewayGrant 按 (被授权人, 实例, 库) 创建或更新一条网关自己下发的授权。
func (s *Services) writeGatewayGrant(actor, user *model.User, conn *model.Connection, dbName string, c gatewayGrant) (*model.QueryGrant, error) {
	lim := s.BizQueryLimits()
	if err := checkGrantLimits(c.MaxRows, c.TimeoutSec, c.Starts, c.Ends, time.Now(), lim); err != nil {
		return nil, err
	}
	if err := s.ensureBizQueryRole(user, nil, actor, actor.Name, c.Via); err != nil {
		return nil, err
	}
	g, gerr := s.Repo.GetQueryGrantByKey(user.ID, conn.ID, dbName)
	created := false
	switch {
	case repository.IsNotFound(gerr):
		g = &model.QueryGrant{UserID: user.ID, ConnectionID: conn.ID, DBName: dbName}
		created = true
	case gerr != nil:
		return nil, gerr
	}
	if g.ExternalID != "" && !created {
		_ = s.Repo.RecordQueryGrantApproval(g.ClientID, g.ExternalID, g.ID)
	}
	g.MaxRows, g.TimeoutSec, g.AllowExport = c.MaxRows, c.TimeoutSec, c.AllowExport
	g.StartsAt, g.EndsAt = c.Starts, c.Ends
	g.Status, g.RevokedAt, g.RevokedBy = model.GrantActive, nil, ""
	g.ClientID, g.ClientName, g.ExternalID = 0, c.Via, fitRunes(c.ExternalID, 128)
	g.Approver, g.Reason = fitRunes(c.Approver, 128), fitRunes(c.Reason, model.GrantReasonSize)
	if err := s.Repo.SaveQueryGrant(g); err != nil {
		return nil, err
	}
	if err := s.Repo.RecordQueryGrantApproval(0, g.ExternalID, g.ID); err != nil {
		return nil, err
	}
	verb := "更新"
	if created {
		verb = "下发"
	}
	s.recordAuditBy(actor, actor.Name, conn, fmt.Sprintf(
		"bizquery.grant %s查询授权 #%d:%s → %s/%s,行数上限 %s,超时 %s,导出 %v,有效期 %s ~ %s · 来源 %s · 单号 %s · 审批人 %s",
		verb, g.ID, user.Email, conn.Name, dbName, limitText(g.MaxRows, lim.DefaultMaxRows), limitText(g.TimeoutSec, lim.DefaultTimeout)+"s",
		g.AllowExport, c.Starts.Format("2006-01-02 15:04"), c.Ends.Format("2006-01-02 15:04"),
		c.Via, g.ExternalID, orDash(g.Approver)), model.RiskMid, model.ResultExecuted, "", "")
	return g, nil
}
