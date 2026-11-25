package bootstrap

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AI 巡检报告 · 工单 03(.scratch/inspection-report):查看接口与权限。

type inspectionView struct {
	ID            int64  `json:"id"`
	ConnectionID  int64  `json:"connectionId"`
	InstanceName  string `json:"instanceName"`
	ReportDate    string `json:"reportDate"`
	Level         string `json:"level"`
	TopFinding    string `json:"topFinding"`
	CriticalCount int    `json:"criticalCount"`
	Findings      []struct {
		Severity string `json:"severity"`
		Title    string `json:"title"`
	} `json:"findings"`
}

// rawWithHeaders 取接口的原始响应(取 HTML 的接口返回纯文本,不是 JSON 信封)。
func (a *testApp) rawWithHeaders(path, token string) (int, http.Header, string) {
	a.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, a.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(b)
}

func TestInspectionView_DBARolesSeeEverythingOthersDoNot(t *testing.T) {
	app, admin, client, dir := inspectionSetup(t)
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	html := "<!doctype html><h1>巡检</h1><script>alert(1)</script>"
	eq(t, app.openDo(http.MethodPost, inspectionPath, client, inspectionBody("order-cluster", yesterday, "ok", "<html>昨天</html>")).Code, 0, "上传昨天")
	eq(t, app.openDo(http.MethodPost, inspectionPath, client, inspectionBody("order-cluster", today, "critical", html)).Code, 0, "上传今天")

	// 三类 DBA 角色都能看
	owner := app.login("zhangwei@aegisdb.io", "aegisdb123")
	l2 := app.login("chenhao@aegisdb.io", "aegisdb123")
	for name, tok := range map[string]string{"平台管理员": admin, "DBA 负责人": owner, "DBA L2": l2} {
		eq(t, app.do(http.MethodGet, "/api/v1/inspection-reports/latest", tok, nil).Code, 0, name+" 看最新列表")
	}
	// 研发只读、审计员看不到;给研发只读勾上菜单也还是看不到(角色校验在)
	ro := app.login("zhaolei@aegisdb.io", "aegisdb123")
	auditor := app.login("hujun@aegisdb.io", "aegisdb123")
	eq(t, app.do(http.MethodGet, "/api/v1/inspection-reports/latest", ro, nil).Code, 40300, "研发只读")
	eq(t, app.do(http.MethodGet, "/api/v1/inspection-reports/latest", auditor, nil).Code, 40300, "审计员")
	if err := app.repo.DB().Exec("UPDATE tbl_role_menu SET enabled = ? WHERE role_id = ? AND menu_key = ?", true, roleIDByCode(t, app, "ro"), MenuInspection).Error; err != nil {
		t.Fatal(err)
	}
	r := app.do(http.MethodGet, "/api/v1/inspection-reports/latest", ro, nil)
	eq(t, r.Code, 40300, "勾上菜单的研发只读仍应被角色校验拦下")

	// 最新列表:每个实例一行,没有报告的也在
	r = app.do(http.MethodGet, "/api/v1/inspection-reports/latest", l2, nil)
	var latest struct {
		Today string `json:"today"`
		Items []struct {
			ConnectionID int64           `json:"connectionId"`
			Instance     string          `json:"instance"`
			Report       *inspectionView `json:"report"`
		} `json:"items"`
	}
	_ = json.Unmarshal(r.Data, &latest)
	conns := app.do(http.MethodGet, "/api/v1/connections", admin, nil)
	var all []struct{ ID int64 }
	_ = json.Unmarshal(conns.Data, &all)
	eq(t, len(latest.Items), len(all), "每个实例一行")
	eq(t, latest.Today, today, "带上服务器的今天")
	var orderID int64
	withReport := 0
	for _, it := range latest.Items {
		if it.Report != nil {
			withReport++
			eq(t, it.Instance, "order-cluster", "只有它有报告")
			eq(t, it.Report.ReportDate, today, "取最新一份")
			eq(t, it.Report.Level, "critical", "最新一份的结论")
			eq(t, it.Report.TopFinding, "t_order 分区将用尽", "带上最严重的一条问题")
			orderID = it.ConnectionID
		}
	}
	eq(t, withReport, 1, "没有报告的实例 report 为空")

	// 历史:按日期倒序
	r = app.do(http.MethodGet, fmt.Sprintf("/api/v1/inspection-reports?instanceId=%d", orderID), owner, nil)
	var hist struct {
		Items []inspectionView `json:"items"`
		Total int              `json:"total"`
	}
	_ = json.Unmarshal(r.Data, &hist)
	if hist.Total != 2 || hist.Items[0].ReportDate != today || hist.Items[1].ReportDate != yesterday {
		t.Fatalf("历史应按日期倒序两份: %s", r.Data)
	}

	// 详情带问题列表
	r = app.do(http.MethodGet, fmt.Sprintf("/api/v1/inspection-reports/%d", hist.Items[0].ID), owner, nil)
	var detail inspectionView
	_ = json.Unmarshal(r.Data, &detail)
	if len(detail.Findings) != 1 || detail.Findings[0].Severity != "critical" {
		t.Errorf("详情应带问题列表: %s", r.Data)
	}

	// 取 HTML:纯文本、附件、nosniff;记查看审计
	status, hdr, body := app.rawWithHeaders(fmt.Sprintf("/api/v1/inspection-reports/%d/html", hist.Items[0].ID), owner)
	eq(t, status, 200, "取 HTML")
	if body != html {
		t.Errorf("应原样返回 HTML,实际 %q", body)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type 应是 text/plain,实际 %q", ct)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Errorf("应以附件方式返回,实际 %q", cd)
	}
	eq(t, hdr.Get("X-Content-Type-Options"), "nosniff", "nosniff")
	if audit := string(app.auditItemsRaw(admin, "?range=24h&full=1")); !strings.Contains(audit, "inspection.view 查看巡检报告") {
		t.Errorf("查看报告应记审计: %s", audit)
	}
	// 审计员看不到详情
	if r := app.do(http.MethodGet, fmt.Sprintf("/api/v1/inspection-reports/%d", hist.Items[0].ID), auditor, nil); r.Code != 40300 {
		t.Errorf("审计员不能看详情,实际 %d", r.Code)
	}

	// 文件被手工删了:明确的错误,不是 500
	if err := os.Remove(filepath.Join(dir, inspectionDirOf(app, "order-cluster"), today[:4], today+".html.gz")); err != nil {
		t.Fatal(err)
	}
	if r := app.do(http.MethodGet, fmt.Sprintf("/api/v1/inspection-reports/%d/html", hist.Items[0].ID), owner, nil); r.Code != 40400 || !strings.Contains(r.Msg, "报告文件不存在") {
		t.Errorf("文件丢失应返回 40400 并说明,实际 %d %q", r.Code, r.Msg)
	}
}
