package bootstrap

// 业务方数据查询 · 第四期:导出开关、授权总表、全局设置。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

func (b *bizEnv) export(query string) (status int, ctype string, body []byte) {
	buf, _ := json.Marshal(map[string]any{"sourceId": b.source, "database": b.db, "sql": query})
	req, _ := http.NewRequest(http.MethodPost, b.app.srv.URL+"/api/v1/bizquery/export", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.user)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		b.app.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ = io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Content-Type"), body
}

func envelopeMsg(body []byte) (int, string) {
	var e apiResp
	_ = json.Unmarshal(body, &e)
	return e.Code, e.Msg
}

// 错误码(后端开发文档第 10 章):40300 只表示"你不能这么做"。并发已满是限流(42900);
// 导出时查询本身失败(库报错、超时)是"做了但没做成",与执行接口一样回 0,原因在 data.error。
func TestBizQuery_ErrorCodes(t *testing.T) {
	b := newBizEnv(t, 5)
	eq(t, b.app.do(http.MethodPut, "/api/v1/settings", b.admin, map[string]any{"bizQuery.exportEnabled": true, "bizQuery.maxConcurrentPerUser": 1}).Code, 0, "开导出、每人并发 1")
	g := b.body("biz@corp.io", "OA-ERR")
	g["maxRows"], g["allowExport"] = 5, true
	grantOf(t, b.put(g))

	type failure struct {
		Error    string `json:"error"`
		ErrorRef struct {
			Code string            `json:"code"`
			Args map[string]string `json:"args"`
		} `json:"errorRef"`
	}
	_, ct, body := b.export("SELECT * FROM no_such_table")
	var env struct {
		Code int     `json:"code"`
		Data failure `json:"data"`
	}
	_ = json.Unmarshal(body, &env)
	if !strings.HasPrefix(ct, "application/json") || env.Code != 0 || !strings.Contains(env.Data.Error, "no_such_table") {
		t.Errorf("导出时查询失败应回 0 + data.error,实际 %s %s", ct, body)
	}
	// 失败原因同样带 code(按读者语言渲染);驱动原文放在参数里,不翻译
	if env.Data.ErrorRef.Code != model.BizExecFailed || !strings.Contains(env.Data.ErrorRef.Args["err"], "no_such_table") {
		t.Errorf("导出失败应带 errorRef bizExecFailed(err=驱动原文),实际 %+v", env.Data.ErrorRef)
	}
	r0, _ := b.exec("SELECT * FROM no_such_table")
	var ex failure
	_ = json.Unmarshal(r0.Data, &ex)
	if r0.Code != 0 || ex.ErrorRef.Code != model.BizExecFailed || !strings.Contains(ex.Error, "no_such_table") {
		t.Errorf("执行失败应回 0 + error + errorRef bizExecFailed,实际 %d %+v", r0.Code, ex)
	}

	// 占住唯一的并发名额:一条要跑好几秒的查询
	slow := make(chan struct{})
	go func() {
		defer close(slow)
		b.exec("WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 5000000) SELECT COUNT(*) FROM c")
	}()
	time.Sleep(300 * time.Millisecond)
	r, _ := b.exec("SELECT 1")
	eq(t, r.Code, 42900, "并发已满是限流,不是无权限")
	<-slow
}

// 拒绝时 data.reasonRef 带机器可读的原因:前端按读者语言渲染,并按 code(不是中文原话)决定
// 要不要刷新授权列表。msg 仍是规范中文串。
func TestBizQuery_DenialCarriesReasonRef(t *testing.T) {
	b := newBizEnv(t, 5)
	ref := func(r apiResp) (code string, args map[string]string) {
		var d struct {
			ReasonRef struct {
				Code string            `json:"code"`
				Args map[string]string `json:"args"`
			} `json:"reasonRef"`
		}
		_ = json.Unmarshal(r.Data, &d)
		return d.ReasonRef.Code, d.ReasonRef.Args
	}
	r, _ := b.exec("DELETE FROM orders")
	if code, args := ref(r); r.Code != 40300 || code != model.BizNotSelect || args["verb"] != "DELETE" || !strings.Contains(r.Msg, "DELETE") {
		t.Errorf("写语句:应 40300 + reasonRef bizNotSelect(verb=DELETE) + 中文 msg,实际 %d %q %s %v", r.Code, r.Msg, code, args)
	}
	eq(t, b.app.do(http.MethodDelete, "/api/v1/open/query-grants?externalId=OA-100", b.token, nil).Code, 0, "撤销授权")
	r, _ = b.exec("SELECT 1")
	if code, args := ref(r); r.Code != 40300 || code != model.BizNoGrant || args["db"] != b.db {
		t.Errorf("授权撤销后:应 40300 + reasonRef bizNoGrant(db=%s),实际 %d %q %s %v", b.db, r.Code, r.Msg, code, args)
	}
}

