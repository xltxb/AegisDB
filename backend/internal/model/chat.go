package model

import "time"

// ChatBinding 把一个网关账号和一个聊天账号绑在一起(迁移 0065,.scratch/chat-approval-cards)。
//
// 每个渠道一个用户最多一个绑定,一个聊天账号最多绑一个用户。BotID 记下绑定时的机器人 / 应用:
// 聊天账号标识只在那个机器人下有意义,机器人换了旧绑定自然失效。
type ChatBinding struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    int64     `gorm:"not null;uniqueIndex:uk_chat_binding_user,priority:2" json:"userId"`
	Channel   string    `gorm:"size:16;not null;uniqueIndex:uk_chat_binding_user,priority:1;uniqueIndex:uk_chat_binding_account,priority:1" json:"channel"`
	BotID     string    `gorm:"column:bot_id;size:64;not null;uniqueIndex:uk_chat_binding_account,priority:2" json:"-"`
	AccountID string    `gorm:"column:account_id;size:64;not null;uniqueIndex:uk_chat_binding_account,priority:3" json:"-"`
	ChatID    string    `gorm:"column:chat_id;size:64;not null;default:''" json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

func (ChatBinding) TableName() string { return "tbl_chat_binding" }

// ChatBindCode 是一次性绑定码。库里只有摘要(CodeHash,SHA-256 的十六进制),码本身只显示给本人一次。
// 一个用户每个渠道一行:重新生成是改写这一行,所以旧码必然作废,行也不会越积越多。
type ChatBindCode struct {
	ID        int64      `gorm:"primaryKey;autoIncrement" json:"-"`
	UserID    int64      `gorm:"not null;uniqueIndex:uk_chat_bind_code_user,priority:2" json:"-"`
	Channel   string     `gorm:"size:16;not null;uniqueIndex:uk_chat_bind_code_user,priority:1" json:"-"`
	CodeHash  string     `gorm:"column:code_hash;size:64;not null;uniqueIndex:uk_chat_bind_code" json:"-"`
	ExpiresAt time.Time  `gorm:"not null" json:"-"`
	UsedAt    *time.Time `json:"-"`
}

func (ChatBindCode) TableName() string { return "tbl_chat_bind_code" }

// ChatCard 是要发给一个人的一张审批卡片:一张单、每人、每渠道一行。
//
// 先落库、后台发:建单不等它,也不因它失败;网关重启后接着发。
type ChatCard struct {
	ID         int64  `gorm:"primaryKey;autoIncrement" json:"-"`
	ApprovalID int64  `gorm:"not null;uniqueIndex:uk_chat_card,priority:1" json:"-"`
	UserID     int64  `gorm:"not null;uniqueIndex:uk_chat_card,priority:2" json:"-"`
	Channel    string `gorm:"size:16;not null;uniqueIndex:uk_chat_card,priority:3" json:"-"`
	BotID      string `gorm:"column:bot_id;size:64;not null;default:''" json:"-"`
	State      string `gorm:"size:16;not null;default:queued;index:idx_chat_card_state,priority:1" json:"-"` // ChatCard* 常量
	ChatID     string `gorm:"column:chat_id;size:64;not null;default:''" json:"-"`
	MessageID  string `gorm:"column:message_id;size:128;not null;default:''" json:"-"`
	// TokenHash 是按钮令牌的 SHA-256(十六进制);还没发过的卡片为空。
	TokenHash string `gorm:"column:token_hash;size:64;not null;default:'';index:idx_chat_card_token" json:"-"`
	// Partial:发出去的那张没显示全文,发卡时定下。SourceTruncated:单子本身存的就是截断内容,建单时定下。
	Partial         bool      `gorm:"not null;default:false" json:"-"`
	SourceTruncated bool      `gorm:"column:source_truncated;not null;default:false" json:"-"`
	Attempts        int       `gorm:"not null;default:0" json:"-"`
	NextAt          time.Time `gorm:"not null;index:idx_chat_card_state,priority:2" json:"-"`
	LastError       string    `gorm:"column:last_error;size:255;not null;default:''" json:"-"`
	CreatedAt       time.Time `json:"-"`
	UpdatedAt       time.Time `json:"-"`
}

func (ChatCard) TableName() string { return "tbl_chat_card" }

// ChatCard.State
const (
	ChatCardQueued = "queued" // 待发
	ChatCardSent   = "sent"   // 已发出,单子还在等
	ChatCardClosed = "closed" // 收场:已写上结论,或不必再发 / 再更新
	ChatCardFailed = "failed" // 重试用完
)
