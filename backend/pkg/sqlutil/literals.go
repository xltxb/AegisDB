package sqlutil

// 抹掉 SQL 里字符串字面量与被引起来的标识符的**内容**,只留下引号本身。
//
// 这件事此前有两份实现:判定层一份(把字面量抹掉再找关键词,免得 `note='delete'`
// 被当成一条 DELETE),审查层一份(同样的理由,加上要按位置把问题指回原文)。两份
// 各自演化,对反斜杠、反引号、换行的处理已经分叉 —— 而分叉的后果是同一条语句在
// 两处读出不同的结构。合并成这一份,差异用选项表达。

// LiteralMask 说这次掩码按谁的规矩读引号。
//
// 两个字段都关着(零值)就是最保守的读法:只有 ' 和 " 起引号,反斜杠不转义。
type LiteralMask struct {
	// Backtick —— 反引号是否也算引号。
	//
	// 判定层要开:MySQL 的 `` `drop` `` 是一个标识符,不是那个动词。审查层要关:它的
	// 规则正要看见那些标识符(列名的前缀、表名的命名规范),抹掉就一条也触发不了。
	Backtick bool
	// Backslash —— `\` 是否转义下一个字节。
	//
	// 这不是风格问题,是引擎差异:MySQL 默认 sql_mode 下 `\'` 是转义的引号,字符串
	// 继续往下延伸;PostgreSQL(standard_conforming_strings)、Oracle、SQLite 的标准
	// 字符串里反斜杠只是一个普通字符,引号就地收尾。同一串字节因此是两条不同的
	// 语句 —— 见 gateway.backslashEscapes 如何按引擎选这一项。
	Backslash bool
}

// MaskLiterals 把引号里的每个字节换成空格,引号本身、以及引号外的一切原样留下。
//
// 两条不变式,两个调用方都靠着它们:
//
//   - **长度不变**。掩码文本上算出的位置要能直接映射回原文 —— 审查报告要指出问题
//     在第几行,判定层的命中要能摘出原句。
//   - **换行留着**。把字面量里的换行抹成空格会让行号少数几行,报告指向的位置就和
//     人在编辑器里看到的对不上了。
func MaskLiterals(sql string, opt LiteralMask) string {
	b := []byte(sql)
	for i := 0; i < len(b); i++ {
		q := b[i]
		if q != '\'' && q != '"' && !(q == '`' && opt.Backtick) {
			continue
		}
		// 反引号括的是标识符,里面的反斜杠哪个引擎都不转义。
		esc := opt.Backslash && q != '`'
		i++
		for i < len(b) {
			if esc && b[i] == '\\' && i+1 < len(b) { // `\x` 整对都是内容 —— 包括 `\'`
				blankByte(b, i)
				blankByte(b, i+1)
				i += 2
				continue
			}
			if b[i] == q {
				if i+1 < len(b) && b[i+1] == q { // 双写引号 = 转义,仍在字面量里
					blankByte(b, i)
					blankByte(b, i+1)
					i += 2
					continue
				}
				break // 收尾的那个引号
			}
			blankByte(b, i)
			i++
		}
	}
	return string(b)
}

func blankByte(b []byte, i int) {
	if b[i] != '\n' {
		b[i] = ' '
	}
}
