package bootstrap

// 在网关里申请、审批、直接开通数据查询授权(#173)。
//
// 钉住的是:申请生成一张走默认审批链的单;批准即生成授权(来源「网关审批」,单号即审批单号),
// 驳回 / 撤回不生成;批准前按此刻的设置重新校验;越不过全局上限;不能改由仍启用的外部系统下发
// 的授权;申请方式切到外部时网关不收申请;管理员可以直接给已有账号开通。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
)

type grantRequestView struct {
	ID       int64  `json:"id"`
	Source   string `json:"source"`
	Database string `json:"database"`
	Days     int    `json:"days"`
	ApNo     string `json:"apNo"`
	Status   string `json:"status"`
	GrantID  int64  `json:"grantId"`
	Error    string `json:"error"`
}

// applicant 建一个只有业务查询角色的账号,返回它和它的会话。
func (e *grantEnv) applicant(email string) (*model.User, string) {
	e.app.t.Helper()
	role, _ := e.app.repo.GetRoleByCode(model.RoleCodeBizQuery)
	u := &model.User{Name: strings.Split(email, "@")[0], Email: email, Status: "active", Kind: model.UserKindHuman, RoleID: role.ID, Initials: "BQ", Dept: "业务方", LastActive: "—"}
	if err := e.app.repo.CreateUser(u); err != nil {
		e.app.t.Fatal(err)
	}
	if err := e.app.repo.SetUserRoles(u.ID, []int64{role.ID}); err != nil {
		e.app.t.Fatal(err)
	}
	return u, e.app.bizSession(u)
}

func (e *grantEnv) apply(tok string, body map[string]any) apiResp {
	return e.app.do(http.MethodPost, "/api/v1/bizquery/apply", tok, body)
}

func (e *grantEnv) applyBody(db string) map[string]any {
	return map[string]any{"sourceId": e.source, "database": db, "days": 7, "reason": "月度对账"}
}

