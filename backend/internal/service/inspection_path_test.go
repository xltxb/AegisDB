package service

import (
	"path/filepath"
	"strings"
	"testing"
)

// 实例名变成目录名时写不出保存目录:路径分隔符、.. 都要变成无害的字符(.scratch/inspection-report 工单 02)。
func TestInspectionPathSegment_StaysInsideTheSaveDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "inspection")
	for _, name := range []string{"../../etc", "..", "a/b", `a\b`, "prod-c66-gi", "中文实例", "...", ""} {
		seg := inspectionPathSegment(name)
		if seg == "" || strings.ContainsAny(seg, `/\`) || seg == "." || seg == ".." {
			t.Errorf("%q → %q 不是安全的目录名", name, seg)
		}
		full := filepath.Join(base, seg, "2026", "2026-10-15.html.gz")
		if r, err := filepath.Rel(base, full); err != nil || strings.HasPrefix(r, "..") {
			t.Errorf("%q 拼出的路径跑出了保存目录: %s", name, full)
		}
	}
	if got := inspectionPathSegment("prod-c66-gi"); got != "prod-c66-gi" {
		t.Errorf("正常实例名应原样保留,实际 %q", got)
	}
}

// M10(2026-10 安全审计):实例名换成目录名时非 ASCII 字符都变成 `_`,同样字数的中文实例名会落到同一个
// 目录、同一天的报告互相覆盖。目录名带上连接 ID,不同实例一定不同目录;实例名留着方便人看。
func TestInspectionInstanceDir_DistinctPerConnection(t *testing.T) {
	a, b := inspectionInstanceDir(7, "订单"), inspectionInstanceDir(8, "用户")
	if a == b {
		t.Fatalf("不同实例的目录不能相同: %q == %q", a, b)
	}
	if got := inspectionInstanceDir(12, "prod-c66-gi"); got != "12-prod-c66-gi" {
		t.Errorf("目录名应为 <连接ID>-<实例名>,实际 %q", got)
	}
	if strings.ContainsAny(inspectionInstanceDir(3, "../../etc"), `/\`) {
		t.Error("仍要防路径穿越")
	}
}
