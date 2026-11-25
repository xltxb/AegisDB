package gateway

import "velagateway/pkg/sqlutil"

// MySQL 的反斜杠转义与网关词法的分歧。
//
// 网关的词法(StripComments / blankQuoted / blankStringLiterals / SplitStatements)按 SQL 标准
// 切字符串:只有两个连写的引号('' "")是转义,反斜杠是普通字符。这是有意的 —— PostgreSQL、
// Oracle、开了 NO_BACKSLASH_ESCAPES 的 MySQL 都这么认,反过来按反斜杠切会在这些库上把真语法
// 当成字符串抹掉。
//
// 但 MySQL / MariaDB / TiDB 默认把 \' 当转义。于是
//
//	SELECT 'a\'', (SELECT authentication_string FROM mysql.user) AS 'z'
//
// MySQL 读到的是字符串 a' 加一个子查询;网关读到的是字符串 'a\' 、再一个字符串
// ', (SELECT … mysql.user) AS ' —— 子查询整个被当成数据抹掉,跨库、系统库、危险函数、打码
// 按表生效、高危字典,凡是"先抹字面量再看结构"的判断全都看不见它。
//
// 这里不去猜目标库开没开 NO_BACKSLASH_ESCAPES,而是把**两种理解都拿来判**:
// backslashReading 给出 MySQL 那种理解下的等价文本,调用方对两份文本各判一次、取更严的结果;
// 业务查询(只读、按库)则直接拒绝两种理解不一致的语句。

// backslashReading 返回 sql 在"反斜杠是转义"这种理解下的等价写法:字符串里被反斜杠转义的
// 引号 \' / \" 改写成标准的 '' / "",其余字节不动,长度不变。
//
// 改写后的文本交给网关现有的(标准)词法,得到的字面量边界就是 MySQL 默认模式下的边界。
// 注释照 StripComments 的规则跳过(-- 与 /* */;# 不算注释,理由同 StripComments),
// 反引号标识符里 MySQL 本就不认反斜杠转义。
func backslashReading(sql string) string {
	b := []byte(sql)
	n := len(b)
	for i := 0; i < n; i++ {
		c := b[i]
		switch {
		case c == '\'' || c == '"':
			q := c
			i++
			for i < n {
				switch {
				case b[i] == '\\' && i+1 < n:
					if b[i+1] == q {
						b[i] = q // \' → '':标准词法下同样是字符串里的一个引号
					}
					i += 2
					continue
				case b[i] == q:
					if i+1 < n && b[i+1] == q {
						i += 2
						continue
					}
				}
				if b[i] == q {
					break
				}
				i++
			}
		case c == '`':
			i++
			for i < n && b[i] != '`' {
				i++
			}
		case c == '-' && i+1 < n && b[i+1] == '-':
			for i < n && b[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && b[i+1] == '*' && sqlutil.ExecCommentOpener(sql, i) == 0: // 可执行注释(/*! /*M! /*T!)的内容照常读
			j := i + 2
			for j+1 < n && !(b[j] == '*' && b[j+1] == '/') {
				j++
			}
			i = j + 1
		}
	}
	return string(b)
}

// BackslashAmbiguous 报告 sql 在两种理解下字面量边界是否不同 —— 也就是反斜杠转义的引号
// 会让"哪些是数据、哪些是语法"取决于目标库的 sql_mode。
func BackslashAmbiguous(sql string) bool {
	return backslashReading(sql) != sql
}

// judgeReadings 是 sqlReadings 再叠上 `#` 注释的读法(hash_comment.go):字面量边界和 `#` 都有分歧时,
// 只有叠在一起才看得见。给不分引擎、只看结构的判断用,多判一份只会更严。
func judgeReadings(sql string) []string {
	var out []string
	for _, text := range append([]string{sql}, hashReadings(sql)...) {
		for _, r := range sqlReadings(text) {
			if !contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// sqlReadings 返回要判的文本:原文,以及(有分歧时)反斜杠理解下的等价文本。
// 调用方对每一份都判,取最严的结果 —— 多判一份只会更严,不会更松。
func sqlReadings(sql string) []string {
	if alt := backslashReading(sql); alt != sql {
		return []string{sql, alt}
	}
	return []string{sql}
}
