package repository

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"velagateway/internal/model"
)

// ErrIMIdentityTaken:这个平台账号已经挂在另一个网关账户上。
var ErrIMIdentityTaken = errors.New("im identity already bound to another user")

// BindIMIdentity 写入 / 覆盖一个人在某渠道的绑定。
//
// 先查「这个平台账号是不是别人的」再写,放在同一个事务里;唯一索引是最后一道 ——
// 并发下两人同时绑同一个 ID,后到的那个会在索引上失败,而不是悄悄把前一个挤掉。
func (r *Repo) BindIMIdentity(id *model.IMIdentity) error {
	if id.CreatedAt.IsZero() {
		id.CreatedAt = time.Now()
	}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.IMIdentity{}).
			Where("channel = ? AND platform_user_id = ? AND user_id <> ?", id.Channel, id.PlatformUserID, id.UserID).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrIMIdentityTaken
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "channel"}},
			DoUpdates: clause.AssignmentColumns([]string{"platform_user_id", "bound_by", "created_at"}),
		}).Create(id).Error
	})
	// 并发下两边都查到「没人占」,后写的那个撞在 uk_im_identity_platform 上。它说的是同一件
	// 事 —— 这个平台账号已经是别人的 —— 所以给同一个错误。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uk_im_identity_platform" {
		return ErrIMIdentityTaken
	}
	return err
}

func (r *Repo) IMIdentityByPlatform(channel, pid string) (*model.IMIdentity, error) {
	var id model.IMIdentity
	if err := r.db.Where("channel = ? AND platform_user_id = ?", channel, pid).First(&id).Error; err != nil {
		return nil, err
	}
	return &id, nil
}

func (r *Repo) IMIdentitiesOfUser(userID int64) ([]model.IMIdentity, error) {
	var out []model.IMIdentity
	err := r.db.Where("user_id = ?", userID).Order("channel").Find(&out).Error
	return out, err
}

func (r *Repo) IMPlatformIDs(channel string, userIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(userIDs) == 0 {
		return out, nil
	}
	var rows []model.IMIdentity
	if err := r.db.Where("channel = ? AND user_id IN ?", channel, userIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.UserID] = row.PlatformUserID
	}
	return out, nil
}

func (r *Repo) UnbindIMIdentity(userID int64, channel string) (bool, error) {
	res := r.db.Where("user_id = ? AND channel = ?", userID, channel).Delete(&model.IMIdentity{})
	return res.RowsAffected > 0, res.Error
}

// PutIMBindCode 按 (user_id, channel) upsert —— 新码进来,旧码就没了。
func (r *Repo) PutIMBindCode(c *model.IMBindCode) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "channel"}},
		DoUpdates: clause.AssignmentColumns([]string{"code_hash", "expires_at"}),
	}).Create(c).Error
}

// ConsumeIMBindCode 原子地「找到并删掉」一个未过期的码。DELETE … RETURNING 是一条语句,
// 两条私聊同时带着同一个码进来,只有一条拿得到 user_id。
func (r *Repo) ConsumeIMBindCode(channel, codeHash string, now time.Time) (int64, bool, error) {
	var ids []int64
	err := r.db.Raw(`DELETE FROM tbl_im_bind_code
	                 WHERE channel = ? AND code_hash = ? AND expires_at > ?
	                 RETURNING user_id`, channel, codeHash, now).Scan(&ids).Error
	if err != nil || len(ids) == 0 {
		return 0, false, err
	}
	return ids[0], true, nil
}

func (r *Repo) PurgeExpiredIMBindCodes(now time.Time) (int64, error) {
	res := r.db.Where("expires_at <= ?", now).Delete(&model.IMBindCode{})
	return res.RowsAffected, res.Error
}

func (r *Repo) CreateIMCard(c *model.IMCard) error { return r.db.Create(c).Error }

func (r *Repo) OpenIMCards(approvalID int64) ([]model.IMCard, error) {
	var out []model.IMCard
	err := r.db.Where("approval_id = ? AND NOT closed", approvalID).Order("id").Find(&out).Error
	return out, err
}

func (r *Repo) CloseIMCard(id int64) error {
	return r.db.Model(&model.IMCard{}).Where("id = ?", id).Update("closed", true).Error
}
