package repository

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LIKE 的转义字符曾经是反斜杠,写成 ESCAPE '\'。PostgreSQL 认它;MySQL 把字面量里的反斜杠
// 当转义符,'\' 被读成"一个转义了的引号",字符串没结束,整句语法错误。测试存储库不是
// MySQL,于是测试全绿、生产的审计搜索和审批搜索一直查不出东西。
//
// 这一组钉两件事:escapeLike 的输出形状,以及**源码里不许再出现反斜杠形式的 ESCAPE**。
// 后者是静态守卫:这个坑的性质是"本地永远测不出来",所以只能在源码层面挡。

func TestEscapeLike_UsesNonBackslashEscape(t *testing.T) {
	cases := map[string]string{
		"t_order":  "t!_order",  // 下划线不再是通配符:搜 t_order 不该连 tXorder 一起命中
		"100%":     "100!%",     // 百分号同理
		"a!b":      "a!!b",      // 转义字符自己要加倍
		`a\b`:      `a\b`,       // 反斜杠现在只是个普通字符,原样保留
		"Lin Wei":  "Lin Wei",   // 普通文本不动
		"!_%":      "!!!_!%",    // 三个都出现:每个各自转义,顺序不互相干扰
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
	if !strings.Contains(likeClause, "ESCAPE '"+likeEscape+"'") || likeEscape == `\` {
		t.Fatalf("likeClause 必须显式声明非反斜杠的转义字符,实际 %q", likeClause)
	}
}

// 源码级守卫:repository 包里任何 Go 文件都不得再写 ESCAPE '\'(单反斜杠或双反斜杠形式)。
// 它在 PostgreSQL 存储库上永远测不出问题,所以只能在这里挡。
func TestNoBackslashEscapeInLike(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("列举源码失败: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s: %v", f, err)
		}
		// 只看代码,不看注释:解释"为什么不能这么写"的注释里必然要出现那个写法。
		for i, line := range strings.Split(string(src), "\n") {
			if j := strings.Index(line, "//"); j >= 0 {
				line = line[:j]
			}
			for _, bad := range []string{`ESCAPE '\'`, `ESCAPE '\\'`} {
				if strings.Contains(line, bad) {
					t.Errorf("%s:%d 含有 %s —— MySQL 上这是语法错误(见 likeEscape 的说明),请改用 likeClause", f, i+1, bad)
				}
			}
		}
	}
}
