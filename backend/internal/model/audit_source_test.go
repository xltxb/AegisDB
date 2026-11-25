package model

import "testing"

// 路由 → 来源的映射。路由模板是服务端定的(gin FullPath),这里逐个入口钉住口径。
func TestAuditSourceForRoute(t *testing.T) {
	cases := map[string]string{
		"/api/v1/bizquery/exec":                          AuditSourceQuery,
		"/api/v1/bizquery/export":                        AuditSourceExport, // 查询页导出归导出
		"/api/v1/export":                                 AuditSourceExport,
		"/api/v1/export/jobs/:id/cancel":                 AuditSourceExport,
		"/api/v1/terminal/exec":                          AuditSourceTerminal,
		"/api/v1/terminal/ws":                            AuditSourceTerminal,
		"/api/v1/terminal/exec-async":                    AuditSourceTerminal,
		"/api/v1/terminal/transcript-export":             AuditSourceTerminal,
		"/api/v1/connections/:id/objects/compile":        AuditSourceTerminal,
		"/api/v1/connections/:id/objects/recompile":      AuditSourceTerminal,
		"/api/v1/connections/:id/packages/download":      AuditSourceTerminal,
		"/api/v1/osc/jobs/:id/:action":                   AuditSourceTerminal,
		"/api/v1/scripts/execute":                        AuditSourceScript,
		"/api/v1/scripts/scan":                           AuditSourceScript,
		"/api/v1/approvals/:id/execute":                  AuditSourceApproval,
		"/api/v1/releases/:id/stages/:stageId/confirm":   AuditSourceRelease,
		"/api/v1/pipelines":                              AuditSourceRelease,
		"/api/v1/open/releases":                          AuditSourceAPI,
		"/api/v1/open/sql-review":                        AuditSourceAPI,
		"/api/v1/auth/password":                          AuditSourceAuth,
		"/api/v1/auth/logout":                            AuditSourceAuth,
		"/api/v1/users/:id":                              AuditSourceAdmin,
		"/api/v1/roles/:id/members":                      AuditSourceAdmin,
		"/api/v1/connections/:id":                        AuditSourceAdmin,
		"/api/v1/exporter":                               AuditSourceAdmin, // 前缀要按段匹配,不是字符串前缀
		"":                                               "",               // 没匹配上路由
	}
	for path, want := range cases {
		if got := AuditSourceForRoute(path); got != want {
			t.Errorf("AuditSourceForRoute(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestIsAuditSourceFilter(t *testing.T) {
	for _, s := range append(append([]string{}, AuditSources...), AuditSourceUnknown) {
		if !IsAuditSourceFilter(s) {
			t.Errorf("%q 应当是合法的来源筛选值", s)
		}
	}
	for _, s := range []string{"", "all", "Terminal", "bizquery", "x"} {
		if IsAuditSourceFilter(s) {
			t.Errorf("%q 不该被认作来源", s)
		}
	}
}
