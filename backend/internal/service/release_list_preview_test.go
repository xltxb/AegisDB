package service

import (
	"strings"
	"testing"
	"time"

	"velagateway/pkg/sqlutil"
)

// 2026-11-20:发布单列表改成"先截短再脱敏"。显示出来的预览必须与原先"全文脱敏再截 400"
// 一字不差,口令照样被遮住;而几 MB 的脚本不能再让列表跟着慢。
func TestListSQLPreviewMatchesFullRedactThenClip(t *testing.T) {
	old := func(s string) string { return clip(sqlutil.RedactSecrets(s), listSQLPreviewRunes) }
	insert := "INSERT INTO t_account_config (account_info_id, account_type, account_name) VALUES (1001, 'viber', '中文账号名');\n"
	cases := map[string]string{
		"空":                 "",
		"短脚本":               "SELECT 1;",
		"开头就有口令":            "CREATE USER 'app'@'%' IDENTIFIED BY 'S3cret!pw';\n" + strings.Repeat(insert, 50),
		"口令在 380 字附近跨过预览边界": strings.Repeat("-", 380) + "\nALTER USER 'a'@'%' IDENTIFIED BY 'TopSecretValue';\n" + strings.Repeat(insert, 20),
		"口令在窗口边界附近":         strings.Repeat("x", 990) + " IDENTIFIED BY 'cutAtWindowEdge';",
		"介于预览和窗口之间":         strings.Repeat("中", 700),
		"恰好 400 字":          strings.Repeat("中", 400),
		"401 字":             strings.Repeat("中", 401),
		"多字节大脚本":            strings.Repeat(insert, 20000),
	}
	for name, sql := range cases {
		if got, want := listSQLPreview(sql), old(sql); got != want {
			t.Errorf("%s: 预览与原先不一致\n got: %q\nwant: %q", name, got, want)
		}
	}
	if p := listSQLPreview(cases["开头就有口令"]); strings.Contains(p, "S3cret") {
		t.Errorf("预览里的口令没被遮住: %q", p)
	}
	if p := listSQLPreview(cases["口令在 380 字附近跨过预览边界"]); strings.Contains(p, "TopSecret") {
		t.Errorf("跨预览边界的口令露出了开头: %q", p)
	}
}

func TestListSQLPreviewDoesNotScanTheWholeScript(t *testing.T) {
	big := strings.Repeat("INSERT INTO t VALUES (1, 'some text here', 42);\n", 8*1024*1024/48) // ≈8MB
	start := time.Now()
	for i := 0; i < 50; i++ { // 一页 50 张都是这么大的单子
		listSQLPreview(big)
	}
	// 原先 8MB × 50 张要扫 400MB 正文(按 0.46 秒/MB 是几分钟);现在每张只看前 1000 字。
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("50 张 8MB 脚本的预览用了 %v,仍在随脚本大小增长", d)
	}
}

func TestHeadRunes(t *testing.T) {
	if got := headRunes("中文abc", 3); got != "中文a" {
		t.Errorf("按字符截,不按字节: %q", got)
	}
	if got := headRunes("ab", 5); got != "ab" {
		t.Errorf("不足 n 原样返回: %q", got)
	}
	if got := headRunes("", 3); got != "" {
		t.Errorf("空串: %q", got)
	}
}