func requestOf(t *testing.T, r apiResp) grantRequestView {
	t.Helper()
	if r.Code != 0 {
		t.Fatalf("申请应成功,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	var v grantRequestView
	_ = json.Unmarshal(r.Data, &v)
	return v
}

func (e *grantEnv) mine(tok string) []grantRequestView {
	var out []grantRequestView
	_ = json.Unmarshal(e.app.do(http.MethodGet, "/api/v1/bizquery/apply/mine", tok, nil).Data, &out)
	return out
}

func (e *grantEnv) grantsOf(email string) []grantView {
	var out struct {
		Items []grantView `json:"items"`
	}
	_ = json.Unmarshal(e.app.do(http.MethodGet, "/api/v1/query-grants?email="+email, e.admin, nil).Data, &out)
	return out.Items
}

// 申请 → 生成审批单 → 另一个人批准 → 授权生效。
func TestGrantApply_ApproveCreatesGrant(t *testing.T) {
	e := newGrantEnv(t)
	_, tok := e.applicant("buyer@corp.io")

	// 能选的只有业务查询源,主库不在里面
	var sources []struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(e.app.do(http.MethodGet, "/api/v1/bizquery/apply/sources", tok, nil).Data, &sources)
	ids := map[int64]bool{}
	for _, s := range sources {
		ids[s.ID] = true
	}
	if !ids[e.source] || ids[e.main] {
		t.Fatalf("可申请的实例应当只有业务查询源,实际 %v", sources)
	}
	var dbs []string
	_ = json.Unmarshal(e.app.do(http.MethodGet, "/api/v1/bizquery/apply/sources/"+itoa(e.source)+"/databases", tok, nil).Data, &dbs)
	if !containsString(dbs, e.db) {
		t.Fatalf("库列表里应当有 %s,实际 %v", e.db, dbs)
	}

	req := requestOf(t, e.apply(tok, e.applyBody(e.db)))
	eq(t, req.Status, "pending", "提交后待审批")
	if req.ApNo == "" {
		t.Fatal("申请应当生成审批单")
	}
	// 同一个库还在等的时候不能再交一张
	if r := e.apply(tok, e.applyBody(e.db)); r.Code == 0 || !strings.Contains(r.Msg, req.ApNo) {
		t.Errorf("重复申请应被拒并指出已有的单,实际 code=%d msg=%q", r.Code, r.Msg)
	}

	// 单子在审批列表里是 grant 类型,不是命令单
	ap, _ := e.app.repo.GetApprovalByApNo(req.ApNo)
	eq(t, model.ApprovalType(ap), model.ApTypeGrant, "审批单类型")
	listed := func(typ string) bool {
		var page struct {
			Items []struct {
				ApNo string `json:"apNo"`
			} `json:"items"`
		}
		_ = json.Unmarshal(e.app.do(http.MethodGet, "/api/v1/approvals?type="+typ, e.admin, nil).Data, &page)
		for _, it := range page.Items {
			if it.ApNo == req.ApNo {
				return true
			}
		}
		return false
	}
	if !listed("grant") || listed("cmd") {
		t.Error("按类型筛选:应在 grant 里、不在 cmd 里")
	}

	e.app.decideWindowTicket(req.ApNo, true)

	got := e.mine(tok)
	if len(got) == 0 || got[0].Status != "approved" || got[0].GrantID == 0 {
		t.Fatalf("批准后申请应显示已通过并带授权号,实际 %+v", got)
	}
	gs := e.grantsOf("buyer@corp.io")
	if len(gs) != 1 || gs[0].Status != "active" || gs[0].Client != "网关审批" || gs[0].ExternalID != req.ApNo || gs[0].Database != e.db {
		t.Fatalf("应生成一条来源为网关审批、单号为审批单号的授权,实际 %+v", gs)
	}
	// 批准即生效:没有可以手动执行的命令
	if r := e.app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/execute", e.admin, nil); r.Code == 0 {
		t.Error("权限申请单不能手动执行")
	}
	if auditCount(e.app, "command LIKE ?", "bizquery.grant 下发查询授权%来源 网关审批%") == 0 {
		t.Error("生成授权应写审计")
	}
}

// 驳回、撤回:不生成授权;申请人看得到结论。
func TestGrantApply_RejectAndCancel(t *testing.T) {
	e := newGrantEnv(t)
	_, tok := e.applicant("buyer@corp.io")

	first := requestOf(t, e.apply(tok, e.applyBody("finance_db")))
	e.app.decideWindowTicket(first.ApNo, false)

	second := requestOf(t, e.apply(tok, e.applyBody("orders_db")))
	eq(t, e.app.do(http.MethodPost, "/api/v1/bizquery/apply/"+itoa(second.ID)+"/cancel", tok, nil).Code, 0, "申请人撤回")

	status := map[string]string{}
	for _, r := range e.mine(tok) {
		status[r.ApNo] = r.Status
	}
	eq(t, status[first.ApNo], "rejected", "驳回")
	eq(t, status[second.ApNo], "cancelled", "撤回")
	eq(t, len(e.grantsOf("buyer@corp.io")), 0, "驳回、撤回都不生成授权")

	// 别人撤不了我的申请
	_, other := e.applicant("other@corp.io")
	third := requestOf(t, e.apply(tok, e.applyBody("finance_db")))
	if r := e.app.do(http.MethodPost, "/api/v1/bizquery/apply/"+itoa(third.ID)+"/cancel", other, nil); r.Code == 0 {
		t.Error("不该能撤回别人的申请")
	}
}

// 提交时的校验:只有业务查询源、真实存在的库、全局上限之内、要写理由、导出要全局开着。
func TestGrantApply_Refusals(t *testing.T) {
	e := newGrantEnv(t)
	_, tok := e.applicant("buyer@corp.io")
	for name, body := range map[string]map[string]any{
		"主库不能申请":     {"sourceId": e.main, "database": "finance_db", "days": 7, "reason": "x"},
		"库不存在":       {"sourceId": e.source, "database": "nope_db", "days": 7, "reason": "x"},
		"超过最长有效期":    {"sourceId": e.source, "database": e.db, "days": 9999, "reason": "x"},
		"有效期为 0":     {"sourceId": e.source, "database": e.db, "days": 0, "reason": "x"},
		"没写理由":       {"sourceId": e.source, "database": e.db, "days": 7, "reason": "  "},
		"导出没开却申请了导出": {"sourceId": e.source, "database": e.db, "days": 7, "reason": "x", "allowExport": true},
	} {
		if r := e.apply(tok, body); r.Code == 0 {
			t.Errorf("%s:应被拒", name)
		}
	}
	eq(t, len(e.mine(tok)), 0, "被拒的申请不留记录")

	// 申请方式切到外部审批系统:网关不收申请
	eq(t, e.app.do(http.MethodPut, "/api/v1/settings", e.admin, map[string]any{"bizQuery.applyMode": "external"}).Code, 0, "切到外部")
	if r := e.apply(tok, e.applyBody(e.db)); r.Code == 0 || !strings.Contains(r.Msg, "外部审批系统") {
		t.Errorf("外部申请方式下网关不收申请,实际 code=%d msg=%q", r.Code, r.Msg)
	}
	if r := e.app.do(http.MethodPut, "/api/v1/settings", e.admin, map[string]any{"bizQuery.applyMode": "whatever"}); r.Code == 0 {
		t.Error("申请方式只认 internal / external")
	}
}

// 提交到审批之间情况变了(实例被取消标记):不让批,原因给审批人看;驳回照常。
func TestGrantApply_PrecheckBlocksStaleApproval(t *testing.T) {
	e := newGrantEnv(t)
	_, tok := e.applicant("buyer@corp.io")
	req := requestOf(t, e.apply(tok, e.applyBody(e.db)))
	eq(t, e.app.do(http.MethodPatch, "/api/v1/connections/"+itoa(e.source), e.admin, map[string]any{"bizQuery": false}).Code, 0, "取消业务查询源标记")

	approver := e.app.login("zhangwei@aegisdb.io", "aegisdb123")
	ap, _ := e.app.repo.GetApprovalByApNo(req.ApNo)
	r := e.app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil)
	if r.Code == 0 || !strings.Contains(r.Msg, "不是业务查询源") {
		t.Fatalf("实例不再是业务查询源时不能批准,并说明原因,实际 code=%d msg=%q", r.Code, r.Msg)
	}
	ap, _ = e.app.repo.GetApprovalByApNo(req.ApNo)
	eq(t, ap.Status, model.StatusPending, "没批成,单子还在待审批")
	e.app.decideWindowTicket(req.ApNo, false)
	eq(t, len(e.grantsOf("buyer@corp.io")), 0, "没有生成授权")
}

