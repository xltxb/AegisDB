package bootstrap

// 把**按事件类型**的旧订阅换成**按增删改查**的新订阅。
//
// 订阅那一栏的取值集合换了一维:以前存的是 intercept / approve / exec / login,现在存的是
// create / delete / update / read / other（见 service.Dispatch）。
//
// 不迁的话是一个**静默停推**:旧值一个都匹配不上,webhook 从升级那一刻起什么都不发，而
// 界面上那一栏会变成一排都没勾的格子 —— 没有报错、没有日志，一条审计事件都不再流到 SIEM，
// 要等有人去对账才会发现。对一条审计外发链路来说，这是最坏的一种失败。
//
// ## 换成什么
//
// 旧的四类和新的五类之间**没有忠实的对应**。"exec" 是"真执行了"，它既可能是一次 SELECT
// 也可能是一次 DROP；"intercept" 同样横跨增删改查。硬编一张映射表只会是一份看起来精确的
// 猜测。
//
// 所以不猜，**放宽成全部五类**，并且大声记一条日志。方向是有意选的：
//
//	推多了   噪音。看得见，运维去设置页取消勾选即可。
//	推少了   审计事件悄悄漏掉。看不见，而这正是这条链路存在的理由。
//
// 对一条审计外发链路，宁可吵，不可漏。
//
// ## 只碰"确实是旧值"的那一种
//
// 判据是：这一栏**非空**，但里面**一个合法的增删改查都没有**。那只可能是升级前留下的旧值。
//
//   - 空列表不碰：它是一个明确的"什么都不推"（SaveWebhook 特意保留了这个语义）；
//   - 已经是新值的不碰；
//   - 半新半旧的不碰：里面已经有合法项，说明有人在新界面上存过，剩下的那些会在下一次保存时
//     被 normalizeEvents 归一掉。
//
// 因此天然幂等：跑过一遍之后，这一栏里一定有合法项，条件不再成立。

import (
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// backfillWebhookActions 把旧的事件类型订阅换成全部增删改查。
func backfillWebhookActions(db *gorm.DB) error {
	var rows []model.WebhookConfig
	if err := db.Find(&rows).Error; err != nil {
		return err
	}
	for _, wh := range rows {
		if !isLegacyEventSubscription(wh.Events) {
			continue
		}
		all := strings.Join(gateway.DataActions, ",")
		if err := db.Model(&model.WebhookConfig{}).
			Where("id = ?", wh.ID).Update("events", all).Error; err != nil {
			return err
		}
		// 这条日志必须留:它是运维唯一会知道"订阅被放宽了"的地方。数字之外还印出旧值,
		// 好让人能照着原来的意图重新收窄。
		slog.Warn("webhook 订阅改按「增删改查」匹配,旧的按事件类型的订阅已放宽为全部 —— "+
			"请到 设置 › Webhook 按需收窄(推多了是噪音,推少了是审计事件悄悄漏掉)",
			"id", wh.ID, "旧值", wh.Events, "新值", all)
	}
	return nil
}

// isLegacyEventSubscription:非空,但一个合法的增删改查都没有。
func isLegacyEventSubscription(events string) bool {
	if strings.TrimSpace(events) == "" {
		return false // 空 = 明确的"什么都不推",不是旧值
	}
	for _, part := range strings.Split(events, ",") {
		if gateway.IsDataAction(strings.ToLower(strings.TrimSpace(part))) {
			return false // 已经有新值了
		}
	}
	return true
}
