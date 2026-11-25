package bootstrap

// 接口报错的多语言(apimsg,.scratch/api-msg-i18n)。
//
// msg 永远是原来那句中文(开放接口调用方、日志、中文界面都看它),另带 msgRef 让英文界面翻译。
// 这里从 HTTP 黑盒确认两件事:中文 msg 一字未变;msgRef 带着正确的 code 与参数。

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
)

func msgRefOf(t *testing.T, r apiResp) *model.RuleRef {
	t.Helper()
	if len(r.MsgRef) == 0 || string(r.MsgRef) == "null" {
		return nil
	}
	var ref model.RuleRef
	if err := json.Unmarshal(r.MsgRef, &ref); err != nil {
		t.Fatalf("msgRef 解析失败:%v(%s)", err, r.MsgRef)
	}
	return &ref
}

// 用户报上来的那一句:英文界面提交发布单,类型不符的提示是整句中文。
func TestAPIMsgReleaseChangeTypeCarriesRef(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "多语言报错流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})
	r := submitTyped(t, app, admin, stg, pid, "DDL单装DML", "ddl", "UPDATE tbl_order SET memo='y' WHERE id=1;")
	eq(t, r.Msg, "变更类型为 DDL 的升级单不允许 DML 语句:第 1 条(UPDATE)。请拆成两张单,或改正变更类型", "中文 msg 一字不变")
	ref := msgRefOf(t, r)
	if ref == nil {
		t.Fatal("报错应带 msgRef")
	}
	eq(t, ref.Code, apimsg.Code("变更类型为 %s 的升级单不允许 %s 语句:第 %d 条(%s)。请拆成两张单,或改正变更类型"), "code")
	for i, want := range []string{"DDL", "DML", "1", "UPDATE"} {
		eq(t, ref.Args[string(rune('0'+i))], want, "参数")
	}
}

// 固定中文(resp.Fail 的字面量、中间件的 Abort)按原串认出。
func TestAPIMsgFixedSentencesCarryRef(t *testing.T) {
	app := newTestApp(t)

	r := app.do(http.MethodGet, "/api/v1/releases", "", nil)
	if r.Code == 0 {
		t.Fatal("未登录应被拒")
	}
	if ref := msgRefOf(t, r); ref == nil || ref.Code != apimsg.Code(r.Msg) {
		t.Errorf("中间件的拒绝应带 msgRef:msg=%q ref=%s", r.Msg, r.MsgRef)
	}

	admin := app.login("linwei@vela.io", "vela123")
	r = app.do(http.MethodPost, "/api/v1/releases", admin, "not an object")
	eq(t, r.Msg, "参数错误", "中文 msg 一字不变")
	if ref := msgRefOf(t, r); ref == nil || ref.Code != apimsg.Code("参数错误") {
		t.Errorf("参数错误应带 msgRef,got %s", r.MsgRef)
	}
}

// 成功响应不带 msgRef(msg 是 "ok")。
func TestAPIMsgOKHasNoRef(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	r := app.do(http.MethodGet, "/api/v1/releases", admin, nil)
	eq(t, r.Code, 0, "code")
	if msgRefOf(t, r) != nil {
		t.Errorf("成功响应不该有 msgRef:%s", r.MsgRef)
	}
}
