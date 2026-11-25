package bootstrap

// 执行变更阶段的日志要说清跑了什么:每条语句、影响行数、耗时、成功或失败的原文。
//
// 用户实测 REL-1(一条 UPDATE)两个阶段都「已完成」,执行变更卡片却显示"该阶段暂无日志"。
// 后端其实写了日志,是页面拿列表接口(故意不带日志)的数据盖掉了详情 —— 那一半修在前端
// (e2e/release-refresh.spec.ts)。这里钉住后端这一半:详情接口带日志、列表不带,且日志里
// 有语句本身,而不只是"[1/1] 1 行受影响"。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestExecuteStageLogNamesStatementRowsAndTiming(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := realConnForRelease(t, app, admin, func(db *sql.DB) {
		_, _ = db.Exec(`CREATE TABLE t_bond (bondseq_id INTEGER, bond_type INTEGER)`)
		_, _ = db.Exec(`INSERT INTO t_bond VALUES (855048, 0), (855049, 0)`)
	})

	log := runExecOnlyRelease(t, app, admin, conn, "执行日志", "update t_bond set bond_type=1 where bondseq_id=855048;")

	if strings.TrimSpace(log) == "" {
		t.Fatal("execute stage finished but its log is empty")
	}
	for _, want := range []string{
		"update t_bond set bond_type=1 where bondseq_id=855048", // 语句本身
		"1 行受影响",  // 影响行数
		"影响 1 行",   // 汇总
		"ms)",      // 耗时
		"[1/1]",    // 第几条
	} {
		if !strings.Contains(log, want) {
			t.Errorf("execute log missing %q, got:\n%s", want, log)
		}
	}
}

// 失败时日志要有报错原文和出错的那条语句,并且停在那一条。
func TestExecuteStageLogCarriesFailureText(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := realConnForRelease(t, app, admin, func(db *sql.DB) {
		_, _ = db.Exec(`CREATE TABLE t_ok (n INTEGER)`)
	})
	pid := app.createPipeline(admin, "执行失败日志", "", []map[string]any{{"name": "执行变更", "type": "execute"}})
	r := app.submitRelease(admin, map[string]any{
		"title": "执行失败日志", "pipelineId": pid, "connectionId": conn, "reason": "失败日志回归",
		"sql": "insert into t_ok values (1);\nupdate t_missing set n=2 where n=1;",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	done := app.confirmExecutionAndWait(admin, rel.ID)
	eq(t, done.Status, "failed", "second statement fails")

	log := stageByType(done, "execute").Log
	for _, want := range []string{
		"insert into t_ok values (1)",          // 成功的那条也在
		"第 2/2 条失败",                          // 停在哪
		"update t_missing set n=2 where n=1",   // 出错的语句
		"t_missing",                            // 驱动报错原文(no such table: t_missing)
	} {
		if !strings.Contains(log, want) {
			t.Errorf("failed execute log missing %q, got:\n%s", want, log)
		}
	}
	// 发布级 error 截日志开头:报错原文必须在开头,不能被前面语句的回显挤出 300 字
	if !strings.Contains(done.Error, "no such table") {
		t.Errorf("release error should carry the driver error, got %q", done.Error)
	}
}

// 列表不带阶段日志(体积),详情带 —— 页面必须从详情取日志。这条契约是"暂无日志"的另一半。
func TestReleaseListOmitsStageLogButDetailCarriesIt(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	conn := realConnForRelease(t, app, admin, func(db *sql.DB) {
		_, _ = db.Exec(`CREATE TABLE t_bond (bondseq_id INTEGER, bond_type INTEGER)`)
		_, _ = db.Exec(`INSERT INTO t_bond VALUES (1, 0)`)
	})
	pid := app.createPipeline(admin, "列表与详情", "", []map[string]any{
		{"name": "规范审查", "type": "review"},
		{"name": "执行变更", "type": "execute"},
	})
	r := app.submitRelease(admin, map[string]any{
		"title": "列表与详情", "pipelineId": pid, "connectionId": conn, "reason": "契约",
		"sql": "update t_bond set bond_type=1 where bondseq_id=1;",
	})
	eq(t, r.Code, 0, "submit release")
	var rel releaseView
	_ = json.Unmarshal(r.Data, &rel)
	done := app.confirmExecutionAndWait(admin, rel.ID)
	eq(t, done.Status, "success", "release runs ("+done.Error+")")
	for _, st := range done.Stages {
		if strings.TrimSpace(st.Log) == "" {
			t.Errorf("detail: stage %s (%s) finished with an empty log", st.Name, st.Status)
		}
	}

	lr := app.do(http.MethodGet, "/api/v1/releases?scope=all&pageSize=100", admin, nil)
	var page struct {
		Items []releaseView `json:"items"`
	}
	_ = json.Unmarshal(lr.Data, &page)
	for _, it := range page.Items {
		if it.ID != rel.ID {
			continue
		}
		for _, st := range it.Stages {
			if st.Log != "" {
				t.Errorf("list should not carry stage logs (stage %s)", st.Name)
			}
		}
		return
	}
	t.Fatal("release not in list")
}
