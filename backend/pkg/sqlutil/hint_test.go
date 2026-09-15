package sqlutil

import "testing"

// 优化器提示 `/*+ ... */` 必须原样留在语句里。
//
// 它长得像块注释,但服务端会读它 —— 丢掉不会报错,只是执行计划悄悄变了,而写它的人
// 拿不到任何提示。这比语法错难查得多:语法错当场就说,丢掉的 hint 要等到某天有人
// 发现同一条查询在终端里比在客户端里慢很多。
//
// 尤其阴的是它此前只在**批量**时丢:execCommand 单条走原文、多条走拆分后的文本,
// 所以同一条 SQL 单独跑 hint 生效、和别的语句一起粘贴就失效 —— 行为取决于旁边写了
// 什么,那是最难复现的一类问题。
func TestSplit_KeepsOptimiserHints(t *testing.T) {
	for _, c := range []struct{ name, sql, want string }{
		{"MySQL/Oracle 风格", "SELECT /*+ INDEX(t idx) */ * FROM t;", "SELECT /*+ INDEX(t idx) */ * FROM t"},
		{"提示里有分号也不拆", "SELECT /*+ a;b */ 1;", "SELECT /*+ a;b */ 1"},
		{"多条时同样保留", "SELECT /*+ FULL(t) */ 1; SELECT 2;", "SELECT /*+ FULL(t) */ 1"},
		// 普通块注释仍旧换成空白 —— 这是既有行为,空白归一在上一层(service.splitStatements)。
		{"普通块注释仍然丢掉", "SELECT /* 说明 */ 1;", "SELECT   1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := SplitStatements(c.sql)
			if len(got) == 0 {
				t.Fatalf("一条都没拆出来")
			}
			if got[0] != c.want {
				t.Errorf("拆出 %q, 想要 %q", got[0], c.want)
			}
		})
	}
}
