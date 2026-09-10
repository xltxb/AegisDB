package service

// Oracle 无效对象:清点 + 批量重编译。
//
// 和单个编译(oracle_compile.go)是同一件事做 N 次,所以守的是同一条线:
//
//   - 执行走 gateway.RecompileTargets,而它内部逐个调 RealCompileObject —— 批量**不是**
//     一条新的下发通道。新开一条,就等于给自己留一个不回读 all_objects.status 的后门。
//   - **判定一次,判的是全部语句。** 把这一批要执行的 ALTER 全部拼出来交给同一个
//     strictestVerdict:只要其中任何一条在这台实例上是 deny 或需审批,整批都不执行。
//     逐个判、能编的先编,是最糟的形态 —— 它会在一个需要审批的库上留下"编了一半"。
//   - **逐个记审计**,不是记一行"批量编译了 12 个"。审计要回答的是"三个月后:那天
//     凌晨到底动了哪些对象、结果如何",一行汇总答不了。每个对象记一行,记的是它这次
//     的**最终**状态(中间轮次不单独记,与单个编译一次记一行保持一致)。

import (
	"fmt"
	"strings"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// oracleObjectConn 是清点/批量编译共用的前置检查。
//
// 顺序是刻意的:先把说得清楚的错误说出来(引擎不对、实例在维护),再谈凭据 ——
// "没配凭据"是最没有信息量的一句,不该盖住前面那些。
func (s *Services) oracleObjectConn(u *model.User, connID int64, database string) (*model.Connection, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	applyTargetDatabase(conn, database)
	if !gateway.IsOracleEngine(conn.Engine) {
		return nil, fmt.Errorf("无效对象重编译是 Oracle 专有能力,当前实例 %s 的引擎为 %s", conn.Name, conn.Engine)
	}
	if !gateway.RealExecSupported(conn) {
		// 模拟连接下返回一个空清单,等于对着一个可能满是 INVALID 的库说"很干净"。
		// 这是这个功能最坏的失败方式,所以宁可报错。
		return nil, fmt.Errorf("该实例未配置真实执行凭据,无法清点无效对象")
	}
	return conn, nil
}

// InvalidObjects 清点一个 schema 下所有 INVALID 的可编译对象。只读,不判 DDL。
func (s *Services) InvalidObjects(u *model.User, connID int64, scope, database string) ([]gateway.InvalidObject, error) {
	conn, err := s.oracleObjectConn(u, connID, database)
	if err != nil {
		return nil, err
	}
	return gateway.RealInvalidObjects(conn, scope)
}

// RecompileInvalid 重新编译一个 schema 下的无效对象。
//
// only 为空表示"全部";非空时只处理名字在其中的那些(界面上勾选了几个的情形)。
func (s *Services) RecompileInvalid(u *model.User, connID int64, scope, database string, only []string) (*gateway.RecompileReport, error) {
	conn, err := s.oracleObjectConn(u, connID, database)
	if err != nil {
		return nil, err
	}
	if conn.Status == "maint" {
		return nil, fmt.Errorf("目标实例处于维护态,暂不能编译")
	}

	all, err := gateway.RealInvalidObjects(conn, scope)
	if err != nil {
		return nil, err
	}
	targets := filterInvalidByName(all, only)
	if len(targets) == 0 {
		// 没有无效对象是个好消息,不是错误 —— 返回一份空报告让界面照常显示。
		return &gateway.RecompileReport{Owner: strings.ToUpper(strings.TrimSpace(scope))}, nil
	}

	// 超出上限的部分**明确报告为跳过**。悄悄只做一半的后果是有人以为清干净了。
	skipped := 0
	if len(targets) > gateway.MaxRecompileTargets() {
		skipped = len(targets) - gateway.MaxRecompileTargets()
		targets = targets[:gateway.MaxRecompileTargets()]
	}

	// 判定用的就是即将执行的那些语句本身 —— 判什么、执行什么,一字不差。
	stmts := make([]string, 0, len(targets)*2)
	for _, o := range targets {
		st, serr := gateway.OracleCompileStatements(o.Owner, o.Kind, o.Name)
		if serr != nil {
			// 名字拼不进编译语句的对象直接不进这一批(而不是让整批失败):它多半是
			// 一个带特殊字符的引用标识符,别的对象不该被它连累。
			continue
		}
		stmts = append(stmts, st...)
	}
	if len(stmts) == 0 {
		return nil, fmt.Errorf("这批无效对象的名字都无法安全拼入编译语句,已全部拒绝")
	}

	v := s.strictestVerdict(u, conn, stmts)
	label := fmt.Sprintf("RECOMPILE INVALID %s (%d 个对象)",
		strings.ToUpper(strings.TrimSpace(targets[0].Owner)), len(targets))
	switch {
	case v.Action == gateway.ActionDeny:
		s.recordAudit(u, conn, label, model.RiskHigh, model.ResultRejected, "", "intercept")
		return nil, ErrForbidden
	case v.RequiresApproval():
		s.recordAudit(u, conn, label, v.Risk, model.ResultRejected, "", "intercept")
		return nil, fmt.Errorf("该实例上的编译命中「%s」需审批,请通过发布单执行", v.Rule)
	}

	rep := gateway.RecompileTargets(conn, targets, func(o gateway.InvalidObject, r *gateway.CompileReport, cerr error) {
		one := fmt.Sprintf("COMPILE %s %s", strings.ToUpper(o.Kind), o.Name)
		if cerr != nil {
			s.recordAudit(u, conn, one+" :: "+strings.TrimSpace(cerr.Error()), v.Risk, model.ResultWarn, "", "exec")
			return
		}
		result := model.ResultExecuted
		if !r.OK() {
			result = model.ResultWarn
		}
		s.recordAudit(u, conn, one+compileAuditSuffix(r), v.Risk, result, "", "exec")
	})
	rep.Skipped = skipped
	return rep, nil
}

// filterInvalidByName 按名字筛选。名字比较不分大小写:界面上传回来的可能是用户看到的
// 大写形态,也可能是原样回传,而 Oracle 的数据字典里存的是大写。
func filterInvalidByName(all []gateway.InvalidObject, only []string) []gateway.InvalidObject {
	if len(only) == 0 {
		return all
	}
	want := make(map[string]bool, len(only))
	for _, n := range only {
		if n = strings.ToUpper(strings.TrimSpace(n)); n != "" {
			want[n] = true
		}
	}
	out := make([]gateway.InvalidObject, 0, len(only))
	for _, o := range all {
		if want[strings.ToUpper(o.Name)] {
			out = append(out, o)
		}
	}
	return out
}
