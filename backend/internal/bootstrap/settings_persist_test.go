package bootstrap

// 保存设置:写不进去就不能回 ok。
//
// 原先那一行是 `_ = h.Repo.SetSetting(k, s)` —— 错误被丢掉,接口照样回 `{"ok":true}`。
// 于是管理员在界面上看到「已保存」,而库里什么都没变:他以为审批超时改成了 30 分钟、
// 以为 IP 白名单开了,直到某天发现门一直开着。
//
// 一次静默的写失败比一次响亮的报错糟得多,因为**没有人会去复查一件他以为已经做完的事**。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestSaveSettings_ReportsAPersistenceFailure(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// 把设置表挪走 —— 落库从此必然失败。这比构造一个"恰好写不进去的值"可靠:
	// 要验的是**错误有没有被上报**,不是哪一种错误。
	if err := app.repo.DB().Exec("ALTER TABLE tbl_setting RENAME TO tbl_setting_moved").Error; err != nil {
		t.Fatalf("挪走设置表: %v", err)
	}

	r := app.do(http.MethodPut, "/api/v1/settings", token, map[string]any{
		"approval.timeoutMinutes": 30,
	})
	if r.Code == 0 {
		t.Error("一次写不进库的保存回了 ok —— 管理员会以为这条设置已经生效了")
	}
}

// 一批设置里有一条不合法,整批都不该落库。
//
// 保存是一次动作,不是十次:界面上点的是一个「保存」。如果第 3 条不合法而前 2 条已经
// 写进去了,管理员看到的是一句报错 —— 而他无从知道哪几条其实已经生效。
//
// 这个用例**依赖遍历顺序**,所以它在修复前是高概率红、不是必然红(非法的那条恰好第一个
// 被处理时,旧代码也什么都没写)。真正让它确定成立的是实现里的两段式:先把每一条都算成
// 最终要写的样子,一条都不写;全算完了再一次性写。
func TestSaveSettings_RejectsTheWholeBatchOnOneBadValue(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// 伴随键取种子里本来就有的那些 —— 于是断言可以落在**值变没变**上,而不是"键在不在"
	// (在不在对种子键永远为真,那种断言什么也守不住)。
	companions := map[string]any{
		"approval.timeoutMinutes": 4321,
		"security.idleMinutes":    4322,
		"export.maxRows":          4323,
		"export.retentionDays":    4324,
	}
	before := app.getSettingsMap(token)
	for k := range companions {
		if _, ok := before[k]; !ok {
			t.Fatalf("前置条件不成立:%q 不在种子里,这个用例需要一个本来就有值的键", k)
		}
	}

	body := map[string]any{
		// 这一条不合法:URL 解析不出协议。
		"approval.external.baseURL": "://not-a-url",
	}
	for k, v := range companions {
		body[k] = v
	}

	if r := app.do(http.MethodPut, "/api/v1/settings", token, body); r.Code == 0 {
		t.Fatal("一批里有不合法的值,整次保存却回了 ok")
	}

	after := app.getSettingsMap(token)
	for k := range companions {
		if fmt.Sprint(after[k]) != fmt.Sprint(before[k]) {
			t.Errorf("同一批里的 %q 被写进去了(%v → %v)—— 保存应当是一次动作,要么全成要么全不成",
				k, before[k], after[k])
		}
	}
}

// getSettingsMap 读回当前设置(GET /settings 把密钥类的键排除在外,这里用不到它们)。
func (a *testApp) getSettingsMap(token string) map[string]any {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/settings", token, nil)
	eq(a.t, r.Code, 0, "读设置")
	var out struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal(r.Data, &out); err != nil {
		a.t.Fatalf("decode settings: %v", err)
	}
	return out.Settings
}
