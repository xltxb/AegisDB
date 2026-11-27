package service

import (
	"testing"

	"aegisdb/internal/model"
)

// 每种工单类型在 approvalFlows 里都有一行,且"能不能手动执行"与模型层的说法一致(#182)。
// 新增一种类型而忘了补这张表,这条用例会先红。
func TestApprovalFlows_CoverEveryType(t *testing.T) {
	for _, typ := range model.ApprovalTypes() {
		f, ok := approvalFlows[typ]
		if !ok {
			t.Errorf("类型 %q 在 approvalFlows 里没有一行", typ)
			continue
		}
		if f.output == "" || f.outputRef == "" || f.title == nil || f.body == nil {
			t.Errorf("类型 %q 的批准结果或通知没写全", typ)
		}
		if manual := model.ApprovalTypeManualExec(typ); manual != (f.noManualExec == nil) {
			t.Errorf("类型 %q:模型说要人执行=%v,approvalFlows 却说能手动执行=%v", typ, manual, f.noManualExec == nil)
		}
		if _, ok := model.ApprovalTypeWhere(typ); !ok {
			t.Errorf("类型 %q 没有筛选条件", typ)
		}
	}
	if len(approvalFlows) != len(model.ApprovalTypes()) {
		t.Errorf("approvalFlows 里有模型不认识的类型")
	}
}
