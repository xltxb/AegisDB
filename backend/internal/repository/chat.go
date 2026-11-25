package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"aegisdb/internal/model"
)

// ChatBindingOfUser 取一个用户在某个渠道上的绑定;没有返回 gorm.ErrRecordNotFound。
func (r *Repo) ChatBindingOfUser(userID int64, channel string) (*model.ChatBinding, error) {
	var b model.ChatBinding
	if err := r.db.Where("channel = ? AND user_id = ?", channel, userID).First(&b).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

// ChatBindingOfAccount 取某个机器人下某个聊天账号的绑定;没有返回 gorm.ErrRecordNotFound。
//
// 库里的比较不分大小写(MySQL *_ci、PG citext),而聊天账号标识是平台给的不透明串 —— 这是在认人,
// 差一个大小写也是另一个人,所以取出来再逐字比一遍。
func (r *Repo) ChatBindingOfAccount(channel, botID, accountID string) (*model.ChatBinding, error) {
	var b model.ChatBinding
	err := r.db.Where("channel = ? AND bot_id = ? AND account_id = ?", channel, botID, accountID).First(&b).Error
	if err != nil {
		return nil, err
	}
	if b.BotID != botID || b.AccountID != accountID {
		return nil, gorm.ErrRecordNotFound
	}
	return &b, nil
}

// ChatCardByTokenHash 按按钮令牌的摘要取卡片;没有返回 gorm.ErrRecordNotFound。
//
// 空摘要一律查不到:还没发出的卡片摘要都是空的,放过去的话,一个不带令牌的点击会落到别人的卡片上。
func (r *Repo) ChatCardByTokenHash(hash string) (*model.ChatCard, error) {
	if hash == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var c model.ChatCard
	if err := r.db.Where("token_hash = ?", hash).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// 兑换绑定码时可以说清楚的两种失败;其余错误都是系统问题。
var (
	// ErrChatCodeInvalid:码不存在、用过、过期、不属于这个渠道,或它的主人已不能绑定。
	// 故意不细分:发来码的人还没有身份,不告诉他是哪一种。
	ErrChatCodeInvalid = errors.New("chat bind code invalid")
	// ErrChatAccountTaken:这个聊天账号已经绑着另一个网关账号。
	ErrChatAccountTaken = errors.New("chat account already bound")
)

// SaveChatBindCode 存下一个新的绑定码(只有摘要)。一个用户每个渠道只有一行,新码就是改写它 ——
// 旧码在同一条语句里作废,两个并发的生成也不会同时留下两个有效的码。
func (r *Repo) SaveChatBindCode(userID int64, channel, hash string, expiresAt time.Time) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"code_hash", "expires_at", "used_at"}),
	}).Create(&model.ChatBindCode{UserID: userID, Channel: channel, CodeHash: hash, ExpiresAt: expiresAt}).Error
}

// DeleteChatBindCode 作废一个刚生成的码。
func (r *Repo) DeleteChatBindCode(hash string) error {
	return r.db.Where("code_hash = ?", hash).Delete(&model.ChatBindCode{}).Error
}

// RedeemChatBindCode 用一个码把聊天账号绑到码的主人名下,返回他;已有的绑定被替换。
//
// 整件事在一个事务里,任何一步不成都当没发生过(码也没用掉)。第一步先用 UPDATE 认领那一行码:
// 它既保证并发两次只有一次成功,又让解绑(DeleteChatBindings 先删码)排在它前面或后面 ——
// 排在前面则码已经没了,排在后面则刚写上的绑定会被一并删掉,不会漏。
func (r *Repo) RedeemChatBindCode(hash, channel, botID, accountID, chatID string, now time.Time) (*model.User, error) {
	var u model.User
	err := r.db.Transaction(func(tx *gorm.DB) error {
		claim := tx.Model(&model.ChatBindCode{}).
			Where("code_hash = ? AND channel = ? AND used_at IS NULL", hash, channel).Update("used_at", now)
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return ErrChatCodeInvalid
		}
		var code model.ChatBindCode
		if err := tx.Where("code_hash = ?", hash).First(&code).Error; err != nil {
			return err
		}
		if !now.Before(code.ExpiresAt) {
			return ErrChatCodeInvalid
		}
		if err := tx.First(&u, code.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrChatCodeInvalid
			}
			return err
		}
		if u.Status != "active" || u.Kind == model.UserKindService {
			return ErrChatCodeInvalid
		}
		in := &Repo{db: tx}
		switch held, err := in.ChatBindingOfAccount(channel, botID, accountID); {
		case err == nil && held.UserID != u.ID:
			return ErrChatAccountTaken
		case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}
		// 已有绑定就地改写,没有才插入。不先删后插:MySQL 上两个人同时首次绑定时,对不存在的行
		// 执行 DELETE 会各持一把间隙锁,随后的 INSERT 互相等待而死锁。
		old, err := in.ChatBindingOfUser(u.ID, channel)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&model.ChatBinding{UserID: u.ID, Channel: channel, BotID: botID,
				AccountID: accountID, ChatID: chatID, CreatedAt: now}).Error
		}
		if err != nil {
			return err
		}
		res := tx.Model(&model.ChatBinding{}).Where("id = ?", old.ID).Updates(map[string]any{
			"bot_id": botID, "account_id": accountID, "chat_id": chatID, "created_at": now})
		if res.Error == nil && res.RowsAffected != 1 {
			return errors.New("chat binding row vanished while being replaced") // 不能回「已绑定」而库里没有
		}
		return res.Error
	})
	if err == nil {
		return &u, nil
	}
	if !errors.Is(err, ErrChatCodeInvalid) && !errors.Is(err, ErrChatAccountTaken) {
		// 两个人同时抢同一个聊天账号:上面的查询都看不到对方,输的一方撞唯一索引。撞了就再查一次,
		// 给出准话,不按驱动各自的错误码去猜。
		if held, herr := r.ChatBindingOfAccount(channel, botID, accountID); herr == nil && held.UserID != u.ID {
			return nil, ErrChatAccountTaken
		}
	}
	return nil, err
}

