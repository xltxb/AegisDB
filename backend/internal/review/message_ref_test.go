package review

import (
	"testing"

	"aegisdb/internal/apimsg"
)

// 第三批(.scratch/api-msg-i18n):审查发现带 MessageRef,界面按读者语言显示;Message 仍是原来的中文。
func TestFindingCarriesMessageRef(t *testing.T) {
	rules := []Rule{
		{Code: "dml.require.where", Name: "DML 必须带 WHERE", Level: LevelError, Kind: "builtin", Enabled: true},
		{Code: "x.custom", Name: "自定义", Level: LevelWarn, Kind: "builtin", Enabled: true},
	}
	res := Check(DialectMySQL, "DELETE FROM t_order", rules[:1])
	if len(res.Findings) != 1 {
		t.Fatalf("应有一条发现,实际 %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Message != "DELETE 未带 WHERE 条件,将作用于全表" {
		t.Errorf("中文消息变了:%q", f.Message)
	}
	if f.MessageRef == nil || f.MessageRef.Code != apimsg.Code("%s 未带 WHERE 条件,将作用于全表") || f.MessageRef.Args["0"] != "DELETE" {
		t.Errorf("MessageRef 不对:%+v", f.MessageRef)
	}

	// 运维自定义的规则消息:原样嵌在句首,检查器的细节仍可翻译
	custom := rules[0]
	custom.Message = "删数据必须带条件"
	f = Check(DialectMySQL, "DELETE FROM t_order", []Rule{custom}).Findings[0]
	if f.Message != "删数据必须带条件 — DELETE 未带 WHERE 条件,将作用于全表" {
		t.Errorf("自定义消息拼接变了:%q", f.Message)
	}
	if f.MessageRef == nil || f.MessageRef.Code != apimsg.Code("%s — %s") || len(f.MessageRef.Parts) != 1 {
		t.Errorf("自定义消息的 ref 不对:%+v", f.MessageRef)
	}
}
