package repository

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// GetQueryGrantByKey 按 (人, 实例, 库) 取那一条授权;没有返回 gorm.ErrRecordNotFound。
func (r *Repo) GetQueryGrantByKey(userID, connID int64, db string) (*model.QueryGrant, error) {
	var g model.QueryGrant
	err := r.db.Where("user_id = ? AND connection_id = ? AND db_name = ?", userID, connID, db).First(&g).Error
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// CountQueryGrantsOn 数这个人在这台实例上有过多少条授权(任何状态)。
func (r *Repo) CountQueryGrantsOn(userID, connID int64) (int64, error) {
	var n int64
	err := r.db.Model(&model.QueryGrant{}).Where("user_id = ? AND connection_id = ?", userID, connID).Count(&n).Error
	return n, err
}

func (r *Repo) GetQueryGrant(id int64) (*model.QueryGrant, error) {
	var g model.QueryGrant
	if err := r.db.First(&g, id).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

// SaveQueryGrant 新建或整行保存一条授权。
func (r *Repo) SaveQueryGrant(g *model.QueryGrant) error {
	if g.ID == 0 {
		return r.db.Create(g).Error
	}
	return r.db.Save(g).Error
}

// QueryGrantApprovalUsed 报告这个外部系统的这张审批单是否作用过这条授权。
func (r *Repo) QueryGrantApprovalUsed(clientID int64, externalID string, grantID int64) (bool, error) {
	var n int64
	err := r.db.Model(&model.QueryGrantApproval{}).
		Where("client_id = ? AND external_id = ? AND grant_id = ?", clientID, externalID, grantID).Count(&n).Error
	return n > 0, err
}

// RecordQueryGrantApproval 记下审批单作用过这条授权;已记过就什么都不做。
func (r *Repo) RecordQueryGrantApproval(clientID int64, externalID string, grantID int64) error {
	if used, err := r.QueryGrantApprovalUsed(clientID, externalID, grantID); err != nil || used {
		return err
	}
	return r.db.Create(&model.QueryGrantApproval{ClientID: clientID, ExternalID: externalID, GrantID: grantID}).Error
}

// QueryGrantFilter 列表筛选。零值字段不参与筛选。
type QueryGrantFilter struct {
	UserID       int64
	ConnectionID int64
	DBName       string
	Status       string
	ExternalID   string
	ClientID     int64
	Limit        int
}

func (r *Repo) ListQueryGrants(f QueryGrantFilter) ([]model.QueryGrant, error) {
	q := r.db.Model(&model.QueryGrant{})
	if f.UserID > 0 {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.ConnectionID > 0 {
		q = q.Where("connection_id = ?", f.ConnectionID)
	}
	if f.DBName != "" {
		q = q.Where("db_name = ?", f.DBName)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.ExternalID != "" {
		q = q.Where("external_id = ?", f.ExternalID)
	}
	if f.ClientID > 0 {
		q = q.Where("client_id = ?", f.ClientID)
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 1000
	}
	var out []model.QueryGrant
	err := q.Order("id desc").Limit(f.Limit).Find(&out).Error
	return out, err
}

// ActiveQueryGrantsEndedBefore 取已过有效期、状态仍记为 active 的授权(过期任务用)。
func (r *Repo) ActiveQueryGrantsEndedBefore(t time.Time) ([]model.QueryGrant, error) {
	var out []model.QueryGrant
	err := r.db.Where("status = ? AND ends_at <= ?", model.GrantActive, t).Order("id asc").Limit(500).Find(&out).Error
	return out, err
}

// MarkQueryGrantExpired 只在它仍是 active 时改成 expired —— 与并发的"再次下发"不打架:
// 那边刚把它续上了,这里就什么都不改。
func (r *Repo) MarkQueryGrantExpired(id int64, endedBefore time.Time) (bool, error) {
	res := r.db.Model(&model.QueryGrant{}).
		Where("id = ? AND status = ? AND ends_at <= ?", id, model.GrantActive, endedBefore).
		Update("status", model.GrantExpired)
	return res.RowsAffected > 0, res.Error
}

// CountBizQueriesSince 数某人自 since 起真正执行了的业务查询与导出(每日次数上限用)。
//
// 数的是审计而不是内存计数器:网关重启、多实例部署都不丢。业务查询的执行 / 导出审计 operator
// 以"查询授权 #"开头(service.auditTag);被判定拒绝的(rejected)没碰到库,不算。
func (r *Repo) CountBizQueriesSince(actorID int64, since time.Time) (int64, error) {
	var n int64
	err := r.db.Model(&model.AuditLog{}).
		Where("actor_id = ? AND occurred_at >= ? AND operator LIKE ? AND result IN ?", actorID, since, "查询授权 #%",
			[]string{model.ResultExecuted, model.ResultSimulated, model.ResultExported, model.ResultWarn}).
		Count(&n).Error
	return n, err
}

// BizQuerySources 列出标记为业务查询源的实例。
func (r *Repo) BizQuerySources() ([]model.Connection, error) {
	var out []model.Connection
	err := r.db.Where("biz_query = ?", true).Order("name asc").Find(&out).Error
	return out, err
}

// MetaDatabaseNames 从元数据缓存里取一台实例上的库名(去重、排序)。
func (r *Repo) MetaDatabaseNames(connID int64) ([]string, error) {
	var names []string
	err := r.db.Model(&model.MetaTable{}).Where("connection_id = ?", connID).
		Distinct("db_name").Order("db_name asc").Pluck("db_name", &names).Error
	return names, err
}

// FindUserByEmailCI 按邮箱(不分大小写)找人。外部审批系统送来的邮箱大小写未必与网关里
// 登记的一致,而同一个邮箱就是同一个人。
func (r *Repo) FindUserByEmailCI(email string) (*model.User, error) {
	var u model.User
	if err := r.db.Where("LOWER(email) = ?", strings.ToLower(strings.TrimSpace(email))).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// SetConnectionBizQuery 只改一列 —— 不整行 Save,免得把并发改动的别的字段写回旧值。
func (r *Repo) SetConnectionBizQuery(id int64, on bool) error {
	return r.db.Model(&model.Connection{}).Where("id = ?", id).Update("biz_query", on).Error
}

// IsNotFound 统一判断"没有这一行"。
func IsNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

// ---------------------------------------------------------------- 网关内申请(#173)

func (r *Repo) CreateQueryGrantRequest(q *model.QueryGrantRequest) error { return r.db.Create(q).Error }

func (r *Repo) DeleteQueryGrantRequest(id int64) error {
	return r.db.Delete(&model.QueryGrantRequest{}, id).Error
}

func (r *Repo) GetQueryGrantRequest(id int64) (*model.QueryGrantRequest, error) {
	var q model.QueryGrantRequest
	if err := r.db.First(&q, id).Error; err != nil {
		return nil, err
	}
	return &q, nil
}

func (r *Repo) UpdateQueryGrantRequest(id int64, fields map[string]any) error {
	return r.db.Model(&model.QueryGrantRequest{}).Where("id = ?", id).Updates(fields).Error
}

// PendingQueryGrantRequest 返回这个人对这个实例这个库还在等审批的那次申请(没有返回 nil)。
func (r *Repo) PendingQueryGrantRequest(userID, connID int64, db string) (*model.QueryGrantRequest, *model.Approval, error) {
	var q model.QueryGrantRequest
	err := r.db.Table("tbl_query_grant_request AS q").
		Select("q.*").
		Joins("JOIN tbl_approval a ON a.id = q.approval_id").
		Where("q.user_id = ? AND q.connection_id = ? AND q.db_name = ? AND a.status = ?", userID, connID, db, model.StatusPending).
		Order("q.id DESC").Limit(1).Scan(&q).Error
	if err != nil || q.ID == 0 {
		return nil, nil, err
	}
	ap, err := r.GetApproval(q.ApprovalID)
	if err != nil {
		return nil, nil, err
	}
	return &q, ap, nil
}

// ListQueryGrantRequests 列出一个人最近的申请(新的在前)。
func (r *Repo) ListQueryGrantRequests(userID int64, limit int) ([]model.QueryGrantRequest, error) {
	var out []model.QueryGrantRequest
	err := r.db.Where("user_id = ?", userID).Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}
