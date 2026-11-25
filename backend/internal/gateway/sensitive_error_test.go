package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// H5(2026-10 安全审计):打码只管结果集,报错原文照样回显。语句里对敏感列做类型转换,
// 转换失败的报错会把那一格的原值带出来(PG:invalid input syntax for type integer: "…")。
// 语句提到了生效的敏感列时,报错里引号括起的片段一律抹掉。

func TestMaskErrorText_ScrubsQuotedValuesWhenSensitiveColumnMentioned(t *testing.T) {
	rules := []SensitiveRule{{Table: "t_user", Column: "id_card"}}
	raw := errors.New(`pq: invalid input syntax for type integer: "110101199001011234"`)

	got := maskErrorText("SELECT CAST(id_card AS int) FROM t_user", raw, rules)
	if strings.Contains(got.Error(), "110101199001011234") {
		t.Fatalf("报错里不应再有原值:%s", got)
	}
	if !strings.Contains(got.Error(), "invalid input syntax for type integer") {
		t.Errorf("报错的说明部分应保留:%s", got)
	}
	if !errors.Is(got, raw) {
		t.Error("应保留错误链(errors.Is 仍认得原错误)")
	}

	// 没提到敏感列:报错原样
	if got := maskErrorText("SELECT CAST(name AS int) FROM t_user", raw, rules); got != raw {
		t.Errorf("没提到敏感列时报错应原样返回,实际 %v", got)
	}
	// 规则按表限定,别的表上同名列不触发
	if got := maskErrorText("SELECT CAST(id_card AS int) FROM t_other", raw, rules); got != raw {
		t.Errorf("规则限定的表没被查,报错应原样返回,实际 %v", got)
	}
	// 超时 / 取消的判定照常
	wrapped := fmt.Errorf("query: %w", context.DeadlineExceeded)
	if got := maskErrorText("SELECT id_card FROM t_user", wrapped, rules); !errors.Is(got, context.DeadlineExceeded) {
		t.Error("超时错误经过之后 errors.Is 仍应认得")
	}
	if maskErrorText("SELECT id_card FROM t_user", nil, rules) != nil {
		t.Error("nil 进 nil 出")
	}
}

// 2026-10-26 讨论 01:只对读语句脱敏。写语句、DDL、建包的编译错误不抹 —— 从前表名、列名、对象名一起
// 变成 '***',排错看不出是哪个对象。读语句(含 WITH)照旧抹。
func TestMaskErrorText_OnlyForReads(t *testing.T) {
	rules := []SensitiveRule{{Table: "*", Column: "phone"}}
	for _, c := range []struct{ q, msg string }{
		{"ALTER TABLE t_user ADD INDEX idx_phone(phone)", `Error 1061: Duplicate key name 'idx_phone'`},
		{"UPDATE t_user SET nme = 1 WHERE phone = '1'", `Error 1054: Unknown column 'nme' in 'field list'`},
		{"CREATE OR REPLACE PACKAGE BODY pk IS PROCEDURE p IS BEGIN SELECT phone INTO v FROM t; pk_util.f; END; END;", `PLS-00201: identifier 'PK_UTIL.F' must be declared`},
	} {
		raw := errors.New(c.msg)
		if got := maskErrorText(c.q, raw, rules); got != raw {
			t.Errorf("非读语句的报错应原样返回: %q → %v", c.q, got)
		}
	}
	raw := errors.New(`Truncated incorrect DOUBLE value: '13800001111'`)
	for _, q := range []string{
		"SELECT phone + 0 FROM t_user",
		"WITH x AS (SELECT phone FROM t_user) SELECT phone + 0 FROM x",
	} {
		if got := maskErrorText(q, raw, rules); strings.Contains(got.Error(), "13800001111") {
			t.Errorf("读语句的报错仍应抹掉原值: %q → %v", q, got)
		}
	}
}
