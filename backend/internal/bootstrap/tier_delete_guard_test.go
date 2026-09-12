package bootstrap

// 删分层:检查和删除要在同一次操作里,而且绑着流程模板的分层不能删。
//
// 原来三项检查(不是扫描基准、没有环境挂着、不是最后一个)跑在事务外,删除是第四步。
// 中间那一瞬里,另一个人可以把一个环境绑到这个分层上 —— 检查说「没有环境」,删除照做,
// 而那个环境从此指向一个不存在的分层。后果是 fail-closed(解析不到分层 → 拒绝每一条
// 命令),所以不是安全洞,是一台谁也说不清为什么用不了的实例。
//
// 还漏了一项:**流程模板**也按分层绑(Pipeline.TierCode)。分层删掉之后,那个模板永远
// 匹配不上任何东西 —— 因为不会再有实例属于一个不存在的分层。它躺在列表里,看起来好好
// 的,用的时候报「仅适用于 XXX 分层」,而那个分层已经不在了。

import (
	"net/http"
	"testing"

	"velagateway/internal/model"
)

func TestDeleteEnvTier_RefusesWhenAPipelineIsBoundToIt(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	// 一个没有环境挂着的新分层 —— 否则会先撞上「还有环境」那一条。
	eq(t, app.do(http.MethodPost, "/api/v1/env-tiers", token, map[string]any{
		"code": "sandbox", "displayName": "沙盒", "templateCode": "dev",
	}).Code, 0, "建分层")

	// 一个绑在它上面的流程模板。
	pid := app.createPipeline(token, "沙盒专用流程", "", []map[string]any{
		{"name": "执行变更", "type": "execute"},
	})
	if err := app.repo.DB().Model(&model.Pipeline{}).Where("id = ?", pid).
		Update("tier_code", "sandbox").Error; err != nil {
		t.Fatalf("绑定流程到分层: %v", err)
	}

	if r := app.do(http.MethodDelete, "/api/v1/env-tiers/sandbox", token, nil); r.Code == 0 {
		t.Error("绑着流程模板的分层被删掉了 —— 那个模板从此永远匹配不上任何东西," +
			"躺在列表里看起来好好的,用的时候才报「仅适用于一个不存在的分层」")
	}

	// 解绑之后可以删。
	if err := app.repo.DB().Model(&model.Pipeline{}).Where("id = ?", pid).
		Update("tier_code", "").Error; err != nil {
		t.Fatalf("解绑: %v", err)
	}
	eq(t, app.do(http.MethodDelete, "/api/v1/env-tiers/sandbox", token, nil).Code, 0,
		"解绑之后应当能删")
}

// 检查与删除是同一次操作:条件不再成立时,删除必须落空而不是照做。
//
// 这条直接打在仓储层的条件删除上 —— 并发窗口没法在集成测试里稳定复现,而它要守的正是
// 那个窗口里的行为:环境是在检查之后、删除之前绑上来的。
func TestDeleteEnvTierIfUnused_LosesToAConcurrentBinding(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	eq(t, app.do(http.MethodPost, "/api/v1/env-tiers", token, map[string]any{
		"code": "raced", "displayName": "竞态", "templateCode": "dev",
	}).Code, 0, "建分层")

	// 模拟「检查通过之后、删除之前」有人把环境绑了上来。
	if err := app.repo.DB().Create(&model.Environment{
		Code: "raced-env", DisplayName: "竞态环境", TierCode: "raced", SortOrder: 9,
	}).Error; err != nil {
		t.Fatalf("绑环境: %v", err)
	}

	deleted, err := app.repo.DeleteEnvTierIfUnused("raced")
	if err != nil {
		t.Fatalf("删除: %v", err)
	}
	if deleted {
		t.Error("环境已经绑上来了,删除却照做了 —— 那个环境从此指向一个不存在的分层")
	}
	var tier model.EnvTier
	if app.repo.DB().First(&tier, "code = ?", "raced").Error != nil {
		t.Error("分层被删掉了")
	}
}