// 批准前要核实库还在;实例此刻列不出库时同样不让批 —— 核实不了不等于还在(#179)。
func TestGrantApply_PrecheckRefusesWhenDatabasesCannotBeListed(t *testing.T) {
	e := newGrantEnv(t)
	_, tok := e.applicant("buyer@corp.io")
	req := requestOf(t, e.apply(tok, e.applyBody(e.db)))

	// 元数据缓存清空、且不许走模拟路径:实例的库列不出来
	if err := e.app.repo.DB().Where("connection_id = ?", e.source).Delete(&model.MetaTable{}).Error; err != nil {
		t.Fatal(err)
	}
	prev := gateway.AllowSimulation
	gateway.AllowSimulation = false
	t.Cleanup(func() { gateway.AllowSimulation = prev })

	approver := e.app.login("zhangwei@aegisdb.io", "aegisdb123")
	ap, _ := e.app.repo.GetApprovalByApNo(req.ApNo)
	r := e.app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil)
	if r.Code == 0 || !strings.Contains(r.Msg, "无法核实") {
		t.Fatalf("列不出库时不能批准,并说明原因,实际 code=%d msg=%q", r.Code, r.Msg)
	}
	ap, _ = e.app.repo.GetApprovalByApNo(req.ApNo)
	eq(t, ap.Status, model.StatusPending, "没批成,单子还在待审批")
	eq(t, len(e.grantsOf("buyer@corp.io")), 0, "没有生成授权")
}

