package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 2026-11-14 规范配置:参数说明书经 catalog 下发;保存内置规则时按说明书校验;
// 而切级别/开关原样带回的旧参数不校验 —— 库里早先存下的写法不能让人连开关都关不掉。
func TestReviewRuleParams_CatalogValidateAndLegacyToggle(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodGet, "/api/v1/sql-review/catalog", admin, nil)
	eq(t, r.Code, 0, "catalog")
	var cat struct {
		Params map[string][]struct {
			Key             string         `json:"key"`
			Type            string         `json:"type"`
			Default         any            `json:"default"`
			DialectDefaults map[string]any `json:"dialectDefaults"`
		} `json:"params"`
	}
	if err := json.Unmarshal(r.Data, &cat); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	idLen := cat.Params["naming.identifier.length"]
	if len(idLen) != 1 || idLen[0].Key != "max" || idLen[0].Default != float64(64) || idLen[0].DialectDefaults["oracle"] != float64(30) {
		t.Fatalf("naming.identifier.length 的参数说明不对: %+v", idLen)
	}
	if _, ok := cat.Params["dml.require.where"]; ok {
		t.Error("dml.require.where 没有参数,不该出现在说明书里")
	}
	if _, ok := cat.Params["_regex"]; !ok {
		t.Error("自定义规则的参数说明(_regex)缺失")
	}

	id := app.ruleIDByCode(admin, "naming.identifier.length")
	save := func(params string) (int, string) {
		r := app.do(http.MethodPut, "/api/v1/sql-review/rules/"+itoa(id), admin, map[string]any{
			"name": "标识符长度上限", "level": "warn", "enabled": true, "params": params,
		})
		return r.Code, r.Msg
	}
	if code, msg := save(`{"max":"abc"}`); code == 0 || !strings.Contains(msg, "正整数") {
		t.Errorf("max=abc 应被拒并说明须为正整数,得到 code=%d msg=%s", code, msg)
	}
	if code, msg := save(`{"maxx":10}`); code == 0 || !strings.Contains(msg, "maxx") {
		t.Errorf("写错的键应被拒并点名,得到 code=%d msg=%s", code, msg)
	}
	if code, msg := save(`{"max":128}`); code != 0 {
		t.Fatalf("合法参数应保存成功: %s", msg)
	}

	// 一条带着旧写法参数的行(说明书出来之前存下的):改级别/开关照常可以。
	if err := app.repo.UpdateSQLReviewRuleFields(id, map[string]any{"params": `{"legacy":1}`}); err != nil {
		t.Fatalf("seed legacy params: %v", err)
	}
	r = app.do(http.MethodPut, "/api/v1/sql-review/rules/"+itoa(id), admin, map[string]any{
		"name": "标识符长度上限", "level": "info", "enabled": false, "params": `{"legacy":1}`,
	})
	eq(t, r.Code, 0, "toggle with untouched legacy params")
}
