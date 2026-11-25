package osc

import (
	"reflect"
	"testing"
)

const (
	native = "原生"
	online = "在线变更"
	unsup  = "不支持"
)

func route(t *testing.T, sql string, v [3]int) (Parsed, string) {
	t.Helper()
	p, ok := ParseDDL(sql)
	if !ok {
		t.Fatalf("%q 应识别为表 DDL", sql)
	}
	p.Resolve(v[0], v[1], v[2])
	switch {
	case p.Supported:
		return p, online
	case p.Native:
		return p, native
	case p.Unsupported != "":
		return p, unsup
	}
	t.Fatalf("%q: 没有结论 %+v", sql, p)
	return p, ""
}

// 工单 08 的分类表:每一格至少一条。
func TestParseDDL_ClassifiesEveryKindOfClause(t *testing.T) {
	v8036 := [3]int{8, 0, 36}
	cases := []struct{ sql, want string }{
		// 原生秒完成:INSTANT / 只改元数据
		{"ALTER TABLE orders DROP INDEX idx_c", native},
		{"ALTER TABLE orders DROP KEY `idx_c`", native},
		{"DROP INDEX idx_c ON `app`.orders", native},
		{"ALTER TABLE orders RENAME INDEX a TO b", native},
		{"ALTER TABLE orders ALTER INDEX a INVISIBLE", native},
		{"ALTER TABLE orders ALTER COLUMN st SET DEFAULT 'new'", native},
		{"ALTER TABLE orders ALTER st DROP DEFAULT", native},
		{"ALTER TABLE orders RENAME COLUMN a TO b", native},
		{"ALTER TABLE orders ADD COLUMN c INT NOT NULL DEFAULT 0", native},
		{"ALTER TABLE orders ADD c INT AFTER id", native},
		{"ALTER TABLE orders ADD COLUMN v INT AS (a + 1) VIRTUAL", native},
		{"ALTER TABLE orders DROP COLUMN c", native},
		{"ALTER TABLE orders DROP c", native},
		{"ALTER TABLE orders RENAME TO orders_old", native},
		{"ALTER TABLE orders AUTO_INCREMENT = 1000", native},
		{"ALTER TABLE orders COMMENT = '订单'", native},
		{"ALTER TABLE orders DEFAULT CHARSET = utf8mb4", native},
		{"ALTER TABLE orders DROP FOREIGN KEY fk_a", native},
		{"ALTER TABLE orders ADD COLUMN c INT, ALGORITHM=INSTANT", native},
		// 在线变更:INPLACE / COPY
		{"ALTER TABLE orders ADD INDEX idx_c (created_at);", online},
		{"alter table `app`.`orders` add key `idx_x` (`a`, b(20))", online},
		{"CREATE INDEX idx_c ON orders (created_at ASC)", online},
		{"ALTER TABLE orders ADD UNIQUE INDEX u (a)", online},
		{"ALTER TABLE orders ADD UNIQUE KEY u (a)", online},
		{"CREATE UNIQUE INDEX u ON orders (a)", online},
		{"ALTER TABLE orders ADD CONSTRAINT uk UNIQUE (a)", online},
		{"CREATE FULLTEXT INDEX f ON orders (note)", online},
		{"ALTER TABLE orders ADD SPATIAL INDEX g (pt)", online},
		{"ALTER TABLE orders ADD INDEX f ((lower(name)))", online},
		{"ALTER TABLE orders ADD INDEX d (a DESC)", online},
		{"ALTER TABLE orders ADD PRIMARY KEY (id)", online},
		{"ALTER TABLE orders DROP PRIMARY KEY, ADD PRIMARY KEY (id, a)", online},
		{"ALTER TABLE orders DROP PRIMARY KEY", online},
		{"ALTER TABLE orders DROP INDEX `PRIMARY`", online},
		{"ALTER TABLE orders MODIFY name VARCHAR(200)", online},
		{"ALTER TABLE orders MODIFY COLUMN amount BIGINT NOT NULL", online},
		{"ALTER TABLE orders CHANGE note remark VARCHAR(64)", online},
		{"ALTER TABLE orders ADD COLUMN seq BIGINT NOT NULL AUTO_INCREMENT, ADD UNIQUE KEY (seq)", online},
		{"ALTER TABLE orders ADD COLUMN s INT AS (a * 2) STORED", online},
		{"ALTER TABLE orders CONVERT TO CHARACTER SET utf8mb4", online},
		{"ALTER TABLE orders ENGINE=InnoDB", online},
		{"ALTER TABLE orders FORCE", online},
		{"ALTER TABLE orders ROW_FORMAT=DYNAMIC", online},
		{"ALTER TABLE orders ADD CONSTRAINT ck CHECK (amount >= 0)", online},
		{"ALTER TABLE orders ADD INDEX a (x), ADD INDEX b (y)", online},
		{"ALTER TABLE orders ADD INDEX idx_c USING BTREE (created_at) COMMENT 'x', ALGORITHM=INPLACE", online},
		{"ALTER TABLE orders ADD COLUMN c INT, ADD INDEX ic (c)", online}, // 有一条要重建,整条走在线变更
		// 不支持
		{"ALTER TABLE orders ADD CONSTRAINT fk FOREIGN KEY (uid) REFERENCES users (id)", unsup},
		{"ALTER TABLE orders PARTITION BY HASH(id) PARTITIONS 4", unsup},
		{"ALTER TABLE orders DROP PARTITION p0", unsup},
		{"ALTER TABLE orders REMOVE PARTITIONING", unsup},
		{"ALTER TABLE orders ENGINE=MyISAM", unsup},
		{"ALTER TABLE orders ORDER BY created_at", unsup},
		{"ALTER TABLE orders DISCARD TABLESPACE", unsup},
		{"ALTER TABLE orders FROBNICATE", unsup},
		{"ALTER TABLE orders ADD INDEX i (a), RENAME TO o2", unsup},
		{"ALTER TABLE orders ADD INDEX i (a), ADD CONSTRAINT fk FOREIGN KEY (u) REFERENCES u (id)", unsup},
	}
	for _, c := range cases {
		p, got := route(t, c.sql, v8036)
		if got != c.want {
			t.Errorf("%q: 应为 %s,实际 %s (%+v)", c.sql, c.want, got, p)
		}
		if got == online && p.Why == "" {
			t.Errorf("%q: 走在线变更要说清为什么", c.sql)
		}
		if got == unsup && p.Unsupported == "" {
			t.Errorf("%q: 不支持要说清为什么", c.sql)
		}
	}
}

