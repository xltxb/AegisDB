package review

import (
	"strings"
	"testing"
)

// 「对象未带 schema 限定」这条规则抓表名用的正则原先只认 ASCII:中文表名整个抓不到,
// 规则就静静地跳过它 —— 一张叫「用户表」的表不带 schema 也不会被指出来。
// 与 gateway 包 sensitiveTableRefRe 是同一类问题(标识符是什么文字,不该决定规则管不管用)。
func TestSchemaQualified_NonASCIITableNames(t *testing.T) {
	mk := func(sql string) *stmt { return &stmt{sql: sql, masked: sql, upper: strings.ToUpper(sql)} }

	// 中文表名、不带 schema:必须报
	if out := chkSchemaQualified(mk("SELECT id FROM 用户表"), params{}, ""); len(out) != 1 || !strings.Contains(out[0].S, "用户表") {
		t.Fatalf("中文表名未带 schema 应被指出,实际 %v", out)
	}
	// 带了 schema:不报
	if out := chkSchemaQualified(mk("SELECT id FROM 业务库.用户表"), params{}, ""); len(out) != 0 {
		t.Fatalf("带 schema 的中文表不该报,实际 %v", out)
	}
	// ASCII 行为不变
	if out := chkSchemaQualified(mk("SELECT id FROM t_user"), params{}, ""); len(out) != 1 {
		t.Fatalf("ASCII 表名未带 schema 应被指出,实际 %v", out)
	}
	if out := chkSchemaQualified(mk("SELECT id FROM dwd.t_user"), params{}, ""); len(out) != 0 {
		t.Fatalf("带 schema 的 ASCII 表不该报,实际 %v", out)
	}
}
