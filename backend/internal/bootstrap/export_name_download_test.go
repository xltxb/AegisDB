package bootstrap

// 2026-10-29:导出的文件名必填(列表里靠它认出是哪一份);下载过的分卷服务端记下来,
// 列表标「已下载」,省得重复下载(迁移 0057)。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

func TestExport_NameIsRequired(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	for _, c := range []struct{ name, want string }{
		{"", "文件名"},
		{"   ", "文件名"},
		{strings.Repeat("订", 65), "太长"},
	} {
		r := app.do(http.MethodPost, "/api/v1/export", token, map[string]any{"connectionId": dev, "sql": "SELECT 1", "name": c.name})
		if r.Code != 40001 || !strings.Contains(r.Msg, c.want) {
			t.Errorf("文件名 %q:应以 40001 拒绝并提到 %q,实际 %d %s", c.name, c.want, r.Code, r.Msg)
		}
	}
	var n int64
	app.repo.DB().Model(&model.ExportJob{}).Count(&n)
	eq(t, n, int64(0), "没填文件名的导出不该建出任务")

	id := app.submitExport(token, dev, "SELECT 1", strings.Repeat("订", 64))
	app.waitJob(token, id)
	var job model.ExportJob
	app.repo.DB().First(&job, id)
	if job.Name != strings.Repeat("订", 64) {
		t.Errorf("64 个字的文件名应能提交并原样保存,实际 %q", job.Name)
	}
}

func TestExport_DownloadIsMarked(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	dev := app.connIDByEnv(token, "dev")
	eq(t, app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{"export.savePath": t.TempDir()}).Code, 0, "set path")

	id := app.submitExport(token, dev, "SELECT id, name FROM users", "用户清单")
	job := app.waitJob(token, id)
	eq(t, job.Status, "done", "job status")
	parts := job.fileParts()

	type listed struct {
		ID              int64   `json:"id"`
		Name            string  `json:"name"`
		DownloadedAt    *string `json:"downloadedAt"`
		DownloadedFiles string  `json:"downloadedFiles"`
	}
	find := func() listed {
		var all []listed
		_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/export/jobs", token, nil).Data, &all)
		for _, j := range all {
			if j.ID == id {
				return j
			}
		}
		t.Fatalf("列表里没有任务 %d", id)
		return listed{}
	}
	if j := find(); j.Name != "用户清单" || j.DownloadedAt != nil || j.DownloadedFiles != "" {
		t.Fatalf("没下载过:列表应带文件名、不带下载标记,实际 %+v", j)
	}

	app.raw(http.MethodGet, "/api/v1/export/download?file="+urlEscape(parts[0]), token)
	j := find()
	if j.DownloadedAt == nil || j.DownloadedFiles != parts[0] {
		t.Fatalf("下载之后应记下这个分卷和时间,实际 %+v", j)
	}
	// 再下一次:照样能下,不重复记
	if body := app.raw(http.MethodGet, "/api/v1/export/download?file="+urlEscape(parts[0]), token); len(body) == 0 {
		t.Error("再次下载应照样拿到文件")
	}
	if j := find(); j.DownloadedFiles != parts[0] {
		t.Errorf("同一个分卷下两次只记一次,实际 %q", j.DownloadedFiles)
	}

	// 别人的文件下不了,也不会在别人的任务上留下标记
	other := app.login("zhangwei@vela.io", "vela123")
	if r := app.do(http.MethodGet, "/api/v1/export/download?file="+urlEscape(parts[0]), other, nil); r.Code == 0 {
		t.Error("别人的导出文件不该能下载")
	}
}
