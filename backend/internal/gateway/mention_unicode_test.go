package gateway

import "testing"

// mentionsColumn 是两道闸的共同底座:脱敏按表达式比对的第二步,和"嵌套结构是否引用了
// 敏感字段"的导出判断。它原先用 \b 判词边界 —— 而 Go 正则的 \b **只认 ASCII**:
// 在"身份证"两侧,一边是空格、一边是汉字,谁都不是 ASCII 词字符,于是根本没有边界,
// \b身份证\b 永远匹配不上。
//
// 后果是中文字段名的规则整个失效:SELECT SUBSTR(身份证,1,6) AS x 不打码,
// SELECT x FROM (SELECT 身份证 AS x FROM t) 也不进审批 —— 而这两条恰恰是这套机制
// 要拦的东西。规则字段名是什么文字,不该决定它管不管用。
func TestMentionsColumn_NonASCIIIdentifiers(t *testing.T) {
	cases := []struct {
		expr, col string
		want      bool
	}{
		{"SUBSTR(身份证, 1, 6)", "身份证", true},
		{"d.身份证 AS x", "身份证", true},
		{"身份证", "身份证", true},
		{"`身份证`", "身份证", true},
		// 是别的词的一部分就不算:"身份证号" 不是 "身份证" 这一列
		{"身份证号", "身份证", false},
		{"客户身份证", "身份证", false},
		// ASCII 的行为保持不变
		{"SUBSTR(id_card,1,6)", "id_card", true},
		{"my_id_card_x", "id_card", false},
		{"ID_CARD", "id_card", true},
	}
	for _, c := range cases {
		if got := mentionsColumn(c.expr, c.col); got != c.want {
			t.Errorf("mentionsColumn(%q, %q) = %v, want %v", c.expr, c.col, got, c.want)
		}
	}
}

// 同一个洞在导出判断上的样子:中文敏感字段被改名带出去,必须进审批。
func TestMaskEvasionColumn_NonASCIIColumn(t *testing.T) {
	rules := []SensitiveRule{{Table: "*", Column: "身份证"}}
	if got := MaskEvasionColumn("SELECT x FROM (SELECT 身份证 AS x FROM t_user) s", rules); got != "身份证" {
		t.Fatalf("中文敏感字段被改名带出,应进审批,实际 %q", got)
	}
	if got := MaskEvasionColumn("WITH u AS (SELECT 姓名 FROM t_user) SELECT 姓名 FROM u", rules); got != "" {
		t.Fatalf("没提到敏感字段应直接放行,实际 %q", got)
	}
}

// 脱敏第二步(按表达式)对中文字段同样要兜住。
func TestSensitiveMaskTargets_NonASCIIColumnInExpression(t *testing.T) {
	rules := []SensitiveRule{{Table: "*", Column: "身份证", Style: MaskPartial}}
	cols := []string{"x"}
	got := SensitiveMaskTargets("SELECT SUBSTR(身份证,1,6) AS x FROM t_user", cols, rules)
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("表达式里提到了中文敏感字段,第 0 列应打码,实际 %v", got)
	}
}
