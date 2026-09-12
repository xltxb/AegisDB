package gateway

import "testing"

// PostgreSQL 的 `DO $$ … $$` 块把它真正做的事藏在了动词后面。
//
// 首词是 DO,而 DO 不在能力映射表里 —— 落进默认的 write。于是:
//
//	DO $$ BEGIN DROP TABLE t_orders; END $$;
//
// 一个只有 write 能力的角色就能做 DDL,而 `DROP TABLE t_orders` 裸着写要 ddl。
// 无 WHERE 拦截同样看不见:verb 是 do,不是 delete/update,整层直接跳过。
//
// 这和 WITH 是同一件事(ER9:`WITH d AS (DELETE …) SELECT …` 真的会删),解法也一样 ——
// 让它按**它实际做的那件事**判。块体里有好几条时取最危险的那条:一个 DO 块是一次
// 提交,里面最狠的那条决定了它的后果。
func TestParseVerb_LooksInsideDoBlocks(t *testing.T) {
	for _, c := range []struct {
		sql, want string
	}{
		{`DO $$ BEGIN DROP TABLE t_orders; END $$;`, "DROP"},
		{`DO $$ BEGIN DELETE FROM t_orders; END $$;`, "DELETE"},
		{`DO $body$ BEGIN TRUNCATE TABLE t_orders; END $body$;`, "TRUNCATE"},
		// 好几条时取最危险的:UPDATE 是 write,DROP 是 ddl —— 按 DROP 判。
		{`DO $$ BEGIN UPDATE t SET a=1 WHERE id=1; DROP TABLE t_orders; END $$;`, "DROP"},
		{`DO $$ BEGIN GRANT ALL ON t TO u; END $$;`, "GRANT"},
	} {
		if got := ParseVerb(c.sql); got != c.want {
			t.Errorf("ParseVerb(%q) = %q,期望 %q", c.sql, got, c.want)
		}
	}
}

func TestMapVerb_DoBlockGetsTheCapabilityOfWhatItDoes(t *testing.T) {
	if got := MapVerbToCapability(ParseVerb(`DO $$ BEGIN DROP TABLE t; END $$;`)); got != "ddl" {
		t.Errorf("DO 块里的 DROP 应当要 ddl 能力,实际 %q —— 只有 write 的角色能靠它做 DDL", got)
	}
	if got := MapVerbToCapability(ParseVerb(`DO $$ BEGIN GRANT ALL ON t TO u; END $$;`)); got != "grant" {
		t.Errorf("DO 块里的 GRANT 应当要 grant 能力,实际 %q", got)
	}
}

func TestNoWhere_LooksInsideDoBlocks(t *testing.T) {
	if !NoWhere(`DO $$ BEGIN DELETE FROM t_orders; END $$;`) {
		t.Error("DO 块里的整表删除逃过了无 WHERE 拦截")
	}
	if NoWhere(`DO $$ BEGIN DELETE FROM t_orders WHERE id = 1; END $$;`) {
		t.Error("DO 块里带 WHERE 的删除被误判成无 WHERE")
	}
}

// 不含改动的 DO 块保持原样 —— 没有东西可藏时不必替它改名。
func TestParseVerb_HarmlessDoBlockStaysDo(t *testing.T) {
	if got := ParseVerb(`DO $$ BEGIN RAISE NOTICE 'hello'; END $$;`); got != "DO" {
		t.Errorf("不含改动的 DO 块不必改判,实际 %q", got)
	}
}
