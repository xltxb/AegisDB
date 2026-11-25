package review

import "testing"

// partitionNameRe 曾经是一条**永远匹配不上任何东西**的正则。
//
// 源码里写的是 `(?i)\bPARTITION\s+(...)`,但落到文件里的是一个字面的**退格符**(0x08)
// 加上 `s+` —— 反斜杠在某次编辑中被吃掉了。于是这条正则要求 SQL 里出现一个退格字符,
// 而真实 SQL 永远不会有。结果是 DWS 的「分区数超上限」检查自落地起从未生效过:它不报错、
// 不告警、每次都返回"没问题"。
//
// 这类缺陷没有任何外在表现 —— 一条永远通过的规则和一条正常的规则,在界面上长得一模一样。
// 所以这里不测"某条 SQL 会不会被判违规",而是直接钉住**正则本身能匹配真实语法**。
func TestPartitionNameReMatchesRealSQL(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{`PARTITION p_2026 VALUES LESS THAN ('2026-01-01')`, "p_2026"},
		{`partition   p1 values less than (100)`, "p1"},
		{`... , PARTITION p_max VALUES LESS THAN (MAXVALUE)`, "p_max"},
		{"PARTITION\tp_tab VALUES LESS THAN (1)", "p_tab"},
	}
	for _, c := range cases {
		m := partitionNameRe.FindStringSubmatch(c.sql)
		if m == nil {
			t.Fatalf("匹配不上真实分区语法:%q —— 这正是修复前的状态", c.sql)
		}
		if m[1] != c.want {
			t.Fatalf("分区名取错了:%q -> %q,期望 %q", c.sql, m[1], c.want)
		}
	}
}

// 多个分区要**各数一次**,否则"超过上限"这条规则算出来的数永远是 1。
func TestPartitionNameReCountsEveryPartition(t *testing.T) {
	sql := `CREATE TABLE t (id int) PARTITION BY RANGE (id) (
		PARTITION p1 VALUES LESS THAN (10),
		PARTITION p2 VALUES LESS THAN (20),
		PARTITION p3 VALUES LESS THAN (MAXVALUE))`
	got := partitionNameRe.FindAllStringSubmatch(sql, -1)
	// `PARTITION BY` 也会命中一次,取到的名字是 "BY" —— chkDWSPartitionCount 正是靠
	// 排除 "BY" 来数的,所以这里连同它一起钉住:4 次命中,其中 1 次是 BY。
	if len(got) != 4 {
		t.Fatalf("命中次数 %d,期望 4(三个分区 + 一次 PARTITION BY)", len(got))
	}
	n := 0
	for _, m := range got {
		if m[1] != "BY" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("数出 %d 个分区,期望 3", n)
	}
}
