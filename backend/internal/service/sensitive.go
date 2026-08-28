package service

// 敏感字段维护 —— 按 表名 + 字段名 定义,结果集在离开网关之前打码。
//
// 脱敏本身在 gateway 里做(那里是唯一两处把行读出来的地方);这里只管**规则**:
// 增删改查,以及把当前规则喂给网关。
//
// 规则读得很频繁(每条查询都要),所以带一层短缓存。缓存不是为了省那次查询,而是
// 为了让"每查一次就读一次库"不至于把敏感字段表变成热点 —— 而它必须足够短,否则
// 运维刚加了一条规则,数据还在明文往外走。

import (
	"fmt"
	"strings"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// sensitiveCacheTTL 是规则缓存的存活时间。
//
// 取 5 秒:够挡住高频查询对这张表的反复读取,又短到"加完规则去试一下"就已经生效。
// 一个几分钟的缓存会让人以为规则没保存成功,然后去点第二次、第三次。
const sensitiveCacheTTL = 5 * time.Second

// SensitiveRulesForGateway 是网关取规则的入口(启动时挂上 provider)。
//
// 缓存挂在 Services 实例上,不是包级变量 —— 包级的那一份会在多个 Services 之间
// 串味,而一个"有时候用的是别人的规则"的脱敏开关,比没有还危险。
func (s *Services) SensitiveRulesForGateway() []gateway.SensitiveRule {
	s.sensitiveMu.RLock()
	if !s.sensitiveAt.IsZero() && time.Since(s.sensitiveAt) < sensitiveCacheTTL {
		out := s.sensitiveRules
		s.sensitiveMu.RUnlock()
		return out
	}
	s.sensitiveMu.RUnlock()

	rows, err := s.Repo.ListSensitiveColumns()
	if err != nil {
		// 读不出来时沿用上一次的结果,而不是返回空 —— 空意味着"什么都不脱敏",
		// 一次数据库抖动就会让敏感数据明文回传。宁可用旧规则。
		s.sensitiveMu.RLock()
		defer s.sensitiveMu.RUnlock()
		return s.sensitiveRules
	}
	out := make([]gateway.SensitiveRule, 0, len(rows))
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		out = append(out, gateway.SensitiveRule{Table: r.Tbl, Column: r.ColumnName, Style: r.MaskStyle})
	}
	s.sensitiveMu.Lock()
	s.sensitiveRules, s.sensitiveAt = out, time.Now()
	s.sensitiveMu.Unlock()
	return out
}

// invalidateSensitiveCache 让下一次取规则立刻回库读。改完规则马上生效是这件事
// 唯一可接受的行为 —— 敏感数据多明文流出 5 秒都是多的。
func (s *Services) invalidateSensitiveCache() {
	s.sensitiveMu.Lock()
	s.sensitiveAt = time.Time{}
	s.sensitiveMu.Unlock()
}

func (s *Services) ListSensitiveColumns() []dto.SensitiveColumnView {
	rows, err := s.Repo.ListSensitiveColumns()
	if err != nil {
		return []dto.SensitiveColumnView{}
	}
	out := make([]dto.SensitiveColumnView, 0, len(rows))
	for _, r := range rows {
		out = append(out, dto.SensitiveColumnView{
			ID: r.ID, TableName: r.Tbl, ColumnName: r.ColumnName,
			MaskStyle: r.MaskStyle, Enabled: r.Enabled, Note: r.Note, CreatedAt: r.CreatedAt,
		})
	}
	return out
}

// SaveSensitiveColumn 新建或编辑一条规则。
func (s *Services) SaveSensitiveColumn(actor *model.User, id int64, req dto.SensitiveColumnReq) (*model.SensitiveColumn, error) {
	table := strings.TrimSpace(req.TableName)
	col := strings.TrimSpace(req.ColumnName)
	if col == "" {
		return nil, fmt.Errorf("字段名不能为空")
	}
	if table == "" {
		// 留空按"所有表"处理并写成 * —— 让它在列表里显式可见,而不是一个看不出
		// 含义的空格。
		table = "*"
	}
	if err := validateMaskStyle(req.MaskStyle); err != nil {
		return nil, err
	}
	style := strings.TrimSpace(req.MaskStyle)
	if style == "" {
		style = gateway.MaskPartial
	}
	defer s.invalidateSensitiveCache()

	if id > 0 {
		if _, err := s.Repo.GetSensitiveColumn(id); err != nil {
			return nil, ErrNotFound
		}
		fields := map[string]any{
			"table_name": clip(table, 128), "column_name": clip(col, 128),
			"mask_style": style, "enabled": req.Enabled,
			"note": clip(strings.TrimSpace(req.Note), 255), "updated_at": time.Now(),
		}
		if err := s.Repo.UpdateSensitiveColumnFields(id, fields); err != nil {
			return nil, err
		}
		s.auditAdminAction(actor, "admin.sensitive.update "+table+"."+col)
		return s.Repo.GetSensitiveColumn(id)
	}

	if existing, err := s.Repo.GetSensitiveColumnBy(table, col); err == nil {
		return nil, fmt.Errorf("%s.%s 已经在敏感字段列表里(#%d)", table, col, existing.ID)
	}
	row := &model.SensitiveColumn{
		Tbl: clip(table, 128), ColumnName: clip(col, 128),
		MaskStyle: style, Enabled: true, Note: clip(strings.TrimSpace(req.Note), 255),
		CreatedBy: actor.ID, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.Repo.CreateSensitiveColumn(row); err != nil {
		return nil, err
	}
	s.auditAdminAction(actor, "admin.sensitive.create "+table+"."+col)
	return row, nil
}

// DeleteSensitiveColumn 删掉一条规则 —— 这会让该字段**从此明文回传**,所以它是
// 一个要写进审计的动作。
func (s *Services) DeleteSensitiveColumn(actor *model.User, id int64) error {
	row, err := s.Repo.GetSensitiveColumn(id)
	if err != nil {
		return ErrNotFound
	}
	if err := s.Repo.DeleteSensitiveColumn(id); err != nil {
		return err
	}
	s.invalidateSensitiveCache()
	s.auditAdminAction(actor, "admin.sensitive.delete "+row.Tbl+"."+row.ColumnName+" (该字段将不再脱敏)")
	return nil
}

func validateMaskStyle(style string) error {
	switch strings.TrimSpace(style) {
	case "", gateway.MaskPartial, gateway.MaskFull, gateway.MaskHash:
		return nil
	}
	return fmt.Errorf("脱敏方式只能是 partial / full / hash")
}
