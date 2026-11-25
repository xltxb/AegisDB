package bootstrap

// 2026-10 黑盒测试(.scratch/blackbox-fixes/issues/04-data-tools.md 第 5~7 条):脚本上传的
// JSON 通道与 multipart 同一套类型 / 大小校验;/scripts/scan 校验实例访问范围;
// 响应不回显服务器绝对路径。

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"aegisdb/pkg/resp"
)

// uploadMultipart 走 multipart 通道上传一个文件。
func (a *testApp) uploadMultipart(token, filename string, body []byte) apiResp {
	a.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", filename)
	_, _ = fw.Write(body)
	_ = w.Close()
	req, _ := http.NewRequest(http.MethodPost, a.srv.URL+"/api/v1/scripts/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("multipart upload: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out apiResp
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestScriptUpload_JSONAndMultipartShareValidation(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	app.setSettings(token, map[string]any{"script.savePath": t.TempDir()})

	for _, name := range []string{"evil.exe", "shell.php", "noext", "a.sql.exe", ".bashrc"} {
		if r := app.do(http.MethodPost, "/api/v1/scripts/upload", token, map[string]any{"content": "SELECT 1;", "filename": name}); r.Code != resp.CodeBadRequest {
			t.Errorf("JSON 上传 %q 应当拒绝,实际 code=%d msg=%s", name, r.Code, r.Msg)
		}
		if r := app.uploadMultipart(token, name, []byte("SELECT 1;")); r.Code != resp.CodeBadRequest {
			t.Errorf("multipart 上传 %q 应当拒绝,实际 code=%d msg=%s", name, r.Code, r.Msg)
		}
	}
	big := strings.Repeat("-- x\n", (16<<20)/5)
	if r := app.do(http.MethodPost, "/api/v1/scripts/upload", token, map[string]any{"content": big, "filename": "big.sql"}); r.Code != resp.CodeBadRequest {
		t.Errorf("JSON 上传 16MB 应当拒绝,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	if r := app.uploadMultipart(token, "big.sql", []byte(big)); r.Code != resp.CodeBadRequest {
		t.Errorf("multipart 上传 16MB 应当拒绝,实际 code=%d msg=%s", r.Code, r.Msg)
	}
	// 脚本类扩展名照常接受:.sql、.txt、Oracle 程序包 .pck / .pkb / .pks 等
	for _, name := range []string{"job.sql", "notes.TXT", "PK_AGENT.pck", "body.pkb", "spec.pks", "p.prc"} {
		if r := app.do(http.MethodPost, "/api/v1/scripts/upload", token, map[string]any{"content": "SELECT 1;", "filename": name}); r.Code != 0 {
			t.Errorf("JSON 上传 %q 应当接受,实际 code=%d msg=%s", name, r.Code, r.Msg)
		}
		if r := app.uploadMultipart(token, name, []byte("SELECT 1;")); r.Code != 0 {
			t.Errorf("multipart 上传 %q 应当接受,实际 code=%d msg=%s", name, r.Code, r.Msg)
		}
	}
	// JSON 不带文件名:默认 script.sql
	if r := app.do(http.MethodPost, "/api/v1/scripts/upload", token, map[string]any{"content": "SELECT 1;"}); r.Code != 0 {
		t.Errorf("JSON 上传不带文件名应按 script.sql 接受,实际 code=%d msg=%s", r.Code, r.Msg)
	}
}

// /scripts/scan 与 execute、terminal 同口径校验实例访问范围。
func TestScriptScan_ChecksInstanceScope(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	me := app.userByEmail(admin, "linwei@aegisdb.io")
	orders := app.connIDByName(admin, "order-cluster")
	app.setSettings(admin, map[string]any{"script.savePath": t.TempDir()})

	eq(t, app.do(http.MethodPost, "/api/v1/scripts/scan", admin, map[string]any{
		"connectionId": orders, "content": "SELECT 1;", "filename": "a.sql"}).Code, 0, "范围内可以扫描")

	eq(t, app.setUserTags(admin, me.ID, []string{"analytics"}).Code, 0, "scope to analytics")
	r := app.do(http.MethodPost, "/api/v1/scripts/scan", admin, map[string]any{
		"connectionId": orders, "content": "SELECT 1;", "filename": "a.sql"})
	eq(t, r.Code, resp.CodeForbidden, "范围外的实例不能扫描")
	r = app.do(http.MethodPost, "/api/v1/scripts/execute", admin, map[string]any{
		"connectionId": orders, "content": "SELECT 1;", "filename": "a.sql"})
	eq(t, r.Code, resp.CodeForbidden, "范围外的实例不能执行")
}

// 响应只回文件名,不回服务器绝对路径。
func TestScript_ResponsesDoNotEchoServerPath(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@aegisdb.io", "aegisdb123")
	dir := t.TempDir()
	app.setSettings(token, map[string]any{"script.savePath": dir})
	dev := app.connIDByEnv(token, "dev")
	prod := app.connIDByEnv(token, "prod")

	noPath := func(what string, data []byte) {
		t.Helper()
		s := string(data)
		if strings.Contains(s, dir) || strings.Contains(s, strings.ReplaceAll(dir, `\`, `\\`)) || strings.Contains(s, "Lin_Wei") {
			t.Errorf("%s 回显了服务器路径: %s", what, s)
		}
	}
	up := app.do(http.MethodPost, "/api/v1/scripts/upload", token, map[string]any{"content": "SELECT 1;", "filename": "job.sql"})
	eq(t, up.Code, 0, "upload")
	noPath("/scripts/upload", up.Data)
	var rec struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(up.Data, &rec)
	if !strings.HasSuffix(rec.Path, "_job.sql") || strings.ContainsAny(rec.Path, `/\`) {
		t.Errorf("upload 的 path 应只是文件名,实际 %q", rec.Path)
	}
	noPath("/scripts/uploads", app.do(http.MethodGet, "/api/v1/scripts/uploads", token, nil).Data)

	run := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"content": "SELECT 1;", "filename": "mig.sql", "connectionId": dev})
	eq(t, run.Code, 0, "execute")
	noPath("/scripts/execute", run.Data)
	var out struct {
		SavedFile string `json:"savedFile"`
	}
	_ = json.Unmarshal(run.Data, &out)
	if !strings.HasSuffix(out.SavedFile, "_mig.sql") || strings.ContainsAny(out.SavedFile, `/\`) {
		t.Errorf("execute 的 savedFile 应只是文件名,实际 %q", out.SavedFile)
	}

	// 送审分支同样只回文件名
	ap := app.do(http.MethodPost, "/api/v1/scripts/execute", token, map[string]any{
		"content": "DROP TABLE orders;", "filename": "drop.sql", "connectionId": prod})
	eq(t, ap.Code, resp.CodeIntercepted, "高危脚本送审")
	noPath("/scripts/execute(送审)", ap.Data)
}
