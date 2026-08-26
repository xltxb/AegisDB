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

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// CompileObject recompiles one stored program and reports what Oracle then
// thinks of it. scope is the owner/schema; kind is package/procedure/…
func (s *Services) CompileObject(u *model.User, connID int64, scope, kind, name, database string) (*gateway.CompileReport, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	if database = strings.TrimSpace(database); database != "" {
		conn.Database = database
	}
	if conn.Status == "maint" {
		return nil, fmt.Errorf("目标实例处于维护态,暂不能编译")
	}
	// 先把请求本身判清楚,再谈基础设施:引擎对不对、名字合不合法、这个类型有没有
	// 编译单元 —— 这些错误说得出"哪里不对",而"没配凭据"说不出。
	if !gateway.IsOracleEngine(conn.Engine) {
		return nil, fmt.Errorf("包编译是 Oracle 专有能力,当前实例 %s 的引擎为 %s", conn.Name, conn.Engine)
	}
	// 判定用的就是即将执行的那几条语句本身 —— 判什么、执行什么,一字不差。
	stmts, err := gateway.OracleCompileStatements(scope, kind, name)
	if err != nil {
		return nil, err
	}
	if !gateway.RealExecSupported(conn) {
		// 模拟连接下没有可编译的东西。假装编译成功,是这个功能最坏的失败方式:
		// DBA 会以为线上那个 INVALID 的包已经修好了。
		return nil, fmt.Errorf("该实例未配置真实执行凭据,无法编译(编译必须发生在真实库上)")
	}
	v := s.strictestVerdict(u, conn, stmts)
	label := fmt.Sprintf("COMPILE %s %s", strings.ToUpper(kind), strings.TrimSpace(name))
	switch {
	case v.Action == gateway.ActionDeny:
		s.recordAudit(u, conn, label, model.RiskHigh, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden
	case v.RequiresApproval():
		s.recordAudit(u, conn, label, v.Risk, model.ResultRejected, "", "intercept")
		return nil, fmt.Errorf("该实例上的编译命中「%s」需审批,请通过发布单执行", v.Rule)
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
	return rep, nil
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
