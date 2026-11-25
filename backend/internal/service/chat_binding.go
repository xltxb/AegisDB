package service

// 聊天账号绑定(.scratch/chat-approval-cards/PRD.md §5.1)。
//
// 网关不按邮箱猜"这个聊天账号是谁":本人登录后生成一次性绑定码,发给机器人,两边才连起来。
// 绑定是**比会话活得更久的凭据** —— 绑上的人不登录网关也能批单,所以生成码要过 MFA,绑定与解绑都
// 通知本人并记审计,账号停用、口令或 MFA 被改时自动解绑。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/chat"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
)

// 绑定码的拒绝理由,处理器原样透出。
var (
	ErrChatUnknown     = errors.New("没有这个审批机器人渠道")
	ErrChatOff         = errors.New("这个审批机器人没有开启")
	ErrChatNotReady    = errors.New("审批机器人还没连上,请稍后再试")
	ErrChatHumanOnly   = errors.New("服务账号不能绑定审批机器人")
	ErrChatUserChanged = errors.New("账号状态刚刚变化,请刷新后重试")
	ErrChatTooManyGens = errors.New("生成绑定码太频繁,请 15 分钟后再试")
)

const (
	chatCodeTTL      = 10 * time.Minute
	chatCodeWindow   = 15 * time.Minute
	chatCodeLockout  = 15 * time.Minute
	chatCodeMaxFails = 10 // 同一聊天账号一个窗口里错这么多次就锁
	chatCodeMaxGens  = 10 // 同一用户一个窗口里最多生成这么多次

	// 去掉容易看错的 0 / O、1 / I / L;31 个字符 10 位约 49.5 bit。
	chatCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	chatCodeLen      = 10

	chatIDMax = 64 // tbl_chat_binding 里标识列的宽度
)

// chatAccount 是一个聊天账号:标识只在那个渠道的那个机器人下有意义。
type chatAccount struct {
	ch             chat.Channel
	bot, accountID string
}

// SetChatTransport 换上一个渠道的实现;nil(包括带类型的 nil 指针)= 关闭这个渠道。「渠道开着」就是
// 这里有实现。
func (s *Services) SetChatTransport(ch chat.Channel, t chat.Transport) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if v := reflect.ValueOf(t); t == nil || v.Kind() == reflect.Pointer && v.IsNil() {
		delete(s.chatTransports, ch)
		return
	}
	if s.chatTransports == nil {
		s.chatTransports = map[chat.Channel]chat.Transport{}
	}
	s.chatTransports[ch] = t
}

// chatTransport 取一个渠道此刻的实现;关着返回 nil。
func (s *Services) chatTransport(ch chat.Channel) chat.Transport {
	s.chatMu.RLock()
	defer s.chatMu.RUnlock()
	return s.chatTransports[ch]
}

// chatBotID 是一个渠道当前机器人的标识;渠道关着或机器人还没就绪为空。
func (s *Services) chatBotID(ch chat.Channel) string {
	if t := s.chatTransport(ch); t != nil {
		return t.BotID()
	}
	return ""
}

// ChatBotName 是一个渠道当前机器人的名字;渠道关着为空。
func (s *Services) ChatBotName(ch chat.Channel) string {
	if t := s.chatTransport(ch); t != nil {
		return t.BotName()
	}
	return ""
}

// cardLang 是发到聊天里的文字用的语言(全局设置,默认 zh)。
func (s *Services) cardLang() string { return s.settingString("chat.cardLang", "zh") }

// ChatBindingView 是本人看到的一个渠道的绑定状态。聊天账号与机器人的标识不出接口。
type ChatBindingView struct {
	Channel string     `json:"channel"`
	BotName string     `json:"botName"`
	Bound   bool       `json:"bound"`
	BoundAt *time.Time `json:"boundAt,omitempty"`
}

