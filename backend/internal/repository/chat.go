package repository

import (
	"gorm.io/gorm"

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
