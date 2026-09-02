package gateway

// SHOW USER 在 sqlplus 里跑得好好的,在平台上报 ORA-00900。两边都没错 —— SHOW 不是
// SQL,它是 SQL*Plus 自己的命令,由客户端解释掉,从来不会发到服务端。
//
// 网关承担客户端那一半职责。但翻译**必须在判定之后**:字典和审查匹配的是文本里的
// 那个词,先翻译再判定就是一个绕过。下面最后两条钉的就是这一点。

import (
	"strings"
	"testing"
)

func TestOracleSQLPlus_ShowUserBecomesRealSQL(t *testing.T) {
	got, ok := OracleSQLPlus("SHOW USER")
	if !ok {
		t.Fatal("SHOW USER 应当被翻译 —— 否则原样发给 Oracle 就是 ORA-00900")
	}
	if !strings.Contains(strings.ToUpper(got), "USER") || !strings.Contains(strings.ToUpper(got), "DUAL") {
		t.Errorf("应当翻成一条查当前用户的 SQL,实际: %q", got)
	}
}

// 大小写、多余空白、结尾分号 —— 人怎么敲都得认。
func TestOracleSQLPlus_AcceptsHowPeopleActuallyType(t *testing.T) {
	for _, in := range []string{"SHOW USER", "show user", "  Show   User  ", "SHOW USER;", "show user ;"} {
		if _, ok := OracleSQLPlus(in); !ok {
			t.Errorf("%q 应当被认出来", in)
		}
	}
}

func TestOracleSQLPlus_OtherSupportedCommands(t *testing.T) {
	cases := map[string]string{
		"SHOW CON_NAME":       "CON_NAME",
		"SHOW RELEASE":        "V$VERSION",
		"SHOW VERSION":        "V$VERSION",
		"SHOW PARAMETER open_cursors": "V$PARAMETER",
	}
	for in, want := range cases {
		got, ok := OracleSQLPlus(in)
		if !ok {
			t.Errorf("%q 应当被翻译", in)
			continue
		}
		if !strings.Contains(strings.ToUpper(got), strings.ToUpper(want)) {
			t.Errorf("%q → %q,期望里带 %s", in, got, want)
		}
	}
}

// SHOW PARAMETER 的名字要按子串模糊匹配 —— sqlplus 就是这么做的,人也是这么用的
// (敲 `show parameter cursor` 想看到一组,不是一个)。
func TestOracleSQLPlus_ShowParameterMatchesLikeSqlplusDoes(t *testing.T) {
	got, _ := OracleSQLPlus("SHOW PARAMETER cursor")
	if !strings.Contains(got, "LIKE '%cursor%'") {
		t.Errorf("应当按子串匹配,实际: %q", got)
	}
}

// 翻不了的原样下发。一条报 ORA-00900 的语句,比一条被平台猜着改写成别的东西然后
// 悄悄执行了的语句要好得多。
func TestOracleSQLPlus_LeavesWhatItCannotTranslateAlone(t *testing.T) {
	for _, in := range []string{
		"SHOW ERRORS",           // 问的是 sqlplus 自己的会话状态
		"SHOW SQLCODE",          // 同上
		"SELECT * FROM t_user",  // 本来就是 SQL
		"SHOW",                  // 光一个词
		"SHOWER USER",           // 不是 SHOW
	} {
		got, ok := OracleSQLPlus(in)
		if ok {
			t.Errorf("%q 不该被翻译,却变成了 %q", in, got)
		}
		if got != in {
			t.Errorf("没翻译就该原样返回,%q → %q", in, got)
		}
	}
}

// ——— 这两条是这次改动真正的边界 ———