// ChatBindings 列出开着的渠道与本人在上面的绑定;一个都没开是空列表。
func (s *Services) ChatBindings(userID int64) []ChatBindingView {
	out := []ChatBindingView{}
	for _, ch := range chat.Channels {
		t := s.chatTransport(ch)
		if t == nil {
			continue
		}
		v := ChatBindingView{Channel: string(ch), BotName: t.BotName()}
		if b := s.ChatBindingOf(userID, ch); b != nil {
			at := b.CreatedAt
			v.Bound, v.BoundAt = true, &at
		}
		out = append(out, v)
	}
	return out
}

// ChatBindingOf 取一个用户在某渠道上**此刻有效**的绑定:渠道开着,且绑定时的机器人就是当前这个。
// 换了机器人,旧绑定里的账号标识在新机器人下毫无意义,当作没绑。
func (s *Services) ChatBindingOf(userID int64, ch chat.Channel) *model.ChatBinding {
	bot := s.chatBotID(ch)
	if bot == "" {
		return nil
	}
	b, err := s.Repo.ChatBindingOfUser(userID, string(ch))
	if err != nil || b.BotID != bot {
		return nil
	}
	return b
}

// ChatUserOf 认出一个聊天账号是网关里的谁;没绑定、渠道关着、来自别的机器人都是 nil。库读不出来是
// 错误,不当成没绑定。
//
// 只回答"是谁",不判断他此刻还能不能批:在职、真人、菜单由调用方逐条查,好告诉对方卡在哪一条。
func (s *Services) ChatUserOf(ch chat.Channel, botID, accountID string) (*model.User, error) {
	if botID == "" || botID != s.chatBotID(ch) {
		return nil, nil
	}
	b, err := s.Repo.ChatBindingOfAccount(string(ch), botID, accountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u, err := s.Repo.GetUserByID(b.UserID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return u, err
}

// NewChatBindCode 给本人生成一个绑定码。u 是这个请求认出来的用户;mfaCode 只在他启用了 MFA 时要。
func (s *Services) NewChatBindCode(u *model.User, ch chat.Channel, mfaCode string) (code string, expires time.Time, err error) {
	if s.chatTransport(ch) == nil {
		return "", time.Time{}, ErrChatOff
	}
	if s.chatBotID(ch) == "" {
		return "", time.Time{}, ErrChatNotReady
	}
	if u.Kind == model.UserKindService {
		return "", time.Time{}, ErrChatHumanOnly
	}
	// 查与记是两步,同时发来的几个请求可能都过了这一查、多生成几个;上限只防滥用,不追求精确。
	if s.chatCodeGens.locked(u.ID, time.Now()) {
		return "", time.Time{}, ErrChatTooManyGens
	}
	if u.MFAEnabled && u.MFASecret != "" {
		if err := s.spendMFACode(u, mfaCode, "生成审批机器人绑定码"); err != nil {
			return "", time.Time{}, err
		}
	}
	s.chatCodeGens.fail(u.ID, time.Now()) // 借失败计数表数生成次数:够 chatCodeMaxGens 次就锁
	if code, err = newChatCode(); err != nil {
		return "", time.Time{}, err
	}
	hash := chatCodeHash(code)
	expires = time.Now().Add(chatCodeTTL)
	if err := s.Repo.SaveChatBindCode(u.ID, string(ch), hash, expires); err != nil {
		return "", time.Time{}, err
	}
	// 口令重置、停用、MFA 变更都会删掉已有的码,但这个请求可能更早就过了鉴权、在那之后才存下码。
	// 所以存完回头再看一眼,变了就收回。那边的顺序是:写凭据 / 状态 → 提升会话代次 → 删码
	// (revokeSessionsAndChat);这边是:鉴权读用户 → 存码 → 重读。于是
	//   - 存码在删码之前:那边删得到它;
	//   - 存码在删码之后:重读也在提升代次之后,而请求过鉴权时的代次是提升之前的(提升之后旧 token
	//     过不了鉴权),比得出来。
	// 只比凭据不够:夹在"写凭据"与"提升代次"之间过鉴权的旧会话,手里已经是新凭据。凭据与状态仍要
	// 比:AdminBindMFA 不提升代次。
	fresh, err := s.Repo.GetUserByID(u.ID)
	if err == nil && (fresh.TokenVersion != u.TokenVersion || fresh.Status != "active" || fresh.Kind != u.Kind ||
		fresh.PasswordHash != u.PasswordHash || fresh.MFAEnabled != u.MFAEnabled || fresh.MFASecret != u.MFASecret) {
		err = ErrChatUserChanged
	}
	if err != nil {
		if derr := s.Repo.DeleteChatBindCode(hash); derr != nil {
			slog.Error("收回绑定码失败", "user", u.ID, "err", derr)
		}
		return "", time.Time{}, err
	}
	return code, expires, nil
}

// RedeemChatBindCode 处理发给机器人的一条私聊文字,返回要回的话。botID / accountID / chatID 由渠道从
// 平台的事件里取。认「CODE」与「/start CODE」(Telegram 深链)两种写法,其余文字回用法说明。
func (s *Services) RedeemChatBindCode(ch chat.Channel, botID, accountID, chatID, text string) (reply string) {
	say := func(key string, args ...any) string { return chat.Tr(s.cardLang(), key, args...) }
	// 渠道刚关、机器人刚换时,旧连接上可能还有消息进来。
	if botID == "" || botID != s.chatBotID(ch) {
		return say("bind.off")
	}
	code, ok := parseChatCode(text)
	if !ok {
		return say("bind.usage")
	}
	who := chatAccount{ch, botID, accountID}
	now := time.Now()
	// 查与记是两步,并发的几条消息可能多试几次;码有约 49 bit,多这几次无碍(MFA 的计数同样如此)。
	if s.chatCodeFails.locked(who, now) {
		return say("bind.locked")
	}
	if accountID == "" || len(accountID) > chatIDMax || len(botID) > chatIDMax || len(chatID) > chatIDMax {
		slog.Error("聊天标识为空或过长,无法绑定", "channel", ch)
		return say("bind.failed")
	}
	u, err := s.Repo.RedeemChatBindCode(chatCodeHash(code), string(ch), botID, accountID, chatID, now)
	switch {
	case errors.Is(err, repository.ErrChatCodeInvalid):
		if s.chatCodeFails.fail(who, now) {
			slog.Warn("同一聊天账号绑定码连续输错,已锁定", "channel", ch, "minutes", int(chatCodeLockout/time.Minute))
		}
		return say("bind.invalid")
	case errors.Is(err, repository.ErrChatAccountTaken):
		return say("bind.taken")
	case err != nil:
		slog.Error("绑定聊天账号失败", "channel", ch, "err", err)
		return say("bind.failed")
	}
	// 这里没有 HTTP 请求给 u 盖来源,显式记 auth:这是本人给自己加了一把凭据。
	s.recordAuditSrc(model.AuditSourceAuth, u, nil, "chat.bind 绑定审批机器人 channel="+ch.Name()+" account="+chatAccountMark(accountID)+" user="+u.Email,
		model.RiskMid, model.ResultExecuted, "", "")
	s.notify(u.ID, model.NotifChatBound, apimsg.T("已绑定审批机器人"),
		apimsg.Sprintf("你的账号已绑定 %s 审批机器人,可以直接在聊天里审批。\n不是你本人操作的话,请立即在右上角用户菜单的「审批机器人」里解绑,并联系管理员。", ch.Name()), "")
	return say("bind.done", u.Name)
}

// UnbindChat 解除一个用户的聊天绑定(ch 为空 = 全部渠道),没用掉的绑定码一并作废。渠道关着、机器人
// 换过也照样解得掉。actor 是动手的人(审计来源取他这次请求的),why 是写给本人看的原因。
func (s *Services) UnbindChat(actor *model.User, userID int64, ch chat.Channel, why string) error {
	u, err := s.Repo.GetUserByID(userID)
	if err != nil {
		return ErrNotFound
	}
	gone, err := s.Repo.DeleteChatBindings(userID, string(ch))
	if err != nil {
		return err
	}
	for _, b := range gone {
		name := chat.Channel(b.Channel).Name()
		s.auditAdminAction(actor, "chat.unbind 解除审批机器人绑定 channel="+name+" account="+chatAccountMark(b.AccountID)+" user="+u.Email+" 原因:"+why)
		s.notify(userID, model.NotifChatUnbound, apimsg.T("审批机器人已解绑"),
			apimsg.Sprintf("你的 %s 审批机器人绑定已解除:%s。", name, apimsg.Of(why)), "")
	}
	return nil
}

// revokeSessionsAndChat 在改完凭据之后作废会话并解绑聊天账号。
//
// 顺序不能换:先提升会话代次,再解绑。反过来的话,解绑之后、代次提升之前旧会话还过得了鉴权,能再
// 生成一个码、绑上去,活过这次重置。提升失败也照样解绑(凭据已经换了),错误交给调用方。
func (s *Services) revokeSessionsAndChat(actor *model.User, userID int64, why string) error {
	err := s.Repo.BumpTokenVersion(userID)
	s.autoUnbindChat(actor, userID, why)
	return err
}

// autoUnbindChat 在账号的凭据或状态变了之后解绑。要作废会话的,排在作废之后;失败只记日志 —— 重置口令、停用账号本身不能因为解绑不成而失败。
func (s *Services) autoUnbindChat(actor *model.User, userID int64, why string) {
	if err := s.UnbindChat(actor, userID, "", why); err != nil {
		slog.Error("自动解绑审批机器人失败,该账号的聊天绑定可能仍在", "user", userID, "why", why, "err", err)
	}
}

// parseChatCode 从一条私聊文字里取出绑定码。
//
// "像不像绑定码"决定它算不算一次输错:去掉可选的 /start 与首尾空白之后,恰好 10 位、每一位都在码的
// 字符集里(不分大小写),且字母数字都有 —— 生成时保证了后一条(newChatCode),所以一个十个字母的
// 单词、一串电话号码都只是闲聊。凡是不符合的,本来就不可能对上任何一个码,不计数也不给猜码的人便宜。
func parseChatCode(text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 2 && strings.EqualFold(fields[0], "/start") {
		fields = fields[1:]
	}
	if len(fields) != 1 {
		return "", false
	}
	code := strings.ToUpper(fields[0])
	return code, chatCodeShaped(code)
}

func chatCodeShaped(code string) bool {
	if len(code) != chatCodeLen {
		return false
	}
	for i := 0; i < len(code); i++ {
		if strings.IndexByte(chatCodeAlphabet, code[i]) < 0 {
			return false
		}
	}
	return strings.ContainsAny(code, "23456789") && strings.Trim(code, "23456789") != ""
}

// newChatCode 随机出一个绑定码。不含数字或不含字母的重抽(约 5%),只让熵少不到 0.1 bit。
func newChatCode() (string, error) {
	// 31×8=248:再往上的字节取模会偏,丢掉重取。
	const limit = 256 - 256%len(chatCodeAlphabet)
	for {
		code := make([]byte, 0, chatCodeLen)
		buf := make([]byte, 1)
		for len(code) < chatCodeLen {
			if _, err := rand.Read(buf); err != nil {
				return "", err
			}
			if int(buf[0]) < limit {
				code = append(code, chatCodeAlphabet[int(buf[0])%len(chatCodeAlphabet)])
			}
		}
		if chatCodeShaped(string(code)) {
			return string(code), nil
		}
	}
}

// chatAccountMark 是聊天账号在审计里的短标记:同一个账号前后对得上,又不写出平台的账号标识。
func chatAccountMark(accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	return hex.EncodeToString(sum[:])[:12]
}

func chatCodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
