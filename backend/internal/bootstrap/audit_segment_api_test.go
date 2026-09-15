package bootstrap

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 分段校验走**完整的接线**:PUT /api/v1/settings 设起点,GET /api/v1/audit/verify 读它。
//
// service 层那两条用例(audit_segment_test.go)用 Repo.SetSettings 直接写库,
// 绕过了 API 的 JSON 编码 —— 于是它们全绿,而真实路径是坏的:设置页传字符串时
// 库里存的是 `"3"`,SettingInt 只认裸的 `3`,读出来回落成 0,起点等于没设。
// 报告照样返回 ok/checked,只是 verifiedFromId 是 0,没有任何错误冒出来。
//
// 这就是 harness 里那句话说的事("测试通过了但线上才是另一套接线,是最不该有的
// 一种绿")。所以这一条必须从 HTTP 这一端进去。
func TestAuditSegment_SetThroughTheAPITakesEffect(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")

	// 先确认默认是全量校验。
	var before struct {
		OK             bool  `json:"ok"`
		Checked        int   `json:"checked"`
		VerifiedFromID int64 `json:"verifiedFromId"`
		FirstID        int64 `json:"firstId"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/audit/verify", admin, nil).Data, &before)
	if before.VerifiedFromID != 0 {
		t.Fatalf("默认应当是全量校验,verifiedFromId = %d", before.VerifiedFromID)
	}
	if before.Checked < 2 {
		t.Skipf("夹具里审计行不足(%d 行),这条用例需要至少 2 行", before.Checked)
	}

	// 通过 API 设起点 —— 传字符串,那正是设置页会做的事。
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin,
		map[string]any{auditVerifyFromKeyForTest: "2"}).Code, 0, "设置起点")

	var after struct {
		VerifiedFromID int64  `json:"verifiedFromId"`
		FirstID        int64  `json:"firstId"`
		Note           string `json:"note"`
	}
	_ = json.Unmarshal(app.do(http.MethodGet, "/api/v1/audit/verify", admin, nil).Data, &after)

	if after.VerifiedFromID != 2 {
		t.Errorf("通过 API 设了起点,校验却仍从头查:verifiedFromId = %d —— "+
			"设置存进去了但读不出来(SettingInt 只认裸 JSON 数字?)", after.VerifiedFromID)
	}
	// 判据是**起点行号**,不是行数:这套系统里每个操作都在写审计,
	// 连上面那次 PUT /settings 自己都加了一行,所以 checked 会一边被裁掉开头、
	// 一边从尾部长出来。拿它做断言是脆的。
	if after.FirstID != 2 {
		t.Errorf("分段之后第一行仍是 %d,应当是起点 2 —— id=1 没有被跳过", after.FirstID)
	}
	if !strings.Contains(after.Note, "第 2 行") {
		t.Errorf("Note 没有披露起点,读报告的人会把结论当成全量:\n  %s", after.Note)
	}
}

// auditVerifyFromKeyForTest 与 service.auditVerifyFromKey 同值。
//
// 不能直接引用那个常量:它在 service 包里未导出。抄一份的代价是两处可能漂开,
// 所以这里断言它确实是设置表认得的那个 key —— 上面那条用例一旦 key 写错,
// verifiedFromId 会一直是 0,而错误信息会把人引向 SettingInt。
const auditVerifyFromKeyForTest = "audit.chain.verifyFromId"
