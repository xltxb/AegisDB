package handler

import (
	"os"
	"strings"
	"testing"
)

// openapiSection 取 openapi.yaml 里一个路径的那一段(到下一个顶层路径为止)。
func openapiSection(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatalf("读 openapi.yaml: %v", err)
	}
	doc := strings.ReplaceAll(string(b), "\r\n", "\n")
	start := strings.Index(doc, "\n  "+path+":\n")
	if start < 0 {
		t.Fatalf("openapi.yaml 里没有 %s", path)
	}
	rest := doc[start+1:]
	if end := strings.Index(rest[1:], "\n  /"); end >= 0 {
		rest = rest[:end+1]
	}
	return rest
}

// /audit/verify 与 /audit/approval/{apNo} 的权限描述要与路由一致:audit 菜单 + AuditReader
// (平台管理员或审计员)。文档从前写的是"监督角色 admin/owner/audit"与"可见范围同 /audit",
// 照着文档对接的人会以为 DBA 负责人也能用(2026-10 黑盒测试,工单 02 第 15 条)。
func TestOpenAPI_AuditReaderEndpointsDescribeTheRealGate(t *testing.T) {
	for _, path := range []string{"/audit/verify", "/audit/approval/{apNo}"} {
		sec := openapiSection(t, path)
		if !strings.Contains(sec, "平台管理员") || !strings.Contains(sec, "审计员") {
			t.Errorf("%s 的描述应当写明只给平台管理员与审计员:\n%s", path, sec)
		}
		if strings.Contains(sec, "admin/owner/audit") || strings.Contains(sec, "可见范围与 /audit 相同") {
			t.Errorf("%s 的描述还在说旧的门槛:\n%s", path, sec)
		}
	}
}
