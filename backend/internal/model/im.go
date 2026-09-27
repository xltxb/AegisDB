package model

import "time"

// IMIdentity 把一个 IM 平台账号绑定到一个网关账户。审批卡片上的点击只凭这张表认人。
type IMIdentity struct {
	UserID         int64     `gorm:"primaryKey;autoIncrement:false" json:"userId"`
	Channel        string    `gorm:"primaryKey;size:16" json:"channel"`
	PlatformUserID string    `gorm:"size:128;not null" json:"platformUserId"`
	BoundBy        string    `gorm:"size:16;not null" json:"boundBy"` // self | admin
	CreatedAt      time.Time `json:"createdAt"`
}

func (IMIdentity) TableName() string { return "tbl_im_identity" }

const (
	IMBoundSelf  = "self"
	IMBoundAdmin = "admin"
)

// IMBindCode 是一次性绑定码。只存哈希。
type IMBindCode struct {
	UserID    int64     `gorm:"primaryKey;autoIncrement:false"`
	Channel   string    `gorm:"primaryKey;size:16"`
	CodeHash  string    `gorm:"size:64;not null"`
	ExpiresAt time.Time `gorm:"not null"`
}

func (IMBindCode) TableName() string { return "tbl_im_bind_code" }

// IMCard 是一张已经私信出去的审批卡片。
type IMCard struct {
	ID         int64  `gorm:"primaryKey;autoIncrement"`
	ApprovalID int64  `gorm:"not null"`
	Channel    string `gorm:"size:16;not null"`
	UserID     int64  `gorm:"not null"`
	MsgRef     string `gorm:"size:256;not null"`
	Closed     bool   `gorm:"not null;default:false"`
	CreatedAt  time.Time
}

func (IMCard) TableName() string { return "tbl_im_card" }
