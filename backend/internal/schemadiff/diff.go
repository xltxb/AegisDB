package schemadiff

import (
	"fmt"
	"sort"
	"strings"
)

// DiffSchemas 比较左右两个库的结构。两边引擎族应相同(调用方用 SameFamily 先判);名字的大小写
// 规则按左边的引擎。
func DiffSchemas(left, right *Snapshot, opt Options) *Result {
	f := familyOf(left.Engine)
	res := &Result{Same: []string{}, OnlyLeft: []string{}, OnlyRight: []string{}, Changed: []TableDiff{}}
	res.Summary.Left, res.Summary.Right = len(left.Tables), len(right.Tables)

	rights := map[string]*Table{}
	for i := range right.Tables {
		rights[nameKey(f, right.Tables[i].Name)] = &right.Tables[i]
	}
	seen := map[string]bool{}
	for i := range left.Tables {
		l := &left.Tables[i]
		k := nameKey(f, l.Name)
		r, ok := rights[k]
		if !ok {
			res.OnlyLeft = append(res.OnlyLeft, l.Name)
			continue
		}
		seen[k] = true
		if td := diffTable(f, l, r, opt); td != nil {
			res.Changed = append(res.Changed, *td)
		} else {
			res.Same = append(res.Same, l.Name)
		}
	}
	for i := range right.Tables {
		if !seen[nameKey(f, right.Tables[i].Name)] {
			res.OnlyRight = append(res.OnlyRight, right.Tables[i].Name)
		}
	}
	sort.Strings(res.Same)
	sort.Strings(res.OnlyLeft)
	sort.Strings(res.OnlyRight)
	sort.Slice(res.Changed, func(i, j int) bool { return res.Changed[i].Table < res.Changed[j].Table })
	res.Summary.Same, res.Summary.OnlyLeft = len(res.Same), len(res.OnlyLeft)
	res.Summary.OnlyRight, res.Summary.Changed = len(res.OnlyRight), len(res.Changed)
	return res
}

// diffTable 返回一张表的差异;两边一致返回 nil。
func diffTable(f family, l, r *Table, opt Options) *TableDiff {
	td := &TableDiff{Table: l.Name}
	if l.Kind != r.Kind {
		td.Kind = &Pair{Left: l.Kind, Right: r.Kind}
	}
	if !opt.IgnoreComments && normComment(l.Comment) != normComment(r.Comment) {
		td.Comment = &Pair{Left: l.Comment, Right: r.Comment}
	}
	if lp, rp := keyList(f, l.PrimaryKey), keyList(f, r.PrimaryKey); lp != rp {
		td.PrimaryKey = &Pair{Left: showCols(l.PrimaryKey), Right: showCols(r.PrimaryKey)}
	}
	td.Columns = diffColumns(f, l.Columns, r.Columns, opt)
	td.Indexes = diffIndexes(f, l, r, opt)
	if td.Kind == nil && td.Comment == nil && td.PrimaryKey == nil && len(td.Columns) == 0 && len(td.Indexes) == 0 {
		return nil
	}
	td.LeftDef, td.RightDef = l, r
	return td
}

func byOrdinal(cols []Column) []Column {
	out := append([]Column(nil), cols...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ordinal < out[j].Ordinal })
	return out
}

func diffColumns(f family, lcols, rcols []Column, opt Options) []ColumnDiff {
	lcols, rcols = byOrdinal(lcols), byOrdinal(rcols)
	rByKey := map[string]int{}
	for i, c := range rcols {
		rByKey[nameKey(f, c.Name)] = i
	}
	lByKey := map[string]int{}
	for i, c := range lcols {
		lByKey[nameKey(f, c.Name)] = i
	}

	// 列顺序:只看两边都有的列的相对顺序(ordinal 数值会因删过列而有空洞,不能直接比)。
	// 不在最长公共子序列里的,就是被挪过位置的列 —— 把一列挪到最前,只报它一列,
	// 而不是把被它挤动的每一列都报一遍。
	moved := map[string]bool{}
	if !opt.IgnoreColumnOrder {
		var lc, rc []string
		for _, c := range lcols {
			if _, ok := rByKey[nameKey(f, c.Name)]; ok {
				lc = append(lc, nameKey(f, c.Name))
			}
		}
		for _, c := range rcols {
			if _, ok := lByKey[nameKey(f, c.Name)]; ok {
				rc = append(rc, nameKey(f, c.Name))
			}
		}
		keep := lcs(lc, rc)
		for _, k := range lc {
			if !keep[k] {
				moved[k] = true
			}
		}
	}

	var out []ColumnDiff
	for li, l := range lcols {
		k := nameKey(f, l.Name)
		ri, ok := rByKey[k]
		if !ok {
			out = append(out, ColumnDiff{Column: l.Name, Status: OnlyLeft, Left: showColumn(l)})
			continue
		}
		r := rcols[ri]
		var ch []Change
		if normType(f, l.DataType) != normType(f, r.DataType) {
			ch = append(ch, Change{Attr: "type", Left: l.DataType, Right: r.DataType})
		}
		if l.Nullable != r.Nullable {
			ch = append(ch, Change{Attr: "nullable", Left: showNullable(l.Nullable), Right: showNullable(r.Nullable)})
		}
		ld, lh := normDefault(f, l)
		rd, rh := normDefault(f, r)
		if lh != rh || ld != rd {
			ch = append(ch, Change{Attr: "default", Left: showDefault(l), Right: showDefault(r)})
		}
		if !opt.IgnoreComments && normComment(l.Comment) != normComment(r.Comment) {
			ch = append(ch, Change{Attr: "comment", Left: l.Comment, Right: r.Comment})
		}
		if moved[k] {
			ch = append(ch, Change{Attr: "position", Left: fmt.Sprint(li + 1), Right: fmt.Sprint(ri + 1)})
		}
		if len(ch) > 0 {
			out = append(out, ColumnDiff{Column: l.Name, Status: Changed, Changes: ch})
		}
	}
	for _, r := range rcols {
		if _, ok := lByKey[nameKey(f, r.Name)]; !ok {
			out = append(out, ColumnDiff{Column: r.Name, Status: OnlyRight, Right: showColumn(r)})
		}
	}
	return out
}

