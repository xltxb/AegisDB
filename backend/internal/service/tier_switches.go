package service

import (
	"context"

	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
)

// 分层上的两个开关(2026-10-29,迁移 0056):敏感字段脱敏、数据查询导出限制。默认 prod / staging / gli
// 开,dev / uat 关。分层解析不出来按开:确认不了是哪一层,就按生产对待。

// tierMasks:这台实例所在分层要不要按脱敏字典打码。
func (s *Services) tierMasks(conn *model.Connection) bool {
	if s.Repo == nil {
		return true // 没有库可查(只装了执行器的单元测试):按开
	}
	t, err := s.tierOf(conn)
	return err != nil || t == nil || t.MaskSensitive
}

// tierBizExportRestricted:这台实例上数据查询的导出,要不要受全局开关与授权「允许导出」约束。
func (s *Services) tierBizExportRestricted(conn *model.Connection) bool {
	if s.Repo == nil {
		return true
	}
	t, err := s.tierOf(conn)
	return err != nil || t == nil || t.BizExportRestrict
}

// tierAllowsAutoExecute:这台实例此刻所在的分层允不允许「自动执行」阶段跳过人工确认(迁移 0058)。
// 与上面两个开关方向相反 —— 开 = 放宽,所以分层解析不出来按**不允许**:确认不了是哪一层,就等人。
func (s *Services) tierAllowsAutoExecute(conn *model.Connection) bool {
	if s.Repo == nil {
		return false
	}
	t, err := s.tierOf(conn)
	return err == nil && t != nil && t.AutoExecute
}

// withTierMasking:分层关掉了脱敏,就让 ctx 下的执行跳过打码(gateway.WithMaskingOff)。
func (s *Services) withTierMasking(ctx context.Context, conn *model.Connection) context.Context {
	if s.tierMasks(conn) {
		return ctx
	}
	return gateway.WithMaskingOff(ctx)
}
