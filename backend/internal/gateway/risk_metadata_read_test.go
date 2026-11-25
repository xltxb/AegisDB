package gateway

import (
	"testing"

	"velagateway/internal/model"
)

// 2026-10-27 生产:终端里 `SHOW CREATE TABLE platform_event` 命中「高危命令字典 · 需审批」。
// 字典按文本里的单词匹配,运维为拦建表在字典里加了 CREATE,SHOW CREATE TABLE 里也有这个词
// (SHOW CREATE VIEW / PROCEDURE 同理)。SHOW / DESC 只看元数据,按 select 判,不过字典。
func metaReadEngine(caps map[string]string) *RiskEngine {
	return NewRiskEngine(&fakeStore{caps: caps, strict: true, cmds: []model.RiskCommand{
		{Command: "CREATE", TierCode: "prod", Level: model.RiskMid},
		{Command: "GRANT", TierCode: "prod", Level: model.RiskMid},
		{Command: "DROP", TierCode: "prod", Level: model.RiskHigh},
		{Command: "ALTER", TierCode: "prod", Level: model.RiskHigh},
	}})
}

func TestEvaluate_ShowAndDescSkipDictionary(t *testing.T) {
	e := metaReadEngine(nil)
	for _, sql := range []string{
		"SHOW CREATE TABLE platform_event;",
		"show create table `event_platform`.`platform_event`",
		"SHOW CREATE VIEW v_orders",
		"SHOW CREATE PROCEDURE p_drop_old",
		"SHOW GRANTS FOR CURRENT_USER",
		"SHOW TABLES LIKE '%drop%'",
		"DESC alter_log",
		"DESCRIBE create_task",
		"-- 看看建表语句\nSHOW CREATE TABLE t",
	} {
		v := e.EvaluateFor([]int64{1}, "MySQL 8.0", "prod", sql)
		if v.Action != ActionAllow {
			t.Errorf("%q 应放行,实际 %+v", sql, v)
		}
		if _, risk, _ := e.ScanStatement("prod", sql); risk != "safe" {
			t.Errorf("脚本扫描 %q 应为 safe,实际 %s", sql, risk)
		}
	}
}

// 读的那道闸照旧:select 被拒 / 需审批的角色,SHOW 也一样。
func TestEvaluate_ShowStillGatedBySelect(t *testing.T) {
	if v := metaReadEngine(map[string]string{"select|prod": model.LevelDeny}).EvaluateFor([]int64{1}, "MySQL 8.0", "prod", "SHOW CREATE TABLE t"); v.Action != ActionDeny {
		t.Errorf("select 被拒时 SHOW 应拒绝,实际 %+v", v)
	}
	if v := metaReadEngine(map[string]string{"select|prod": model.LevelApprove}).EvaluateFor([]int64{1}, "MySQL 8.0", "prod", "SHOW CREATE TABLE t"); v.Action != ActionApprove {
		t.Errorf("select 需审批时 SHOW 应需审批,实际 %+v", v)
	}
}

// 能借 SHOW 开头藏执行命令的写法不走捷径,照旧过字典:多条语句、可执行注释、\' 歧义。
func TestEvaluate_ShowShortcutNotABypass(t *testing.T) {
	e := metaReadEngine(nil)
	for _, sql := range []string{
		"SHOW TABLES; DROP TABLE t",
		"SHOW TABLES /*!40000 ; DROP TABLE t */",
		"SHOW TABLES /*T! ; DROP TABLE t */",
		`SHOW TABLES LIKE 'a\'; DROP TABLE t; -- '`,
	} {
		if v := e.EvaluateFor([]int64{1}, "MySQL 8.0", "prod", sql); v.Action == ActionAllow {
			t.Errorf("%q 不应放行,实际 %+v", sql, v)
		}
		if _, risk, _ := e.ScanStatement("prod", sql); risk == "safe" {
			t.Errorf("脚本扫描 %q 不应为 safe", sql)
		}
	}
	// 回归:真正的建表 / 改表照旧命中字典
	for sql, want := range map[string]string{"CREATE TABLE t (id int)": model.RiskMid, "ALTER TABLE t ADD c int": model.RiskHigh} {
		if v := e.EvaluateFor([]int64{1}, "MySQL 8.0", "prod", sql); v.Action != ActionApprove || v.Risk != want {
			t.Errorf("%q 应命中字典(%s),实际 %+v", sql, want, v)
		}
	}
}
