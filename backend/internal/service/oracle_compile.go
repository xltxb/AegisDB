package service

// Oracle 包/存储程序的重新编译。
//
// 编译是一次 **DDL** —— 它改变目标库里对象的状态,失败还会把对象留成 INVALID。
// 所以它和终端里敲一条 ALTER 走完全相同的三层闸门:菜单权限、能力矩阵(ddl 维度)、
// 高危命令字典 + 严格模式,并且逐条记审计。这一点不能省:网关里每一条能改动目标库
// 的通道最终都补上了同一次判定(ER6 是异步执行通道漏判的那次教训),对象浏览器上
// 的一个按钮没有理由成为例外。
//
// 判定被拒时不"降级执行",而是把话说清楚:需要审批的实例请走发布单 —— 编译是
// 一次性动作,没有审批单据承载它,悄悄执行等于绕过。

import (
	"fmt"
	"strings"

	"velagateway/internal/apimsg"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// CompileObject recompiles one stored program and reports what Oracle then
// thinks of it. scope is the owner/schema; kind is package/procedure/…
func (s *Services) CompileObject(u *model.User, connID int64, scope, kind, name, database, mfaCode string) (*gateway.CompileReport, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	applyTargetDatabase(conn, database)
	if conn.Status == model.ConnStatusMaint {
		return nil, apimsg.Errorf("目标实例处于维护态,暂不能编译")
	}
	// 编译会把库上的对象改成 VALID/INVALID —— 它**改变生产状态**,和终端里执行一条
	// DDL 没有区别。这条路原先整个绕开了生产步进验证(L-7):同一个人在终端里要输码,
	// 换到对象树上点「编译」就不用。闸要么两边都有,要么哪边都别有。
	if err := s.checkMFA(u, conn, mfaCode); err != nil {
		return nil, err
	}
	// 先把请求本身判清楚,再谈基础设施:引擎对不对、名字合不合法、这个类型有没有
	// 编译单元 —— 这些错误说得出"哪里不对",而"没配凭据"说不出。
	if !gateway.IsOracleEngine(conn.Engine) {
		return nil, apimsg.Errorf("包编译是 Oracle 专有能力,当前实例 %s 的引擎为 %s", conn.Name, conn.Engine)
	}
	// 判定用的就是即将执行的那几条语句本身 —— 判什么、执行什么,一字不差。
	stmts, err := gateway.OracleCompileStatements(scope, kind, name)
	if err != nil {
		return nil, err
	}
	if !gateway.RealExecSupported(conn) {
		// 模拟连接下没有可编译的东西。假装编译成功,是这个功能最坏的失败方式:
		// DBA 会以为线上那个 INVALID 的包已经修好了。
		return nil, apimsg.Errorf("该实例未配置真实执行凭据,无法编译(编译必须发生在真实库上)")
	}
	v := s.strictestVerdict(u, compileJudgeConn(conn, scope), stmts)
	label := fmt.Sprintf("COMPILE %s %s", strings.ToUpper(kind), strings.TrimSpace(name))
	switch {
	case v.Action == gateway.ActionDeny:
		s.recordAudit(u, conn, label, model.RiskHigh, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden
	case v.RequiresApproval():
		s.recordAudit(u, conn, label, v.Risk, model.ResultRejected, "", "intercept")
		return nil, apimsg.Errorf("该实例上的编译命中「%s」需审批,请通过发布单执行", v.Rule)
	}

	rep, cerr := gateway.RealCompileObject(conn, strings.TrimSpace(scope), kind, name)
	if cerr != nil {
		s.recordAudit(u, conn, label, v.Risk, model.ResultWarn, "", "exec")
		return nil, cerr
	}
	// 审计结果按 Oracle 的裁决记,而不是按"调用有没有报错"记:
	// 编译出错时对象被留成 INVALID,那不是一次成功的操作。
	result := model.ResultExecuted
	if !rep.OK() {
		result = model.ResultWarn
	}
	s.recordAudit(u, conn, label+compileAuditSuffix(rep), v.Risk, result, "", "exec")
	if rep.OK() {
		s.compileDependents(u, conn, strings.TrimSpace(scope), kind, name, rep)
	}
	return rep, nil
}

// compileJudgeConn 是判定编译语句时用的连接:请求没带 schema(对象树上 Oracle 不传 database)
// 时,把被编译对象的 owner 当作目标 schema。
//
// 编译语句写的是 "OWNER"."NAME",落在哪个 schema 是确定的;但窗口按目标 schema 找,
// TargetSchema 为空时一扇都找不到,班车时段里点「编译」照样被拒成"需审批"(2026-10-26)。
// 只用于判定 —— 返回的是副本,执行用的连接不动,会话里不会因此多一次 ALTER SESSION。
func compileJudgeConn(conn *model.Connection, owner string) *model.Connection {
	owner = strings.TrimSpace(owner)
	if conn == nil || owner == "" || strings.TrimSpace(conn.TargetSchema) != "" {
		return conn
	}
	c := *conn
	c.TargetSchema = owner
	return &c
}

// compileDependents 是单个编译之后的连带编译:把直接或间接依赖这个对象、此刻处于
// INVALID 的对象按依赖分层编掉。
//
// 只在本对象编成 VALID 之后做 —— 它自己还是 INVALID,依赖它的对象编了也只会再报一遍
// "PLS-00905 object … is invalid",那是噪音。
//
// 它和批量重编译走同一段(recompileJudged):整批判定、逐个审计。连带编译改动的是用户
// 没有点名的对象,所以绝不能比批量那条路宽松。它的任何失败都只写进 CascadeNote,
// 不影响本对象已经成功的那次编译。
func (s *Services) compileDependents(u *model.User, conn *model.Connection, scope, kind, name string, rep *gateway.CompileReport) {
	root := gateway.InvalidObject{
		Owner: strings.ToUpper(scope),
		Name:  strings.ToUpper(strings.TrimSpace(name)),
		Kind:  strings.ToLower(strings.TrimSpace(kind)),
	}
	deps, err := gateway.RealInvalidDependents(conn, root)
	if err != nil {
		m := apimsg.Sprintf("未能读取依赖对象,未连带编译: %s", strings.TrimSpace(err.Error()))
		rep.CascadeNote, rep.CascadeNoteRef = m.S, m.Ref
		return
	}
	if len(deps) == 0 {
		return
	}
	skipped := 0
	if len(deps) > gateway.MaxRecompileTargets() {
		skipped = len(deps) - gateway.MaxRecompileTargets()
		deps = deps[:gateway.MaxRecompileTargets()]
	}
	label := fmt.Sprintf("RECOMPILE DEPENDENTS OF %s %s (%d 个对象)", strings.ToUpper(root.Kind), root.Name, len(deps))
	cascade, err := s.recompileJudged(u, conn, deps, label)
	if err != nil {
		if err == ErrForbidden {
			m := apimsg.T("连带编译被规则禁止,依赖对象未编译")
			rep.CascadeNote, rep.CascadeNoteRef = m.S, m.Ref
		} else {
			m := apimsg.Sprintf("连带编译未执行: %s", err)
			rep.CascadeNote, rep.CascadeNoteRef = m.S, m.Ref
		}
		return
	}
	cascade.Skipped = skipped
	rep.Cascade = cascade
}

// compileAuditSuffix puts the outcome INTO the audit line: a chain entry saying
// only "COMPILE PACKAGE X" cannot answer "did it come back valid?" months later.
func compileAuditSuffix(rep *gateway.CompileReport) string {
	if rep == nil {
		return ""
	}
	parts := make([]string, 0, len(rep.Targets))
	for _, t := range rep.Targets {
		parts = append(parts, t.Type+"="+t.Status)
	}
	out := " :: " + strings.Join(parts, ", ")
	if n := len(rep.Errors); n > 0 {
		out += fmt.Sprintf(" · %d 条编译错误", n)
	}
	return out
}
