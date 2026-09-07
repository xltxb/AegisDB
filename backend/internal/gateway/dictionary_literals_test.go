package gateway

import (
	"testing"

	"velagateway/internal/model"
)

func dictEngine() *RiskEngine {
	return NewRiskEngine(&fakeStore{cmds: []model.RiskCommand{
		{Command: "DROP", TierCode: "prod", Level: model.RiskHigh},
		{Command: "DELETE", TierCode: "prod", Level: model.RiskHigh},
		{Command: "TRUNCATE", TierCode: "prod", Level: model.RiskHigh},
	}})
}

// 字典扫的是抹掉字面量之后的结构。数据里的词不是语法。
//
// 真实故障:一份 105 条语句的菜单初始化脚本(全是 INSERT INTO sys_menu),因为权限串
// 写作 'system:menu:delete',被判成高危 DELETE 并整脚本送审。它一条都没删。
func TestDictionary_IgnoresKeywordsInsideLiterals(t *testing.T) {
	e := dictEngine()
	cases := []string{
		`INSERT INTO sys_menu (menu_name, perms) VALUES ('删除菜单', 'system:menu:delete')`,
		`INSERT INTO t (a) VALUES ('drop this row')`,
		`SELECT 'truncate' AS word FROM dual`,
		`UPDATE t SET note = 'delete later' WHERE id = 1`,
		"SELECT `delete` FROM t",
	}
	for _, sql := range cases {
		cmd, lvl, err := e.matchCommand(sql, "prod")
		if err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
		if cmd != "" {
			t.Errorf("字面量里的关键词不该命中字典: %q → cmd=%q level=%q", sql, cmd, lvl)
		}
	}
}

// 对照面:真正的语句照样命中,这次改动不能把字典变松。
func TestDictionary_StillMatchesRealStatements(t *testing.T) {
	e := dictEngine()
	for sql, want := range map[string]string{
		`DROP TABLE orders`:                    "DROP",
		`delete from orders where id = 1`:      "DELETE",
		`TRUNCATE TABLE audit_tmp`:             "TRUNCATE",
		`WITH d AS (DELETE FROM t RETURNING 1) SELECT * FROM d`: "DELETE",
	} {
		cmd, _, err := e.matchCommand(sql, "prod")
		if err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
		if cmd != want {
			t.Errorf("%q 应命中 %s,实际 %q", sql, want, cmd)
		}
	}
}

// 动态 SQL 的载荷是真的会执行的,所以字面量抹除对它必须开一个口子。
//
// 从前这类语句是碰巧被覆盖的(字典扫原文,顺带扫进了引号);现在是明确规则:结构里
// 出现动态执行的构造,就把字面量内容也扫一遍。
func TestDictionary_LooksInsideDynamicSQL(t *testing.T) {
	e := dictEngine()
	for _, sql := range []string{
		`EXECUTE IMMEDIATE 'DROP TABLE t'`,
		`BEGIN EXECUTE IMMEDIATE 'DROP TABLE t'; END;`,
		`EXEC sp_executesql N'TRUNCATE TABLE t'`,
		`PREPARE s FROM 'DELETE FROM orders'`,
		`EXEC('DROP TABLE t')`,
	} {
		cmd, _, err := e.matchCommand(sql, "prod")
		if err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
		if cmd == "" {
			t.Errorf("动态 SQL 的载荷必须照样命中字典: %q", sql)
		}
	}
}

// 但"动态执行"这个判断本身也只看结构 —— 数据里出现 execute 这个词,不能把整行变成
// 动态 SQL,否则误报会从另一个门绕回来。
func TestDictionary_DataSayingExecuteIsNotDynamicSQL(t *testing.T) {
	e := dictEngine()
	sql := `INSERT INTO audit_log (action, detail) VALUES ('execute', 'user asked to drop the old report')`
	cmd, _, err := e.matchCommand(sql, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "" {
		t.Errorf("字段值里的 execute 不该开启载荷扫描,实际命中 %q", cmd)
	}
}

// 没有任何角色 = 拒绝,不是放行。
//
// 从前返回 allow,理由是"这个状态到不了" —— 用户总有一个主角色,接口也拒绝保存空的
// 角色集。停用账户会收回角色之后,这个状态就到得了了,而 allow 意味着一个被剥光角色
// 的账户,一旦哪一处状态检查漏掉,就手握全部能力。权限是**从角色来的**,没有角色就
// 没有来源。
func TestCapability_NoRolesMeansDeny(t *testing.T) {
	e := NewRiskEngine(&fakeStore{})
	for _, cap := range []string{"select", "write", "ddl", "grant"} {
		lvl, err := e.capabilityLevelUnion(nil, cap, "prod")
		if err != nil {
			t.Fatal(err)
		}
		if lvl != model.LevelDeny {
			t.Errorf("空角色集在 %s 上应当拒绝,实际 %q", cap, lvl)
		}
	}
	// 端到端:一个没有角色的用户,连只读查询都不该放行。
	v := e.EvaluateFor(nil, "", "prod", "SELECT 1")
	if v.Action != ActionDeny {
		t.Errorf("没有角色的用户执行 SELECT 应被拒绝,实际 %s/%s", v.Action, v.Rule)
	}
}