// lcs 返回两个序列最长公共子序列里的元素(元素各自唯一)。
func lcs(a, b []string) map[string]bool {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	keep := map[string]bool{}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i] == b[j]:
			keep[a[i]] = true
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return keep
}

// diffIndexes 比较索引。主键单独比(PrimaryKey),支撑主键的那个唯一索引不在这里重复报。
//
// 匹配:先按名字(两边都不是系统命名、也没勾"忽略索引名"时),剩下的按"是否唯一 + 列组成"匹配。
// 按名字配上的,列组成或唯一性不同就是"改了";都配不上的,就是只在一边。
func diffIndexes(f family, l, r *Table, opt Options) []IndexDiff {
	li := userIndexes(l)
	ri := userIndexes(r)
	usedR := make([]bool, len(ri))
	matchedL := make([]bool, len(li))
	var out []IndexDiff

	if !opt.IgnoreIndexNames {
		for i, a := range li {
			if a.SystemNamed {
				continue
			}
			for j, b := range ri {
				if usedR[j] || b.SystemNamed || nameKey(f, a.Name) != nameKey(f, b.Name) {
					continue
				}
				usedR[j], matchedL[i] = true, true
				if sig(f, a) != sig(f, b) {
					out = append(out, IndexDiff{Index: a.Name, Status: Changed, Left: showIndex(a), Right: showIndex(b)})
				}
				break
			}
		}
	}
	for i, a := range li {
		if matchedL[i] {
			continue
		}
		// 按名字能配上的已经在上面处理了;剩下的只有系统命名 / 忽略索引名时才按列组成配。
		for j, b := range ri {
			if usedR[j] || sig(f, a) != sig(f, b) {
				continue
			}
			if !opt.IgnoreIndexNames && !a.SystemNamed && !b.SystemNamed {
				continue
			}
			usedR[j], matchedL[i] = true, true
			break
		}
		if !matchedL[i] {
			out = append(out, IndexDiff{Index: a.Name, Status: OnlyLeft, Left: showIndex(a)})
		}
	}
	for j, b := range ri {
		if !usedR[j] {
			out = append(out, IndexDiff{Index: b.Name, Status: OnlyRight, Right: showIndex(b)})
		}
	}
	return out
}

// userIndexes 是参与比较的索引:只去掉支撑主键的那一个(主键单独比)。与主键同列、但不是它的
// 唯一索引照常比 —— 只在一边多建了一个,正是要报的差异。
func userIndexes(t *Table) []Index {
	var out []Index
	for _, ix := range t.Indexes {
		if !ix.Primary {
			out = append(out, ix)
		}
	}
	return out
}

func sig(f family, ix Index) string {
	u := ""
	if ix.Unique {
		u = "u:"
	}
	return u + keyList(f, ix.Columns)
}

func keyList(f family, cols []string) string {
	ks := make([]string, len(cols))
	for i, c := range cols {
		// 表达式索引的"列"是表达式原文:只把引号外转小写(MySQL 名字不分大小写),
		// json_extract(doc,'$.A') 与 '$.a' 是两个不同的索引。
		if f == famMySQL && strings.ContainsAny(c, "('") {
			ks[i] = lowerOutsideQuotes(c)
		} else {
			ks[i] = nameKey(f, c)
		}
	}
	return strings.Join(ks, ",")
}

func showCols(cols []string) string { return "(" + strings.Join(cols, ", ") + ")" }

func showIndex(ix Index) string {
	if ix.Unique {
		return "UNIQUE " + showCols(ix.Columns)
	}
	return showCols(ix.Columns)
}

func showColumn(c Column) string {
	s := c.DataType + " " + showNullable(c.Nullable)
	if c.HasDefault {
		s += " DEFAULT " + c.Default
	}
	return s
}