// 批准了却没能生成授权:通知申请人"未生效",按执行失败归类,不混进"驳回"(#180)。
func TestGrantApply_FailedGrantNotifiesAsExecFailed(t *testing.T) {
	e := newGrantEnv(t)
	u, tok := e.applicant("buyer@corp.io")
	req := requestOf(t, e.apply(tok, e.applyBody(e.db)))

	// 预检之后、写入之前情况变了:申请人的账号没了,授权写不进去
	ap, _ := e.app.repo.GetApprovalByApNo(req.ApNo)
	if err := e.app.repo.DB().Delete(&model.User{}, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	approver := e.app.login("zhangwei@aegisdb.io", "aegisdb123")
	eq(t, e.app.do(http.MethodPost, "/api/v1/approvals/"+itoa(ap.ID)+"/approve", approver, nil).Code, 0, "批准")

	ns, _ := e.app.repo.ListNotifications(u.ID, 20)
	var got []string
	for _, n := range ns {
		got = append(got, n.Type)
		if n.RefNo == req.ApNo && n.Type == model.NotifApprovalRejected {
			t.Fatalf("已批准的单不能按驳回通知: %+v", n)
		}
	}
	for _, n := range ns {
		if n.RefNo == req.ApNo && n.Type == model.NotifExecFailed {
			return
		}
	}
	t.Fatalf("应有一条 exec-failed 通知,实际类型 %v", got)
}

// 由仍启用的外部系统下发的授权,网关里既不能申请修改,也不能直接开通覆盖。
func TestGrantApply_DoesNotOverrideLiveExternalGrant(t *testing.T) {
	e := newGrantEnv(t)
	u, tok := e.applicant("buyer@corp.io")
	_, ok := grantOf(t, e.put(e.body(u.Email, "EXT-1")))
	if !ok {
		t.Fatal("前提:外部系统下发一条授权")
	}
	if r := e.apply(tok, e.applyBody(e.db)); r.Code == 0 || !strings.Contains(r.Msg, "审批系统") {
		t.Errorf("外部系统下发的授权不能在网关里申请修改,实际 code=%d msg=%q", r.Code, r.Msg)
	}
	r := e.app.do(http.MethodPost, "/api/v1/query-grants", e.admin, map[string]any{
		"email": u.Email, "sourceId": e.source, "database": e.db, "days": 7, "reason": "x",
	})
	if r.Code == 0 {
		t.Error("外部系统下发的授权不能被控制台覆盖")
	}
}

// 管理员直接开通:只给已有账号;来源记「控制台」;顺带补上业务查询角色;非管理员不行。
func TestAdminCreateQueryGrant(t *testing.T) {
	e := newGrantEnv(t)
	create := func(tok string, body map[string]any) apiResp {
		return e.app.do(http.MethodPost, "/api/v1/query-grants", tok, body)
	}
	body := map[string]any{"email": "chenhao@aegisdb.io", "sourceId": e.source, "database": e.db, "days": 30, "maxRows": 500, "reason": "季度审计"}

	r := create(e.admin, body)
	eq(t, r.Code, 0, "直接开通: "+r.Msg)
	var g grantView
	_ = json.Unmarshal(r.Data, &g)
	if g.Status != "active" || g.Client != "控制台" || g.MaxRows != 500 {
		t.Fatalf("应开通一条来源为控制台的授权,实际 %+v", g)
	}
	ch, _ := e.app.repo.GetUserByEmailFold("chenhao@aegisdb.io")
	role, _ := e.app.repo.GetRoleByCode(model.RoleCodeBizQuery)
	ids := e.app.repo.EffectiveRoleIDs(ch)
	if !containsInt64(ids, role.ID) || ids[0] == role.ID {
		t.Errorf("应追加业务查询角色且不动主角色,实际 %v", ids)
	}

	for name, b := range map[string]map[string]any{
		"账号不存在":  {"email": "ghost@corp.io", "sourceId": e.source, "database": e.db, "days": 7},
		"主库":     {"email": "chenhao@aegisdb.io", "sourceId": e.main, "database": "finance_db", "days": 7},
		"超过行数上限": {"email": "chenhao@aegisdb.io", "sourceId": e.source, "database": e.db, "days": 7, "maxRows": 99999999},
	} {
		if r := create(e.admin, b); r.Code == 0 {
			t.Errorf("%s:应被拒", name)
		}
	}
	ro := e.app.login("chenhao@aegisdb.io", "aegisdb123")
	if r := create(ro, body); r.Code == 0 {
		t.Error("非管理员不能直接开通")
	}
}

// 新装默认网关内申请,也不再带任何外部申请页。
func TestBizQuery_ApplyModeDefaultsToInternal(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	var d struct {
		Limits struct {
			ApplyMode string `json:"applyMode"`
			ApplyURL  string `json:"applyUrl"`
		} `json:"limits"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/bizquery/grants", admin, nil).Data, &d)
	eq(t, d.Limits.ApplyMode, "internal", "默认网关内申请")
	eq(t, d.Limits.ApplyURL, "", "默认没有外部申请页")
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func containsInt64(list []int64, v int64) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
