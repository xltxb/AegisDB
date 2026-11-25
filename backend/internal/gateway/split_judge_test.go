package gateway

import (
	"reflect"
	"testing"
)

// MySQL 的 `--` 后面必须跟空白才是注释:`SELECT 1--1` 是"1 减负 1"。通用拆分器按标准
// SQL 把 `--1 …` 到行尾都当注释剥掉,于是判定看到的是 `SELECT 1`,而单条语句下发的是
// 原文 —— `INTO OUTFILE`、`load_file(…)` 就这样藏在判定之外,服务器照跑。
func TestSplitForJudge_MySQLDashDashIsNotAComment(t *testing.T) {
	got := SplitForJudge("MySQL 8.0", "SELECT 1--1 INTO OUTFILE '/tmp/p'")
	if len(got) != 1 || !WritesServerFile(got[0]) {
		t.Fatalf("MySQL 上判定应看到 INTO OUTFILE,实际 %#v", got)
	}
	if fn := EffectfulCall(SplitForJudge("MariaDB 10.6", "SELECT 1--1, load_file('/etc/passwd')")[0]); fn != "LOAD_FILE" {
		t.Errorf("MySQL 上判定应看到 load_file,实际 %q", fn)
	}
	// `-- ` 后面有空白:真注释,照常剥掉。
	if got := SplitForJudge("MySQL 8.0", "SELECT 1 -- INTO OUTFILE '/tmp/p'"); !reflect.DeepEqual(got, []string{"SELECT 1"}) {
		t.Errorf("真注释应被剥掉,实际 %#v", got)
	}
	// PG / SQLite 上 `--` 永远是注释 —— 不能照 MySQL 改写:`--'` 若不算注释,那个引号会
	// 吞掉后面的分隔符,把下一条语句并进来。
	if got := SplitForJudge("PostgreSQL 15", "SELECT 1 --'\n; DROP TABLE t"); !reflect.DeepEqual(got, []string{"SELECT 1", "DROP TABLE t"}) {
		t.Errorf("PG 上 --' 是注释,实际 %#v", got)
	}
}

// 2026-10 安全审计 C1:MySQL 把 `#` 到行尾当注释,通用拆分器不认(PG 里 # 是运算符)。
// 以 `#` 注释开头的语句于是取不到动词,空动词按读级放行,服务器却跳过注释照跑后面的写。
// MySQL 系判定前去掉语句开头的 `#` 注释。
func TestSplitForJudge_MySQLLeadingHashComment(t *testing.T) {
	for _, sql := range []string{
		"# note\nDELETE FROM t",
		"  #a\n#b\r\n  DELETE FROM t",
		"/* x */ # note\nDELETE FROM t",
		"SELECT 1;\n# note\nDELETE FROM t",
	} {
		got := SplitForJudge("MySQL 8.0", sql)
		last := got[len(got)-1]
		if v := ParseVerb(last); v != "DELETE" {
			t.Errorf("%q:最后一条应判为 DELETE,实际动词 %q(%#v)", sql, v, got)
		}
	}
	// 只有注释:仍是一条空语句 / 没有语句,不凭空造出动词
	for _, got := range SplitForJudge("TiDB", "# only a comment") {
		if v := ParseVerb(got); v != "" {
			t.Errorf("纯注释不应有动词,实际 %q", v)
		}
	}
	// 行中的 # 不动(拆分器对它过度拆分是安全方向);PG 上 # 是运算符,不剥
	if got := SplitForJudge("PostgreSQL 15", "SELECT 1 # 2"); !reflect.DeepEqual(got, []string{"SELECT 1 # 2"}) {
		t.Errorf("PG 上 # 不是注释,实际 %#v", got)
	}
}

// 业务查询 / 导出 / 脚本 / 发布单有不少路径直接用通用拆分器,不经 SplitForJudge;它们都靠
// ParseVerb 取动词,所以跳过开头 `#` 注释放在 ParseVerb 里,不分引擎。
func TestParseVerb_SkipsLeadingHashComment(t *testing.T) {
	cases := map[string]string{
		"# note\nDELETE FROM t":                "DELETE",
		"# it's\nUPDATE t SET a = 1":           "UPDATE", // 注释里的引号不能让后面变成"字面量"
		"# a\n/* b */ -- c\n# d\nDROP TABLE t": "DROP",
		"#only":                                "",
		"SELECT 1 # 2":                         "SELECT",
	}
	for sql, want := range cases {
		if got := ParseVerb(sql); got != want {
			t.Errorf("ParseVerb(%q) = %q, want %q", sql, got, want)
		}
	}
	if MapVerbToCapability(ParseVerb("# x\nDELETE FROM t")) != "write" {
		t.Error("# 注释开头的 DELETE 应按写级判")
	}
}
