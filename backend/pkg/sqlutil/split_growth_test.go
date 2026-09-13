package sqlutil

// 拆分一份注释居多的脚本,代价必须随长度线性增长。
//
// PL/SQL 整块识别与 DELIMITER 指令都只在**语句边界**上生效,所以循环每个字节都要问
// 一次"正在攒的这条语句目前还是空的吗"。这个问题从前的答法是 TrimSpace(b.String()) ——
// 重扫一遍缓冲区里已经攒下的全部内容。
//
// 一般脚本上看不出来:每遇到一个分号缓冲区就清空,所以它一直很短。但注释是被跳过的,
// 跳过时只留下空白 —— 一份注释占绝大多数、迟迟等不到分号的文件,缓冲区就一路涨到
// 整个文件那么大,而每个字节都要把它重扫一遍。O(n²)。
//
// 这不是理论问题:上传通道明确接受 15MB 的脚本。实测 800KB 全注释要 8.9 秒,按二次
// 外推,15MB 是**五十多分钟** —— 而它最后得出的结论是"0 条语句"。一次上传就能占住
// 一个工作线程接近一小时,而且这种文件不必是恶意的:一个大注释头、一份带 `--` 行的
// 导出文件都长这样。
//
// 这里不断言绝对耗时(那只会随机器快慢飘),断言的是**翻倍的代价**:长度翻一倍,用时
// 该跟着翻一倍,而不是翻四倍。

import (
	"strings"
	"testing"
	"time"
)

func commentScript(size int) string {
	var b strings.Builder
	for b.Len() < size {
		b.WriteString("-- 这一行是注释,没有语句\n")
	}
	return b.String()
}

func TestSplitStatements_CommentHeavyScriptStaysLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("计时用例")
	}
	timeAt := func(size int) time.Duration {
		s := commentScript(size)
		start := time.Now()
		got := SplitStatements(s)
		d := time.Since(start)
		if len(got) != 0 {
			t.Fatalf("%dKB 全注释该拆出 0 条语句,实际 %d 条", size>>10, len(got))
		}
		return d
	}

	small := timeAt(1 << 20)
	large := timeAt(2 << 20)
	ratio := float64(large) / float64(small)
	t.Logf("1MB %s / 2MB %s(翻倍比 %.2f)", small.Round(time.Millisecond), large.Round(time.Millisecond), ratio)

	// 线性是 2.0,二次是 4.0。3.0 把两者分得开,又给机器抖动留了余地。
	if ratio > 3.0 {
		t.Errorf("长度翻倍,用时翻了 %.2f 倍(1MB %s → 2MB %s)—— 代价是二次的,15MB 的上传会占住一个线程几十分钟",
			ratio, small.Round(time.Millisecond), large.Round(time.Millisecond))
	}
}
