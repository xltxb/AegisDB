package bootstrap

// Oracle 程序包下载(对象树上右键 → 下载)。
//
// 夹具里没有活的 Oracle,用的是无凭据实例的模拟源码;要钉住的是这个出口本身:
// 文件是不是 .pck 该有的样子、多选是不是一个 zip、引擎和权限对不上时拒不拒、
// 以及源码离开网关这件事有没有留下审计。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rawResp struct {
	status      int
	contentType string
	disposition string
	body        []byte
}

func (a *testApp) downloadPackages(token string, connID int64, body map[string]any) rawResp {
	a.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost,
		a.srv.URL+"/api/v1/connections/"+itoa(connID)+"/packages/download", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("download packages: %v", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return rawResp{res.StatusCode, res.Header.Get("Content-Type"), res.Header.Get("Content-Disposition"), data}
}

// envelopeOf 解出失败时的 JSON 信封;成功的文件响应不是 JSON,解不出来就返回 code=0。
func envelopeOf(r rawResp) apiResp {
	var e apiResp
	if strings.HasPrefix(r.contentType, "application/json") {
		_ = json.Unmarshal(r.body, &e)
	}
	return e
}

func TestPackageDownload_SingleIsRunnablePck(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	cid := app.newOracleConn(admin, "pkg-dl-single")

	r := app.downloadPackages(admin, cid, map[string]any{"scope": "APP", "names": []string{"PKG_BILLING"}})
	if e := envelopeOf(r); e.Code != 0 {
		t.Fatalf("download refused: code=%d msg=%s", e.Code, e.Msg)
	}
	if !strings.HasPrefix(r.contentType, "text/plain") {
		t.Errorf("a single package should come back as plain text, got %q", r.contentType)
	}
	if !strings.Contains(r.disposition, "attachment") || !strings.Contains(r.disposition, "PKG_BILLING.pck") {
		t.Errorf("file name should be <package>.pck, got %q", r.disposition)
	}
	src := string(r.body)
	if !strings.Contains(src, "CREATE OR REPLACE PACKAGE PKG_BILLING") {
		t.Errorf("file should carry the package source, got %q", src)
	}
	// 最后一个单元也要有单独一行的 "/",否则在 SQL*Plus 里 @ 这个文件,包体不会被执行。
	if !strings.HasSuffix(src, "\n/\n") {
		t.Errorf("a .pck must end with a lone '/' line, got tail %q", src[max(0, len(src)-20):])
	}
}

func TestPackageDownload_ManyIsOneZip(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	cid := app.newOracleConn(admin, "pkg-dl-many")

	// 重复的名字只算一次。
	r := app.downloadPackages(admin, cid, map[string]any{
		"scope": "APP", "names": []string{"PKG_A", "PKG_B", "PKG_A", " "},
	})
	if e := envelopeOf(r); e.Code != 0 {
		t.Fatalf("download refused: code=%d msg=%s", e.Code, e.Msg)
	}
	if r.contentType != "application/zip" {
		t.Fatalf("several packages should come back as one zip, got %q", r.contentType)
	}
	if !strings.Contains(r.disposition, "APP_packages_") || !strings.Contains(r.disposition, ".zip") {
		t.Errorf("zip should be named after the schema, got %q", r.disposition)
	}
	zr, err := zip.NewReader(bytes.NewReader(r.body), int64(len(r.body)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		pkg := strings.TrimSuffix(f.Name, ".pck")
		if !strings.Contains(string(b), "PACKAGE "+pkg) || !strings.HasSuffix(string(b), "\n/\n") {
			t.Errorf("%s does not look like that package's .pck: %q", f.Name, b)
		}
	}
	if strings.Join(names, ",") != "PKG_A.pck,PKG_B.pck" {
		t.Errorf("zip entries = %v, want PKG_A.pck, PKG_B.pck", names)
	}
}

func TestPackageDownload_IsAudited(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	cid := app.newOracleConn(admin, "pkg-dl-audit")

	r := app.downloadPackages(admin, cid, map[string]any{"scope": "APP", "names": []string{"PKG_SECRET_SAUCE"}})
	if e := envelopeOf(r); e.Code != 0 {
		t.Fatalf("download refused: code=%d msg=%s", e.Code, e.Msg)
	}
	items := app.auditItemsRaw(admin, "?instance=pkg-dl-audit")
	var rows []struct {
		Command string `json:"command"`
		Result  string `json:"result"`
	}
	_ = json.Unmarshal(items, &rows)
	for _, row := range rows {
		if strings.Contains(row.Command, `\pck`) && strings.Contains(row.Command, "APP.PKG_SECRET_SAUCE") {
			if row.Result != "exported" {
				t.Errorf("download audit should be result=exported, got %q", row.Result)
			}
			return
		}
	}
	t.Fatalf("package source left the gateway without an audit row; got %s", items)
}

func TestPackageDownload_Refusals(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	cid := app.newOracleConn(admin, "pkg-dl-refuse")

	cases := []struct {
		what   string
		connID int64
		body   map[string]any
		inMsg  string
	}{
		{"non-Oracle engine", app.mysqlConnID(admin), map[string]any{"scope": "app", "names": []string{"P"}}, "Oracle"},
		{"no package picked", cid, map[string]any{"scope": "APP", "names": []string{}}, "程序包"},
		{"no scope", cid, map[string]any{"scope": " ", "names": []string{"P"}}, "scope"},
		{"over the cap", cid, map[string]any{"scope": "APP", "names": manyNames(201)}, "200"},
	}
	for _, c := range cases {
		e := envelopeOf(app.downloadPackages(admin, c.connID, c.body))
		if e.Code == 0 {
			t.Errorf("%s: should be refused", c.what)
			continue
		}
		if !strings.Contains(e.Msg, c.inMsg) {
			t.Errorf("%s: refusal should mention %q, got %q", c.what, c.inMsg, e.Msg)
		}
	}
}

func TestPackageDownload_OutsiderForbidden(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	cid := app.newOracleConn(admin, "pkg-dl-outsider")
	outsider := app.login("zhaolei@aegisdb.io", "aegisdb123") // 研发只读,标签范围之外

	e := envelopeOf(app.downloadPackages(outsider, cid, map[string]any{"scope": "APP", "names": []string{"PKG_A"}}))
	eq(t, e.Code, 40300, "download on an instance outside the user's scope")
}

func manyNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("PKG_%03d", i)
	}
	return out
}