// 加 / 删列是不是 INSTANT 看版本:8.0.12 起末尾加列,8.0.29 起任意位置加列与删列。
func TestParseDDL_InstantDependsOnVersion(t *testing.T) {
	cases := []struct {
		sql  string
		v    [3]int
		want string
	}{
		{"ALTER TABLE t ADD COLUMN c INT", [3]int{8, 0, 11}, online},
		{"ALTER TABLE t ADD COLUMN c INT", [3]int{8, 0, 12}, native},
		{"ALTER TABLE t ADD COLUMN c INT FIRST", [3]int{8, 0, 28}, online},
		{"ALTER TABLE t ADD COLUMN c INT AFTER id", [3]int{8, 0, 29}, native},
		{"ALTER TABLE t DROP COLUMN c", [3]int{8, 0, 28}, online},
		{"ALTER TABLE t DROP COLUMN c", [3]int{8, 0, 29}, native},
		{"ALTER TABLE t DROP COLUMN c", [3]int{8, 4, 0}, native},
	}
	for _, c := range cases {
		if _, got := route(t, c.sql, c.v); got != c.want {
			t.Errorf("%q @%v: 应为 %s,实际 %s", c.sql, c.v, c.want, got)
		}
	}
}

// 走在线变更时:影子表上执行的子句原文(去掉 ALGORITHM / LOCK)、列改名映射。
func TestParseDDL_ChangeCarriesSpecAndRenames(t *testing.T) {
	p, _ := route(t, "ALTER TABLE `app`.orders CHANGE note remark VARCHAR(64), RENAME COLUMN a TO b, MODIFY c BIGINT, ALGORITHM=INPLACE, LOCK=NONE", [3]int{8, 0, 36})
	want := Change{Action: ActionAlter, Spec: "CHANGE note remark VARCHAR(64), RENAME COLUMN a TO b, MODIFY c BIGINT",
		Renames: map[string]string{"note": "remark", "a": "b"}}
	if p.Schema != "app" || p.Table != "orders" || !reflect.DeepEqual(p.Change, want) {
		t.Errorf("应为 app.orders %+v,实际 %s.%s %+v", want, p.Schema, p.Table, p.Change)
	}
	p, _ = route(t, "CREATE UNIQUE INDEX u USING BTREE ON orders (a, b) ALGORITHM=INPLACE LOCK=NONE", [3]int{8, 0, 36})
	if p.Change.Spec != "ADD UNIQUE INDEX u USING BTREE (a, b)" {
		t.Errorf("CREATE INDEX 应改写成等价的 ADD 子句,实际 %q", p.Change.Spec)
	}
}

func TestParseDDL_NotATableDDL(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM orders",
		"UPDATE orders SET a = 1",
		"DROP TABLE orders",
		"CREATE TABLE t (a INT)",
		"TRUNCATE orders",
		"",
	} {
		if _, ok := ParseDDL(sql); ok {
			t.Errorf("%q 不是 ALTER / CREATE INDEX / DROP INDEX,不该识别", sql)
		}
	}
}

func TestParseDDL_QuotedCommaIsNotAClauseSeparator(t *testing.T) {
	p, got := route(t, "ALTER TABLE orders ADD INDEX idx_c (created_at) COMMENT 'a,b', ADD COLUMN x VARCHAR(8) DEFAULT 'p,q'", [3]int{8, 0, 36})
	if got != online || len(p.clauses) != 2 {
		t.Fatalf("引号里的逗号不是子句分隔: %s %+v", got, p.clauses)
	}
}
