package bootstrap

// 审计链有人写,但没有任何地方会去验它。
//
// 这条哈希链存在的全部理由,是"事后有人动过库,查得出来"。链在写:每一行带着前一行的
// 哈希,PrevHash 上还有唯一索引防分叉。但**没有一个地方读它**——没有 verify 端点,
// 没有定时校验,谁也没有验过一次。
//
// (从前写的是"ADR 0006 那条哈希链",指错了:0006 是 open-api release intake,
// docs/adr/ 下今天没有一篇写审计链。那一篇 ADR 待补。)
//
// 一条没人验的链,和没有链的区别只在出事那天才显出来:那天你才发现,它从三个月前就断了。
//
// 这组用例要钉住三件事:
//   1. 正常跑出来的链,校验得过 —— 这条看着最无聊,其实最容易挂:payload 里的时间是
//      写入时刻格式化出来的,而校验只能从库里读回 OccurredAt。时区或精度差一点,
//      校验器就会把**每一行**都报成被篡改,而那比不校验更糟——没人会信一个天天喊狼来了
//      的警报。
//   2. 改掉一行的内容 → 查得出来,并且指得出是哪一行。
//   3. 从中间抽掉一行 → 查得出来(链的接口对不上了)。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"velagateway/internal/model"
)

type verifyResp struct {
	OK       bool   `json:"ok"`
	Checked  int    `json:"checked"`
	BrokenID int64  `json:"brokenId"`
	Reason   string `json:"reason"`
}

func (a *testApp) verifyChain(token string) verifyResp {
	a.t.Helper()
	env := a.do(http.MethodGet, "/api/v1/audit/verify", token, nil)
	if env.Code != 0 {
		a.t.Fatalf("verify 端点返回 code=%d msg=%s", env.Code, env.Msg)
	}
	var v verifyResp
	if err := json.Unmarshal(env.Data, &v); err != nil {
		a.t.Fatalf("verify decode: %v", err)
	}
	return v
}

// 先跑出一条真链,再验它。
func (a *testApp) makeAuditRows(token string, connID int64) {
	a.t.Helper()
	for _, sql := range []string{"SELECT 1", "SELECT 2", "SELECT 3"} {
		a.do(http.MethodPost, "/api/v1/terminal/exec", token, map[string]any{
			"connectionId": connID, "sql": sql,
		})
	}
}

func TestAuditChain_IntactChainVerifies(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.makeAuditRows(admin, app.connIDByEnv(admin, "dev"))

	v := app.verifyChain(admin)
	if !v.OK {
		t.Fatalf("一条没被动过的链没通过校验:第 %d 行,%s —— 校验器自己是错的,比不校验更糟", v.BrokenID, v.Reason)
	}
	if v.Checked < 3 {
		t.Errorf("只校验了 %d 行,链上至少该有 3 行", v.Checked)
	}
}

func TestAuditChain_EditedRowIsCaught(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.makeAuditRows(admin, app.connIDByEnv(admin, "dev"))

	var rows []model.AuditLog
	if err := app.repo.DB().Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(rows) < 3 {
		t.Fatalf("前置条件不成立:只有 %d 行", len(rows))
	}
	// 把中间某一行的命令改掉 —— 最典型的篡改:让一条 DROP 看起来像一条 SELECT。
	target := rows[len(rows)-2]
	if err := app.repo.DB().Model(&model.AuditLog{}).Where("id = ?", target.ID).
		Update("command", "SELECT '什么都没发生'").Error; err != nil {
		t.Fatalf("tamper: %v", err)
	}

	v := app.verifyChain(admin)
	if v.OK {
		t.Fatal("有人改了审计行的内容,校验说没事")
	}
	if v.BrokenID != target.ID {
		t.Errorf("报的是第 %d 行,实际被改的是第 %d 行 —— 指错了行,查的人会从错的地方开始找", v.BrokenID, target.ID)
	}
}

func TestAuditChain_DeletedRowIsCaught(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	app.makeAuditRows(admin, app.connIDByEnv(admin, "dev"))

	var rows []model.AuditLog
	if err := app.repo.DB().Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("read audit: %v", err)
	}
	gone := rows[len(rows)-2]
	if err := app.repo.DB().Delete(&model.AuditLog{}, gone.ID).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}

	v := app.verifyChain(admin)
	if v.OK {
		t.Fatal("有人从链中间抽掉了一行,校验说没事")
	}
	// 光"发现"不够。抽掉一行之后,下一行的内容校验**也**会失败(prev 本身就进哈希),
	// 所以这两种事故光看"通没通过"分不开。而查的人需要知道该从哪开始找:
	// 「这一行被改过」会让他去看那一行的内容,「接不上上一行」才会让他去想少了什么。
	if !strings.Contains(v.Reason, "接不上") {
		t.Errorf("删掉一行报的理由是 %q —— 它读起来像内容被改,会把查的人引到错的方向", v.Reason)
	}
	if v.BrokenID <= gone.ID {
		t.Errorf("报的是第 %d 行,而被删的是第 %d 行 —— 断裂该报在它后面那一行上", v.BrokenID, gone.ID)
	}
}
