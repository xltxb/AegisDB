package service

// 机器人收到的私聊文字与卡片按钮的点击(.scratch/chat-approval-cards/PRD.md §5.4)。
//
// 一次点击批的是生产变更,而它没有登录态:身份全靠「这张卡片的令牌」加「平台说是谁点的」两样对上。
// 规则与站内同一处(DecideBlockFor),决定走同一个核心(DecideApprovalVia);这里没有任何一条路会执行
// 命令(ADR 0010)。

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"

	"gorm.io/gorm"

	"aegisdb/internal/chat"
	"aegisdb/internal/model"
)

var _ chat.Handler = (*Services)(nil)

// OnText 处理一条私聊文字:只有兑换绑定码这一件事。
func (s *Services) OnText(_ context.Context, ch chat.Channel, botID, accountID, chatID, text string) string {
	return s.RedeemChatBindCode(ch, botID, accountID, chatID, text)
}

// OnClick 处理一次按钮点击。
//
// 认不出点击者之前不透露任何东西:没绑定的账号一律同一句,绑定了但卡片不是他的与令牌根本不存在也是
// 同一句 —— 谁都不能拿它来试某个令牌对不对、那张单在不在。认出是收件人之后,卡在哪一条就说哪一条。
func (s *Services) OnClick(_ context.Context, c chat.Click) chat.ClickResult {
	lang := s.cardLang()
	say := func(key string) chat.ClickResult { return chat.ClickResult{Toast: chat.Tr(lang, key)} }
	retry := func(err error) chat.ClickResult {
		slog.Error("处理卡片点击失败", "channel", c.Channel, "err", err)
		return say("click.retry")
	}
	// 渠道刚关、机器人刚换时,旧连接上可能还有点击进来。
	if c.BotID == "" || c.BotID != s.chatBotID(c.Channel) {
		return say("click.off")
	}
	switch c.Action {
	case chat.ActTest:
		return chat.ClickResult{OK: true, Toast: chat.Tr(lang, "click.test")}
	case chat.ActApprove, chat.ActConfirm, chat.ActBack, chat.ActReject:
	default:
		return say("click.invalid")
	}
	if !chatTokenShaped(c.Token) {
		return say("click.invalid")
	}

	row, err := s.Repo.ChatCardByTokenHash(chatCodeHash(c.Token))
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return retry(err)
	}
	if err != nil || row.BotID != c.BotID || row.Channel != string(c.Channel) {
		row = nil
	}
	u, err := s.ChatUserOf(c.Channel, c.BotID, c.AccountID)
	if err != nil {
		return retry(err)
	}
	if u == nil {
		return say("click.unbound")
	}
	if row == nil {
		return say("click.invalid")
	}
	// 有身份却没资格的点击值得留一行;认不出来的不记,不然谁都能刷日志。任何一行都不带令牌。
	refuse := func(key string) chat.ClickResult {
		slog.Warn("卡片点击被拒", "channel", c.Channel, "user", u.ID, "card", row.ID, "why", key)
		return say(key)
	}
	if row.UserID != u.ID {
		return refuse("click.invalid")
	}
	if u.Status != "active" {
		return refuse("click.inactive")
	}
	if u.Kind == model.UserKindService {
		return refuse("click.notHuman")
	}
	switch ok, err := s.hasMenu(u, "approve"); {
	case err != nil:
		return retry(err)
	case !ok:
		return refuse("click.noMenu")
	}

	ap, err := s.Repo.GetApproval(row.ApprovalID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return say("click.invalid")
	}
	if err != nil {
		return retry(err)
	}
	handled := func(ap *model.Approval) chat.ClickResult {
		res := say("click.handled")
		res.Card = s.outcomeCard(ap, row)
		return res
	}
	if ap.Status != model.StatusPending {
		return handled(ap)
	}
	switch row.State {
	case model.ChatCardSent, model.ChatCardQueued, model.ChatCardFailed:
		// 后两种也算:消息送到了、应答却丢了,库里就停在「待发」或「失败」,而按钮在人家手里。
	default:
		return say("click.invalid")
	}
	block, err := s.decideBlock(u, ap)
	if err != nil {
		return retry(err)
	}
	if block != BlockNone {
		return refuse(chatBlockKey(block))
	}

	if c.Action == chat.ActApprove || c.Action == chat.ActBack {
		// 库里只有令牌的摘要,换上去的卡片接着用点击带来的这一个。
		card := s.sentChatCard(ap, row)
		card.Token, card.Confirming = c.Token, c.Action == chat.ActApprove
		return chat.ClickResult{OK: true, Card: &card}
	}

	approve, done := c.Action == chat.ActConfirm, "click.rejected"
	if approve {
		done = "click.approved"
	}
	_, err = s.DecideApprovalVia(u, ap.ID, approve, string(c.Channel), c.Reason)
	var refusal *DecideRefusal
	switch {
	case errors.Is(err, ErrAlreadyDecided):
		// 结论要重读:手里那份还是待审批。重读仍是待审批,说明是一张旧窗口单、而核心没能撤掉它。
		now, rerr := s.Repo.GetApproval(ap.ID)
		if rerr == nil && now.Status == model.StatusPending {
			rerr = err
		}
		if rerr != nil {
			return retry(rerr)
		}
		return handled(now)
	case errors.As(err, &refusal):
		return refuse(chatBlockKey(refusal.Block))
	case err != nil:
		return retry(err)
	}
	// 决定已经写下。结论卡片画不出来也照样说成功:卡片由扫描去收敛。
	res := say(done)
	res.OK = true
	if now, err := s.Repo.GetApproval(ap.ID); err != nil {
		slog.Error("读审批单失败", "card", row.ID, "err", err)
	} else {
		res.Card = s.outcomeCard(now, row)
	}
	return res
}

// hasMenu 报告一个用户此刻有没有某个菜单,与 middleware.MenuGuard 同一口径。
func (s *Services) hasMenu(u *model.User, key string) (bool, error) {
	// EffectiveRoleIDs 读不出成员关系时只算主角色。那样只会少给、不会多给,但「库暂时读不出来」会被
	// 说成「没有权限」,所以先单独读一次。
	if _, err := s.Repo.RoleIDsOfUser(u.ID); err != nil {
		return false, err
	}
	menus, err := s.Repo.MenusForRoles(s.Repo.EffectiveRoleIDs(u))
	return menus[key], err
}

// outcomeCard 是一张已不在待审批的单的结论卡片,没有令牌也就没有按钮;结论读不出来返回 nil。
func (s *Services) outcomeCard(ap *model.Approval, row *model.ChatCard) *chat.Card {
	card := s.sentChatCard(ap, row)
	var err error
	if card.Outcome, _, err = s.chatOutcome(ap, card.Lang); err != nil {
		slog.Error("读审批结论失败", "card", row.ID, "err", err)
		return nil
	}
	return &card
}

// chatBlockKey 是一条拒绝理由在卡片文案表里的键。
func chatBlockKey(b DecideBlock) string {
	switch b {
	case BlockSelf:
		return "click.self"
	case BlockNotPending:
		return "click.handled"
	}
	return "click.notChain"
}

// chatTokenShaped 报告一个串像不像 newChatToken 的产物;不像的不必去查库。
func chatTokenShaped(token string) bool {
	b, err := base64.RawURLEncoding.DecodeString(token)
	// 解码会跳过换行,所以长度也要对得上。
	return err == nil && len(b) == chatTokenBytes && len(token) == base64.RawURLEncoding.EncodedLen(chatTokenBytes)
}
