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
	"time"

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
	var cardID int64
	stale := "unknown token"
	if err == nil {
		cardID = row.ID
	}
	if err != nil {
		row = nil
	} else if row.BotID != c.BotID || row.Channel != string(c.Channel) {
		row, stale = nil, "other bot"
	}
	u, err := s.ChatUserOf(c.Channel, c.BotID, c.AccountID)
	if err != nil {
		return retry(err)
	}
	if u == nil {
		return say("click.unbound")
	}
	// 有身份却没资格的点击值得留一行;认不出来的不记,不然谁都能刷日志。任何一行都不带令牌。
	log := func(why string) {
		if s.chatRefusals.fail(chatRefusal{u.ID, why}, time.Now()) {
			slog.Warn("卡片点击被拒", "channel", c.Channel, "user", u.ID, "card", cardID, "why", why)
		}
	}
	refuse := func(key string) chat.ClickResult {
		log(key)
		return say(key)
	}
	if row == nil {
		log(stale)
		return say("click.invalid")
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
		log("closed card")
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
		// 画好了再让扫描等:从这里到返回不再读库,等的时限才算得准。
		now, err := s.holdChatCard(row)
		if err != nil {
			return retry(err)
		}
		if now.Status != model.StatusPending {
			return handled(now)
		}
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
	// 角色读不全就不往下算:少了成员关系会把「库暂时读不出来」说成「没有权限」,角色表读不出来则会把已经
	// 删掉的角色也算上,多给权限。
	roles, err := s.Repo.EffectiveRoles(u)
	if err != nil {
		return false, err
	}
	menus, err := s.Repo.MenusForRoles(roles)
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

// chatRefusal 是被拒点击的日志的频控键:同一个人、同一种原因。
type chatRefusal struct {
	user int64
	why  string
}

// chatAnswerSlack 是扫描在 chat.CardWithin 之外多等的:留给这里读库,以及平台晚一点才落定的更新。
// 变量只为测试能缩短它。
var chatAnswerSlack = 5 * time.Second

// holdChatCard 让扫描等渠道把二次确认的应答换上去之后,再去收敛这张卡片;返回此刻的单子。
//
// 应答里的卡片带着按钮,而它到平台的那一刻单子可能已经有了结论。扫描若先写下结论并收场,晚到的按钮就盖在
// 上面,再没人去改。顺序不能换:先推迟,再读单子 —— 读到仍待审批,单子离开待审批就在推迟之后,那以后的
// 扫描都看得到这个推迟;读到已有结论,就不给按钮。
func (s *Services) holdChatCard(row *model.ChatCard) (*model.Approval, error) {
	began := time.Now()
	// 只有已发出的会被收敛;没改到(刚被扫描收场)不要紧,下面会读到结论。
	if _, err := s.Repo.UpdateChatCard(row.ID, model.ChatCardSent, map[string]any{"next_at": began.Add(chat.CardWithin + chatAnswerSlack)}); err != nil {
		return nil, err
	}
	ap, err := s.Repo.GetApproval(row.ApprovalID)
	if err == nil && time.Since(began) > chatAnswerSlack/2 {
		return nil, errors.New("too slow to answer")
	}
	return ap, err
}
