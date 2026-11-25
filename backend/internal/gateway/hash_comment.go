package gateway

import (
	"strings"

	"aegisdb/pkg/sqlutil"
)

// MySQL 系的 `#` 行注释。
//
// 拆分器和 StripComments 有意不认 `#`:PostgreSQL 里它是运算符,当注释会把真语句从判定文本里删掉
// (ER2)。这对"把语句判得更危险"的那些判断是安全的,但有两类判断是反过来的 ——
//
//	SELECT 'x' INTO#y<换行>OUTFILE '/tmp/a'      判定看 INTO 与 OUTFILE 中间不是空白,没认出写文件
//	DELETE FROM t # WHERE id=1                   判定看到了 WHERE,服务器看到的是全表删除
//
// 所以不改那些函数,而是多给一份"服务器的读法",和反斜杠的处理同一个思路(backslash.go):每份都判,
// 取最严的。原文那份不动,原来拦得住的照样拦得住。

// hashReadings 返回把 `#` 当注释之后的文本;没有字面量外的 `#` 时为空。字面量的边界有两种理解
// (反斜杠是否转义),两种都给 —— 目标库开没开 NO_BACKSLASH_ESCAPES 这里不猜。
func hashReadings(sql string) []string {
	if !strings.Contains(sql, "#") {
		return nil
	}
	var out []string
	for _, backslash := range []bool{false, true} {
		if s := stripHashComments(sql, backslash); s != sql && !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// mysqlHashReadings 与 hashReadings 相同,用在只对 MySQL 系成立的判断上(调用方先看过引擎)。
// 名字提醒读者:这份读法在别的引擎上不对。
func mysqlHashReadings(sql string) []string { return hashReadings(sql) }

// stripHashComments 按 MySQL 的词法去掉 `#` 到行尾(留下换行),其余字节不动。
func stripHashComments(sql string, backslash bool) string {
	var b strings.Builder
	b.Grow(len(sql))
	n := len(sql)
	for i := 0; i < n; i++ {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < n && sql[j] != c {
				if backslash && c != '`' && sql[j] == '\\' {
					j++
				}
				j++
			}
			if j >= n {
				j = n - 1
			}
			b.WriteString(sql[i : j+1])
			i = j
		case c == '-' && i+1 < n && sql[i+1] == '-' && (i+2 >= n || sql[i+2] <= ' '):
			j := i
			for j < n && sql[j] != '\n' {
				j++
			}
			b.WriteString(sql[i:j])
			i = j - 1
		case c == '/' && i+1 < n && sql[i+1] == '*':
			if m := sqlutil.ExecCommentOpener(sql, i); m > 0 { // 可执行注释的内容是代码,照常读
				b.WriteString(sql[i : i+m])
				i += m - 1
				continue
			}
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				b.WriteString(sql[i:])
				return b.String()
			}
			b.WriteString(sql[i : i+2+j+2])
			i += 2 + j + 1
		case c == '#':
			j := i
			for j < n && sql[j] != '\n' {
				j++
			}
			b.WriteByte(' ')
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
