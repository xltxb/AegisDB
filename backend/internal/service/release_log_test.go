package service

import (
	"strings"
	"testing"

	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
)

// log_refs 带着语句回显与驱动报错,和 log 一样要在出口打码(releaseView)—— 只给 log 打码,
// 口令就从 refs 那一份漏出去。
func TestRedactRefsMasksSecretsInEveryArg(t *testing.T) {
	var l relLog
	l.add(model.RelLogStmtFail, "pos", "1", "total", "1", "ms", "3", "out", "ERROR near IDENTIFIED BY 'S3cret!'")
	l.raw("    alter user bob identified by 'S3cret!';")
	l.refs[0].Parts = []model.RuleRef{*model.NewRuleRef(model.OutExecFailed, "err", "IDENTIFIED BY 'S3cret!'")}
	got := redactRefs(refsJSON(l.refs), true)
	if got == "" || strings.Contains(got, "S3cret!") {
		t.Fatalf("secret leaked through log refs: %s", got)
	}
	one := redactRefs(refJSON(model.NewRuleRef(model.RelErrPanic, "err", "identified by 'S3cret!'")), false)
	if one == "" || strings.Contains(one, "S3cret!") {
		t.Fatalf("secret leaked through error ref: %s", one)
	}
}

// 执行器没带 OutputRef 的几句,由 execOutRef 认出 code;渲染回来必须与原串一字不差。
func TestExecOutRefRoundTrips(t *testing.T) {
	for _, out := range []string{"执行成功", "+ 3 rows", "+ 1,000 rows", "+ 1,000+ rows (显示前 1000)"} {
		ns, ref := execOutRef(gateway.ExecResult{Output: out})
		if ref == nil || ns != "rlLog" {
			t.Fatalf("%q: no ref", out)
		}
		if got := model.RenderRelLog(ref); got != out {
			t.Errorf("%q renders back as %q", out, got)
		}
	}
	if _, ref := execOutRef(gateway.ExecResult{Output: "something else"}); ref != nil {
		t.Errorf("unknown output should carry no ref, got %+v", ref)
	}
}

// 规范串超过列宽会被截,半截的 refs 与截过的原串对不上 —— 那时不存 refs,界面回落原串。
func TestRelLogFieldsDropRefsWhenTextIsClipped(t *testing.T) {
	var l relLog
	l.raw(strings.Repeat("x", releaseLogMax+10))
	f := l.fields()
	if f["log_refs"] != "" {
		t.Errorf("clipped log must not carry refs")
	}
	var short relLog
	short.add(model.RelLogNotified)
	if short.fields()["log_refs"] == "" {
		t.Errorf("a normal log carries refs")
	}
}