// DeleteChatBindings 删掉一个用户的绑定与绑定码(channel 为空 = 全部渠道),返回真被这一次删掉的绑定。
// 先删码:见 RedeemChatBindCode。
func (r *Repo) DeleteChatBindings(userID int64, channel string) ([]model.ChatBinding, error) {
	var gone []model.ChatBinding
	err := r.db.Transaction(func(tx *gorm.DB) error {
		mine := func() *gorm.DB {
			q := tx.Where("user_id = ?", userID)
			if channel != "" {
				q = q.Where("channel = ?", channel)
			}
			return q
		}
		if err := mine().Delete(&model.ChatBindCode{}).Error; err != nil {
			return err
		}
		var rows []model.ChatBinding
		if err := mine().Order("id").Find(&rows).Error; err != nil {
			return err
		}
		gone = nil
		for _, b := range rows {
			res := tx.Where("id = ?", b.ID).Delete(&model.ChatBinding{})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 1 { // 并发的另一次解绑先删了:由它去通知
				gone = append(gone, b)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return gone, nil
}

// ----------------------------------------------------------------- 审批卡片

// ChatBindingsOfUsers 取一批用户在几个渠道上的绑定(不管绑的是哪个机器人)。
func (r *Repo) ChatBindingsOfUsers(userIDs []int64, channels []string) ([]model.ChatBinding, error) {
	var bs []model.ChatBinding
	if len(userIDs) == 0 || len(channels) == 0 {
		return bs, nil
	}
	err := r.db.Where("user_id IN ? AND channel IN ?", userIDs, channels).Order("id").Find(&bs).Error
	return bs, err
}

// QueueChatCards 排下一批要发的卡片;已经有的(同一张单、同一个人、同一个渠道)跳过。
func (r *Repo) QueueChatCards(cards []model.ChatCard) error {
	if len(cards) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "approval_id"}, {Name: "user_id"}, {Name: "channel"}},
		DoNothing: true,
	}).Create(&cards).Error
}

// QueuedChatCards 是一个渠道上到期该发的卡片。
func (r *Repo) QueuedChatCards(channel string, now time.Time, limit int) ([]model.ChatCard, error) {
	return r.dueChatCards(model.ChatCardQueued, channel, now, limit, "")
}

// EndedChatCards 是一个渠道上已发出、单子却已不在待审批(或已不存在)、到期该更新的卡片。
func (r *Repo) EndedChatCards(channel string, now time.Time, limit int) ([]model.ChatCard, error) {
	return r.dueChatCards(model.ChatCardSent, channel, now, limit,
		"approval_id NOT IN (SELECT id FROM tbl_approval WHERE status = '"+model.StatusPending+"')")
}

func (r *Repo) dueChatCards(state, channel string, now time.Time, limit int, extra string) ([]model.ChatCard, error) {
	q := r.db.Where("state = ? AND next_at <= ? AND channel = ?", state, now, channel)
	if extra != "" {
		q = q.Where(extra)
	}
	var cs []model.ChatCard
	err := q.Order("next_at, id").Limit(limit).Find(&cs).Error
	return cs, err
}

// UpdateChatCard 改一张卡片,前提是它还处在 from 状态;返回是否真的改到了。
// 卡片会被后台扫描与按钮点击两处改,谁都不能把对方刚收场的卡片改回去。
func (r *Repo) UpdateChatCard(id int64, from string, fields map[string]any) (bool, error) {
	res := r.db.Model(&model.ChatCard{}).Where("id = ? AND state = ?", id, from).Updates(fields)
	return res.RowsAffected == 1, res.Error
}