// PRD 5.7:每人每天查询次数上限(默认 0 = 不限)。数的是当天真正执行了的查询与导出(按审计,
// 网关重启、多实例都不丢);被判定拒绝的不算。超了是限流(42900),次日自动恢复。
func TestBizQuery_DailyLimitPerUser(t *testing.T) {
	b := newBizEnv(t, 5)
	eq(t, b.app.do(http.MethodPut, "/api/v1/settings", b.admin, map[string]any{"bizQuery.maxDailyPerUser": -1}).Code, 40001, "负数不合法")
	eq(t, b.app.do(http.MethodPut, "/api/v1/settings", b.admin, map[string]any{"bizQuery.maxDailyPerUser": 2}).Code, 0, "每人每天 2 次")

	for i := 0; i < 2; i++ {
		r, _ := b.exec("SELECT 1")
		eq(t, r.Code, 0, "上限以内照常执行")
	}
	r, _ := b.exec("DELETE FROM orders")
	eq(t, r.Code, 40300, "被判定拒绝的查询")
	r, _ = b.exec("SELECT 1")
	var d struct {
		ReasonRef struct {
			Code string            `json:"code"`
			Args map[string]string `json:"args"`
		} `json:"reasonRef"`
	}
	_ = json.Unmarshal(r.Data, &d)
	if r.Code != 42900 || d.ReasonRef.Code != model.BizDailyLimit || d.ReasonRef.Args["n"] != "2" {
		t.Errorf("第 3 次(拒绝的那次不算)应 42900 + bizDailyLimit(n=2),实际 %d %q %+v", r.Code, r.Msg, d.ReasonRef)
	}

	eq(t, b.app.do(http.MethodPut, "/api/v1/settings", b.admin, map[string]any{"bizQuery.maxDailyPerUser": 0}).Code, 0, "0 = 不限")
	r, _ = b.exec("SELECT 1")
	eq(t, r.Code, 0, "改回不限后立即可查")
}

func TestBizExport_TwoSwitchesAndMaskedCSV(t *testing.T) {
	b := newBizEnv(t, 5)

	// 全局开关默认关
	_, ct, body := b.export("SELECT id FROM orders")
	if code, msg := envelopeMsg(body); !strings.HasPrefix(ct, "application/json") || code != 40300 || !strings.Contains(msg, "未开启") {
		t.Fatalf("全局开关关着时应拒绝,实际 %s %d %q", ct, code, msg)
	}
	eq(t, b.app.do(http.MethodPut, "/api/v1/settings", b.admin, map[string]any{"bizQuery.exportEnabled": true}).Code, 0, "打开导出开关")
	// 授权里没批导出
	_, _, body = b.export("SELECT id FROM orders")
	if code, msg := envelopeMsg(body); code != 40300 || !strings.Contains(msg, "不包含导出") {
		t.Fatalf("授权没批导出时应拒绝,实际 %d %q", code, msg)
	}
	g := b.body("biz@corp.io", "OA-EXP")
	g["maxRows"], g["allowExport"] = 5, true
	grantOf(t, b.put(g))

	_, ct, body = b.export("SELECT id, amount FROM orders ORDER BY id")
	if !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("两道开关都开应拿到 CSV,实际 %s %s", ct, body)
	}
	if !bytes.HasPrefix(body, []byte("\uFEFF")) {
		t.Error("CSV 应带 BOM,Excel 打开才不乱码")
	}
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(string(body), "\uFEFF")), "\n")
	if len(lines) != 6 || strings.TrimSpace(lines[0]) != "id,amount" {
		t.Fatalf("应是表头 + 5 行(授权上限),实际 %d 行:%q", len(lines), lines)
	}
	_, _, body = b.export("SELECT t FROM notes ORDER BY id")
	if !strings.Contains(string(body), `'=HYPERLINK`) {
		t.Errorf("以 = 开头的单元格要防公式注入,实际 %q", body)
	}
	// 导出同样过只读判定
	_, _, body = b.export("DELETE FROM orders")
	if code, _ := envelopeMsg(body); code != 40300 {
		t.Error("导出也不能执行写语句")
	}
	var n int64
	b.app.repo.DB().Model(&model.AuditLog{}).Where("result = ? AND operator LIKE ?", model.ResultExported, "查询授权 #% · 5 行%导出 CSV%").Count(&n)
	if n == 0 {
		t.Error("导出应写 result=exported 的审计,operator 带行数与「导出 CSV」")
	}
	b.app.repo.DB().Model(&model.AuditLog{}).Where("result = ? AND operator LIKE ?", model.ResultExecuted, "%ms").Count(&n)
	_ = n
}

