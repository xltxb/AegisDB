package review

// PostgreSQL 的块体也要看得进去。
//
// 这是 ADR 0008「包一层块就从拦截变通过」的 PG 版本。Oracle 那一半早就堵上了
// (plsql_body.go),而 PG 用的是**美元引用**,块头也不一样:
//
//	DO $$ BEGIN DELETE FROM t_orders; END $$;
//	CREATE FUNCTION f() RETURNS void AS $$ BEGIN DROP TABLE t_orders; END $$ LANGUAGE plpgsql;
//
// 两者的首词是 DO 和 CREATE,规则锚在语句开头,于是一条都不触发 —— 同一条 DELETE,
// 裸着写被拦,包进 `$$ … $$` 就通过了。
//
// `$tag$ … $tag$` 这种带标签的形式同样要认:它正是为了"体里也有 $$"而存在的,
// 只认光秃秃的 `$$` 等于留着一个换个写法就绕开的口子。

import "testing"

func TestReview_LooksInsidePostgresDollarQuotedBlocks(t *testing.T) {
	rules := allRules()
	for _, c := range []struct {
		name, sql, wantCode string
	}{
		{"DO 块里的无 WHERE DELETE",
			"DO $$ BEGIN DELETE FROM t_orders; END $$;", "dml.require.where"},
		{"DO 块里的 DROP",
			"DO $$ BEGIN DROP TABLE t_orders; END $$;", "ddl.forbid.drop"},
		{"CREATE FUNCTION 体里的 DROP",
			"CREATE FUNCTION f() RETURNS void AS $$ BEGIN DROP TABLE t_orders; END $$ LANGUAGE plpgsql;",
			"ddl.forbid.drop"},
		{"带标签的美元引用",
			"DO $body$ BEGIN DELETE FROM t_orders; END $body$;", "dml.require.where"},
		{"CREATE OR REPLACE FUNCTION 体里的 TRUNCATE",
			"CREATE OR REPLACE FUNCTION f() RETURNS void AS $fn$ BEGIN TRUNCATE TABLE t_orders; END $fn$ LANGUAGE plpgsql;",
			"ddl.forbid.truncate"},
	} {
		r := Check(DialectGeneric, c.sql, rules)
		if !hasCode(r, c.wantCode) {
			t.Errorf("[%s] 没有命中 %s —— 包一层 $$ 就漏掉,等于给规则开了个后门。实际: %v",
				c.name, c.wantCode, codesOf(r))
		}
		if r.Passed {
			t.Errorf("[%s] 审查判为通过 —— 裸着写是拦的,包进块里不该放行", c.name)
		}
	}
}

// 合规的 PG 块不该变成噪音 —— 一个老是误报的审查，换来的是所有人学会无视它。
func TestReview_CleanPostgresBlockStaysQuiet(t *testing.T) {
	rules := allRules()
	clean := "DO $$ BEGIN UPDATE t_orders SET flag = 1 WHERE id = 1; END $$;"
	if r := Check(DialectGeneric, clean, rules); !r.Passed {
		t.Errorf("合规的 PG 块不该被拦,实际命中: %v", codesOf(r))
	}
}
