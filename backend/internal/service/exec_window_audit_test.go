package service

import (
	"testing"

	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/internal/testsupport"
)

// 窗口放行的命令在审计里靠 Operator 认领:windowOperator 把窗口写成
// `执行窗口 · <名字> (#<id>)`,而 Operator 从 payload v3 起就在链哈希里,所以这个
// 归属事后改不了。
//
// 这一条钉的是**构造与查询必须用同一套格式**。两边各写一遍字符串,是这个功能最可能
// 坏掉的方式:改了 windowOperator 的措辞,查询会安静地返回空列表 —— 不报错,只是
// 那扇门看起来从没放行过任何东西。
func TestWindowOperator_MatchesItsOwnQueryPattern(t *testing.T) {
	w := &model.ExecWindow{ID: 42, Name: "周四凌晨维护窗"}
	op := windowOperator(w)

	if !operatorBelongsToWindow(op, w.ID) {
		t.Errorf("windowOperator 造出来的 %q 匹配不上它自己的查询模式 %q", op, windowOperatorLike(w.ID))
	}
	// 别的窗口不该认领它。
	if operatorBelongsToWindow(op, 43) {
		t.Errorf("%q 被 #43 的模式认领了", op)
	}
	// 名字里带 % 或 _ 这些 LIKE 通配符时也不能串。
	tricky := &model.ExecWindow{ID: 7, Name: "100%_紧急"}
	if !operatorBelongsToWindow(windowOperator(tricky), 7) {
		t.Errorf("窗口名里带 LIKE 通配符时认领失败:%q", windowOperator(tricky))
	}
}

// 按窗口取审计:只拿这扇门放行的,不拿走审批的、也不拿别的门的。
func TestAuditForWindow_ReturnsOnlyWhatThatWindowLetThrough(t *testing.T) {
	db := testsupport.NewDB(t)
	s := &Services{Repo: repository.New(db)}
	viewer := auditActorUser(t, s) // 有审计权限;权限本身由 bootstrap 那条 API 用例守
	actor := &model.User{ID: 1, Name: "林薇"}

	w1 := &model.ExecWindow{ID: 11, Name: "周二凌晨批量窗"}
	w2 := &model.ExecWindow{ID: 22, Name: "周五发版窗"}

	s.appendAudit(actor, nil, "DELETE FROM orders", "high", "executed", "", windowOperator(w1))
	s.appendAudit(actor, nil, "UPDATE users SET x=1", "high", "executed", "", windowOperator(w1))
	s.appendAudit(actor, nil, "DROP TABLE t", "high", "executed", "", windowOperator(w2))
	s.appendAudit(actor, nil, "SELECT 1", "low", "executed", "", "")          // 普通执行
	s.appendAudit(actor, nil, "TRUNCATE t", "high", "executed", "AP-1", "张伟") // 走审批的

	rows, err := s.AuditForWindow(viewer, w1.ID)
	if err != nil {
		t.Fatalf("AuditForWindow: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("窗口 #11 应当放行了 2 条,实际取回 %d 条", len(rows))
	}
	for _, a := range rows {
		if !operatorBelongsToWindow(a.Operator, w1.ID) {
			t.Errorf("取回了不属于窗口 #11 的行:operator=%q command=%q", a.Operator, a.Command)
		}
	}
}

// 窗口改名之后,它此前放行过的记录仍然归它。
//
// 这是"认 id 不认名"的实际后果,也是这个设计的一半理由:审计行写下时是什么样就是
// 什么样(Operator 在链哈希里,本来也改不了),而窗口的名字是可以改的。按名字查的话,
// 一次改名就会让此前所有记录从这扇门上脱钩 —— 而且是安静地脱钩。
func TestAuditForWindow_SurvivesARename(t *testing.T) {
	db := testsupport.NewDB(t)
	s := &Services{Repo: repository.New(db)}
	viewer := auditActorUser(t, s)
	actor := &model.User{ID: 1, Name: "林薇"}

	// 当时叫这个名字,记录就是按这个名字写下的。
	before := &model.ExecWindow{ID: 77, Name: "旧名字"}
	s.appendAudit(actor, nil, "DELETE FROM a", "high", "executed", "", windowOperator(before))

	// 后来改了名。新记录带新名字,但 id 没变。
	after := &model.ExecWindow{ID: 77, Name: "改过的名字"}
	s.appendAudit(actor, nil, "DELETE FROM b", "high", "executed", "", windowOperator(after))

	rows, err := s.AuditForWindow(viewer, 77)
	if err != nil {
		t.Fatalf("AuditForWindow: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("改名前后各一条都该归这扇门,实际取回 %d 条 —— 查询大概在认名字而不是 id", len(rows))
	}
}
