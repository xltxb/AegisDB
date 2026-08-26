package bootstrap

// 开放接口超大脚本的报错一致性(审查修复 #6)。
//
// Both open endpoints accept a multipart script and share one 15MB bound. The
// create endpoint refuses an oversized file BY NAME; the review endpoint used
// to silently drop it and then complain "请提供 sql 或 script 内容" — an error
// pointing at a field the caller did fill in. Same bound, same refusal.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// postOpenMultipart sends a multipart body with one oversized file to an open
// endpoint and returns the envelope.
func postOpenMultipart(t *testing.T, app *testApp, token, path string, fields map[string]string, fileSize int) apiResp {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", "huge.sql")
	_, _ = fw.Write(bytes.Repeat([]byte("-- pad\n"), fileSize/7+1))
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, app.srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("multipart post: %v", err)
	}
	defer res.Body.Close()
	var env apiResp
	_ = json.NewDecoder(res.Body).Decode(&env)
	return env
}

func TestOpenReviewOversizedFileIsNamedNotSwallowed(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	token := app.issueClient(admin, "CI审查", "zhangwei@vela.io", []string{"review:check"})

	env := postOpenMultipart(t, app, token, "/api/v1/open/sql-review",
		map[string]string{"dialect": "mysql"}, 15<<20+1024)
	if env.Code == 0 {
		t.Fatal("an oversized script must be refused")
	}
	// The refusal names the actual problem — the size — not a field the caller
	// filled in. "请提供 sql 或 script" here means the file was silently dropped.
	if !strings.Contains(env.Msg, "15MB") {
		t.Errorf("refusal should name the 15MB bound, got %q", env.Msg)
	}
	if strings.Contains(env.Msg, "请提供") {
		t.Errorf("refusal must not claim the script is missing, got %q", env.Msg)
	}
}

// TestOpenReleaseMultipartHonorsChangeType — multipart 表单里的 changeType 声明
// 必须与 JSON 通道同效。曾经的缺口:handler 的 multipart 分支漏读该字段,声明
// 被静默忽略 —— 一个"看起来被约束了"的约束比没有更糟。
func TestOpenReleaseMultipartHonorsChangeType(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	mpPid := app.createPipeline(admin, "MP类型流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	token := app.issueClientPiped(admin, "MP类型平台", "zhangwei@vela.io", nil, mpPid)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", "MP类型校验")
	_ = mw.WriteField("instance", "sandbox-dev")
	_ = mw.WriteField("changeType", "dml")
	_ = mw.WriteField("sql", "DROP TABLE tbl_order;")
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, app.srv.URL+"/api/v1/open/releases", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("multipart post: %v", err)
	}
	defer res.Body.Close()
	var env apiResp
	_ = json.NewDecoder(res.Body).Decode(&env)
	if env.Code == 0 {
		t.Fatal("a DDL body under a declared dml type must be refused via multipart too")
	}
	if !strings.Contains(env.Msg, "DROP") {
		t.Errorf("refusal should name the verb, got %q", env.Msg)
	}
}