// 翻译发生在判定之后,所以**判定看到的仍是原文**。字典匹配的是文本里的那个词,
// 一条写着 SHOW 的规则必须照样命中 —— 先翻译再判定就是一个绕过。
func TestOracleSQLPlus_TranslationDoesNotHideTheOriginalFromTheDictionary(t *testing.T) {
	// 判定用的动词解析看的是原文。
	if v := ParseVerb("SHOW USER"); !strings.EqualFold(v, "SHOW") {
		t.Errorf("判定应当看到原文的动词 SHOW,实际 %q", v)
	}
	// 翻译只改下发的那一份,不改输入。
	in := "SHOW USER"
	if _, ok := OracleSQLPlus(in); !ok {
		t.Fatal("前置条件")
	}
	if in != "SHOW USER" {
		t.Error("翻译不该改动传入的字符串")
	}
}

// 翻译出来的 SQL 本身必须仍然是一次读 —— 它不能变成一条会改数据的语句。
func TestOracleSQLPlus_EveryTranslationIsStillARead(t *testing.T) {
	for _, in := range []string{"SHOW USER", "SHOW CON_NAME", "SHOW RELEASE", "SHOW PARAMETER cursor"} {
		got, ok := OracleSQLPlus(in)
		if !ok {
			t.Fatalf("%q 应当被翻译", in)
		}
		if !IsRead(got) {
			t.Errorf("%q 翻成了 %q,而它不是一次读 —— 翻译绝不能把读变成写", in, got)
		}
	}
}

// DESC[RIBE] 是同一个 bug —— 它也是 SQL*Plus 的命令,服务端同样报 ORA-00900。
func TestOracleSQLPlus_DescribeBecomesADataDictionaryQuery(t *testing.T) {
	got, ok := OracleSQLPlus("DESC t_user")
	if !ok {
		t.Fatal("DESC 应当被翻译 —— 它和 SHOW 一样是客户端命令")
	}
	up := strings.ToUpper(got)
	if !strings.Contains(up, "ALL_TAB_COLUMNS") {
		t.Errorf("应当查数据字典,实际: %q", got)
	}
	// Oracle 把未加引号的标识符按大写存,敲 desc t_user 的人要看到 T_USER 的列。
	if !strings.Contains(got, "'T_USER'") {
		t.Errorf("表名应当转成大写去匹配数据字典,实际: %q", got)
	}
}

func TestOracleSQLPlus_DescribeAcceptsSchemaQualifiedNames(t *testing.T) {
	got, ok := OracleSQLPlus("describe scott.emp")
	if !ok {
		t.Fatal("带 schema 的 DESCRIBE 也应当被翻译")
	}
	if !strings.Contains(got, "'EMP'") || !strings.Contains(got, "'SCOTT'") {
		t.Errorf("schema 与表名都要用上,实际: %q", got)
	}
}

// 这条正则是一道安全边界:名字最终会拼进字符串字面量。奇怪的名字一律不翻译,
// 原样下发让 Oracle 自己拒绝 —— 绝不能猜着拼出一条别的 SQL 然后执行掉。
func TestOracleSQLPlus_DescribeRefusesAnythingItCannotSafelyQuote(t *testing.T) {
	for _, in := range []string{
		`DESC "Weird Name"`,
		`DESC t_user; DROP TABLE t_x`,
		`DESC t' OR '1'='1`,
		`DESC (SELECT 1 FROM DUAL)`,
	} {
		got, ok := OracleSQLPlus(in)
		if ok {
			t.Errorf("%q 不该被翻译,却变成了 %q", in, got)
		}
		if got != strings.TrimSpace(in) {
			t.Errorf("没翻译就该原样返回,%q → %q", in, got)
		}
	}
}

// ORDER BY x DESC 不是 DESCRIBE —— 它以 SELECT 开头,碰都不该碰。
func TestOracleSQLPlus_OrderByDescIsNotDescribe(t *testing.T) {
	in := "SELECT * FROM t_user ORDER BY id DESC"
	got, ok := OracleSQLPlus(in)
	if ok || got != in {
		t.Errorf("ORDER BY ... DESC 不该被当成 DESCRIBE,%q → %q (ok=%v)", in, got, ok)
	}
}
