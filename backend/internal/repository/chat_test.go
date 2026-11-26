package repository

import (
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
)

func newChatRepo(t *testing.T) *Repo {
	t.Helper()
	return newApprovalRepo(t)
}

// 决定的渠道与原因和状态在同一条 UPDATE 里写:抢输的一方一个字段都碰不到赢家的行。
func TestClaimApprovalDecision_LoserTouchesNothing(t *testing.T) {
	r := newApprovalRepo(t)
	ap := &model.Approval{ApNo: "AP-1", Status: model.StatusPending, InitiatorID: 9}
	if err := r.CreateApproval(ap, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.ClaimApprovalDecision(ap.ID, model.StatusRejected, "lark", "影响大促"); err != nil || !ok {
		t.Fatalf("第一次决定应成功: %v %v", ok, err)
	}
	if ok, err := r.ClaimApprovalDecision(ap.ID, model.StatusApproved, "console", ""); err != nil || ok {
		t.Fatalf("第二次决定应落空: %v %v", ok, err)
	}
	got, _ := r.GetApproval(ap.ID)
	if got.Status != model.StatusRejected || got.DecidedVia != "lark" || got.RejectReason != "影响大促" {
		t.Errorf("赢家的行被改了: %s / %q / %q", got.Status, got.DecidedVia, got.RejectReason)
	}
}

// 绑定:每个渠道一个用户最多一个,一个聊天账号最多绑一个用户;换了机器人是另一个账号空间。
func TestChatBinding_Uniqueness(t *testing.T) {
	r := newChatRepo(t)
	add := func(user int64, ch, bot, account string) error {
		return r.db.Create(&model.ChatBinding{UserID: user, Channel: ch, BotID: bot, AccountID: account, ChatID: "c"}).Error
	}
	if err := add(1, "lark", "cli_a", "ou_1"); err != nil {
		t.Fatal(err)
	}
	for what, err := range map[string]error{
		"同一用户同一渠道第二个绑定": add(1, "lark", "cli_a", "ou_2"),
		"同一聊天账号绑第二个用户":  add(2, "lark", "cli_a", "ou_1"),
	} {
		if err == nil {
			t.Errorf("%s:应被唯一索引拒绝", what)
		}
	}
	for what, err := range map[string]error{
		"同一用户另一个渠道":    add(1, "telegram", "42", "100"),
		"另一个机器人下的同名账号": add(3, "lark", "cli_b", "ou_1"),
		"同一机器人下另一个人":   add(4, "lark", "cli_a", "ou_4"),
	} {
		if err != nil {
			t.Errorf("%s:应允许,实际 %v", what, err)
		}
	}

	if b, err := r.ChatBindingOfUser(1, "lark"); err != nil || b.AccountID != "ou_1" {
		t.Errorf("按用户查绑定: %+v %v", b, err)
	}
	if _, err := r.ChatBindingOfUser(2, "lark"); err == nil {
		t.Error("没绑定的用户不该查到绑定")
	}
	if b, err := r.ChatBindingOfAccount("lark", "cli_b", "ou_1"); err != nil || b.UserID != 3 {
		t.Errorf("按账号查绑定: %+v %v", b, err)
	}
	for _, account := range []string{"", "ou_9", "OU_1"} {
		if _, err := r.ChatBindingOfAccount("lark", "cli_a", account); err == nil {
			t.Errorf("账号 %q 不该查到绑定", account)
		}
	}
}

func TestChatBindCode_DigestIsUnique(t *testing.T) {
	r := newChatRepo(t)
	hash := strings.Repeat("a", 64)
	mk := func() error {
		return r.db.Create(&model.ChatBindCode{UserID: 1, Channel: "lark", CodeHash: hash, ExpiresAt: time.Now()}).Error
	}
	if err := mk(); err != nil {
		t.Fatal(err)
	}
	if mk() == nil {
		t.Error("同一个摘要不该能存两次")
	}
}

// 卡片:一张单每人每渠道一行。排队中的卡片还没有令牌(摘要为空),空摘要绝不能对上任何一张卡片 ——
// 否则一个不带令牌的点击就能落到别人排队中的卡片上。
func TestChatCard_TokenLookup(t *testing.T) {
	r := newChatRepo(t)
	hash := strings.Repeat("b", 64)
	add := func(c model.ChatCard) error { c.Channel, c.NextAt = "lark", time.Now(); return r.db.Create(&c).Error }
	if err := add(model.ChatCard{ApprovalID: 1, UserID: 1, State: model.ChatCardQueued}); err != nil {
		t.Fatal(err)
	}
	if err := add(model.ChatCard{ApprovalID: 1, UserID: 2, State: model.ChatCardSent, TokenHash: hash}); err != nil {
		t.Fatal(err)
	}
	if add(model.ChatCard{ApprovalID: 1, UserID: 1, State: model.ChatCardQueued}) == nil {
		t.Error("同一张单、同一人、同一渠道不该有两张卡片")
	}
	if err := r.db.Create(&model.ChatCard{ApprovalID: 1, UserID: 1, Channel: "telegram", State: model.ChatCardQueued, NextAt: time.Now()}).Error; err != nil {
		t.Errorf("同一张单、同一人、另一个渠道应有自己的卡片: %v", err)
	}
	if c, err := r.ChatCardByTokenHash(hash); err != nil || c.UserID != 2 {
		t.Errorf("按令牌查卡片: %+v %v", c, err)
	}
	for _, h := range []string{"", strings.Repeat("c", 64)} {
		if c, err := r.ChatCardByTokenHash(h); err == nil {
			t.Errorf("摘要 %q 不该查到卡片,查到了 %+v", h, c)
		}
	}
}
