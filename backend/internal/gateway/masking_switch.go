package gateway

import "context"

// 敏感字段脱敏按分层开关(2026-10-29):分层上关掉了脱敏(默认 dev / uat),服务层在执行前给 ctx
// 打上 WithMaskingOff,网关所有打码点(终端结果、报错文本、导出、数据查询)照它跳过脱敏字典。
// 数据查询的口令列兜底(bizCredentialColumns)不受它影响 —— 那是系统表里的口令哈希,不是业务数据。

type maskingOffKey struct{}

// WithMaskingOff 标记 ctx 下的执行不按脱敏字典打码。
func WithMaskingOff(ctx context.Context) context.Context {
	return context.WithValue(ctx, maskingOffKey{}, true)
}

func maskingOff(ctx context.Context) bool {
	v, _ := ctx.Value(maskingOffKey{}).(bool)
	return v
}

// activeSensitiveRules:这次执行要用的脱敏规则;关掉了就是空。
func activeSensitiveRules(ctx context.Context) []SensitiveRule {
	if maskingOff(ctx) {
		return nil
	}
	return currentSensitiveRules()
}