func TestBizExec_AuditCarriesRowsAndTiming(t *testing.T) {
	b := newBizEnv(t, 0)
	b.exec("SELECT id FROM orders")
	var ops []string
	b.app.repo.DB().Model(&model.AuditLog{}).Where("result = ? AND operator LIKE ?", model.ResultExecuted, "查询授权 #%").Pluck("operator", &ops)
	if len(ops) != 1 || !strings.Contains(ops[0], "1000 行(已截断)") || !strings.HasSuffix(ops[0], "ms") {
		t.Fatalf("执行审计应带行数、截断与耗时,实际 %q", ops)
	}
}

func TestBizSettings_ValidatedAsAWhole(t *testing.T) {
	b := newBizEnv(t, 0)
	put := func(body map[string]any) apiResp { return b.app.do(http.MethodPut, "/api/v1/settings", b.admin, body) }
	cases := []struct {
		what string
		body map[string]any
		want string
	}{
		{"默认行数大于上限", map[string]any{"bizQuery.defaultMaxRows": 20000}, "默认行数"},
		{"上限超过硬边界", map[string]any{"bizQuery.maxRowsCap": 1000000}, "行数上限"},
		{"超时上限为 0", map[string]any{"bizQuery.timeoutCapSec": 0}, "超时上限"},
		{"不是整数", map[string]any{"bizQuery.maxGrantDays": "abc"}, "整数"},
		{"导出开关不是布尔", map[string]any{"bizQuery.exportEnabled": "yes"}, "布尔"},
		{"一坏一好也不存", map[string]any{"bizQuery.maxGrantDays": 30, "bizQuery.maxConcurrentPerUser": 999}, "每人并发"},
	}
	for _, c := range cases {
		r := put(c.body)
		if r.Code == 0 || !strings.Contains(r.Msg, c.want) {
			t.Errorf("%s:应拒绝并说明 %q,实际 %d %q", c.what, c.want, r.Code, r.Msg)
		}
	}
	if v, _ := b.app.repo.GetSetting("bizQuery.maxGrantDays"); v != "" {
		t.Errorf("校验失败时一项都不该存,maxGrantDays 却被存成 %q", v)
	}
	eq(t, put(map[string]any{"bizQuery.defaultMaxRows": 50, "bizQuery.maxGrantDays": 30}).Code, 0, "合法的值能存")
	_, out := b.exec("SELECT id FROM orders")
	eq(t, out.MaxRows, 50, "新的默认行数立即生效")
	var n int64
	b.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "settings.bizquery %").Count(&n)
	if n == 0 {
		t.Error("改数据查询设置应写审计")
	}
	if r := b.app.do(http.MethodPut, "/api/v1/settings", b.user, map[string]any{"bizQuery.exportEnabled": true}); r.Code == 0 {
		t.Error("业务方不该能改设置")
	}
}

func TestBizGrantsTable_FilterAndAdminRevoke(t *testing.T) {
	b := newBizEnv(t, 0)
	d := b.body("lisi@corp.io", "OA-L")
	d["dept"] = "客服部"
	grantOf(t, b.put(d))

	list := func(q string) []grantView {
		r := b.app.do(http.MethodGet, "/api/v1/query-grants"+q, b.admin, nil)
		var out struct {
			Items []grantView `json:"items"`
		}
		_ = json.Unmarshal(r.Data, &out)
		return out.Items
	}
	if got := list(""); len(got) != 2 {
		t.Fatalf("总表应列出全部授权,实际 %d 条", len(got))
	}
	if got := list("?dept=客服"); len(got) != 1 || got[0].Email != "lisi@corp.io" {
		t.Errorf("按部门筛选不对: %+v", got)
	}
	target := list("?email=biz@corp.io")[0]
	if r := b.app.do(http.MethodPost, "/api/v1/query-grants/"+itoa(target.ID)+"/revoke", b.user, nil); r.Code == 0 {
		t.Error("业务方不该能撤销授权")
	}
	eq(t, b.app.do(http.MethodPost, "/api/v1/query-grants/"+itoa(target.ID)+"/revoke", b.admin, map[string]any{"reason": "安全事件"}).Code, 0, "管理员撤销")
	if r, _ := b.exec("SELECT 1"); r.Code != 40300 {
		t.Error("管理员撤销后应立即失效")
	}
	var n int64
	b.app.repo.DB().Model(&model.AuditLog{}).Where("command LIKE ?", "bizquery.revoke %管理员 Lin Wei%安全事件%").Count(&n)
	eq(t, n, int64(1), "管理员撤销写审计,写明是谁、为什么")
}
