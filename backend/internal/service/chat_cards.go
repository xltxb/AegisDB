package service

// 审批卡片的排队、发送与收敛(.scratch/chat-approval-cards/PRD.md §5.2、§5.3、§5.5)。
//
// 先落库、后台发:建单时只给每个收件人每个渠道写一行 queued,真正的发送与更新都在 SweepChatCards 里。
// 渠道慢、断、恒失败,建单、站内审批、超时扫描都不受影响;重启之后照着库里的行接着做。
//
// 收敛不在「离开待审批」的每条路上挂钩子,而是扫「已发出、单子已不在待审批」的行:无论那张单从哪条
// 路离开(包括仓储层直接改状态的),都会被看见。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	"aegisdb/internal/chat"
	"aegisdb/internal/model"
	"aegisdb/pkg/sqlutil"
)

const (
	chatMaxAttempts = 5 // 发送与收敛各自最多这么多次
	// 一轮扫描在一个渠道上、发送与收敛各自最多处理这么多张、这么久;剩下的下一轮接着来。
	chatSweepBatch  = 50
	chatSweepBudget = 15 * time.Second

	chatStatementMax   = 3000 // 卡片上语句最多的字数(rune);平台自己更紧的限制由渠道处理
	chatErrWidth       = 255  // tbl_chat_card.last_error 的列宽
	chatTokenBytes     = 24   // 192 位;base64url 后 32 个字符,连动作一起放得进 Telegram 的 64 字节
	chatDecisionSettle = 5 * time.Second
)

// chatBackoff 是第 n 次失败之后等多久再试。
var chatBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second, 135 * time.Second}

// chatCallTimeout 是一次渠道调用的时限;变量只为测试能缩短它。
var chatCallTimeout = 10 * time.Second

var errChatPanic = errors.New("chat transport panicked")

// ---------------------------------------------------------------- 排队

// queueChatCards 给一张刚建好的单排卡片。要在单子与步骤都落库之后调。
//
// 这里不碰渠道(连「当前机器人是谁」都不问),只读写自己的库;任何失败只记日志 —— 建单不等它,也不因它
// 失败。绑定是不是当前机器人的、收件人还在不在职,发送那一刻再判。
func (s *Services) queueChatCards(ap *model.Approval, sourceTruncated bool) {
	if ap == nil || ap.Status != model.StatusPending {
		return
	}
	var open []string
	for _, ch := range chat.Channels {
		if s.chatTransport(ch) != nil {
			open = append(open, string(ch))
		}
	}
	if len(open) == 0 {
		return
	}
	steps, err := s.Repo.StepsOf(ap.ID)
	if err != nil {
		slog.Error("审批卡片排队失败", "apNo", ap.ApNo, "err", err)
		return
	}
	var ids []int64
	for _, st := range steps {
		if st.ApproverID > 0 {
			ids = append(ids, st.ApproverID)
		}
	}
	bindings, err := s.Repo.ChatBindingsOfUsers(ids, open)
	if err != nil {
		slog.Error("审批卡片排队失败", "apNo", ap.ApNo, "err", err)
		return
	}
	var cards []model.ChatCard
	now := time.Now()
	for _, b := range bindings {
		ok, err := s.chatMayReceive(ap, b.UserID)
		if err != nil {
			slog.Error("审批卡片排队失败", "apNo", ap.ApNo, "user", b.UserID, "err", err)
			continue
		}
		if ok {
			cards = append(cards, model.ChatCard{ApprovalID: ap.ID, UserID: b.UserID, Channel: b.Channel, BotID: b.BotID,
				State: model.ChatCardQueued, SourceTruncated: sourceTruncated, NextAt: now})
		}
	}
	if err := s.Repo.QueueChatCards(cards); err != nil {
		slog.Error("审批卡片排队失败", "apNo", ap.ApNo, "err", err)
	}
}

// chatMayReceive 报告链上的这个人此刻该不该收到这张单的卡片:在职、真人、不是发起人(开了自审除外)。
// 他在不在链上不用再问 —— 链建单时就定下了。
func (s *Services) chatMayReceive(ap *model.Approval, userID int64) (bool, error) {
	u, err := s.Repo.GetUserByID(userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if u.Status != "active" || u.Kind == model.UserKindService {
		return false, nil
	}
	return u.ID != ap.InitiatorID || s.settingBool("approval.allowSelfApprove", false), nil
}

// ---------------------------------------------------------------- 内容

// chatShowsSQL 报告一个渠道的卡片带不带语句与原因。Telegram 默认不带(语句会留在它的服务器上)。
func chatShowsSQL(ch chat.Channel, includeSQL bool) bool { return ch != chat.Telegram || includeSQL }

// buildChatCard 画出一张单的卡片,不含令牌与结论。同样的参数永远画出同样的内容,所以发出之后可以照着
// 重画(更新、二次确认、返回);includeSQL 只对 Telegram 有意义。
//
// 不带 SQL 的卡片只留固定几项:语句、原因、库名、提示、发布单的标题与正文都可能带出业务内容,一样不留。
func (s *Services) buildChatCard(ap *model.Approval, ch chat.Channel, lang string, sourceTruncated, includeSQL bool) chat.Card {
	withSQL := chatShowsSQL(ch, includeSQL)
	kind := chatKind(ap)
	c := chat.Card{
		Title: chat.Tr(lang, "title."+kind) + " · " + ap.ApNo,
		Risk:  ap.RiskLevel, Lang: lang, DetailURL: s.chatDetailURL(),
		// 在线变更单:站内审批页上还有数据量与前置检查的提示,卡片上没有。
		Partial: !withSQL || sourceTruncated || ap.ScriptUploadID > 0 || ap.OSCJobID > 0,
	}
	add := func(key, value string) {
		c.Fields = append(c.Fields, chat.Field{Label: chat.Tr(lang, "field."+key), Value: value})
	}
	add("no", ap.ApNo)
	add("type", chat.Tr(lang, "kind."+kind))
	add("instance", ap.Instance)
	if withSQL && ap.Database != "" {
		add("database", ap.Database)
	}
	add("env", strings.ToUpper(ap.Env))
	if withSQL && ap.TierCode != "" {
		add("tier", strings.ToUpper(ap.TierCode))
	}
	add("risk", chatRiskLabel(lang, ap.RiskLevel))
	add("initiator", ap.Initiator)
	if ap.OSCJobID > 0 {
		add("method", chat.Tr(lang, "method.osc"))
	}
	if withSQL {
		if ap.Notice != "" {
			add("notice", chatRefText(lang, ap.NoticeRef, ap.Notice))
		}
		// 先遮口令再截断,理由见 safeClip。
		full := sqlutil.RedactSecrets(ap.Command)
		c.Statement = clipColumn(full, chatStatementMax)
		c.Partial = c.Partial || utf8.RuneCountInString(full) > chatStatementMax
		c.Reason = sqlutil.RedactSecrets(ap.Reason)
		return c
	}
	switch {
	case ap.WindowID > 0:
		// 窗口名与时间段取自建单时存下的结构(CommandRef),不取那句拼了理由的描述,也不现读窗口:
		// 窗口可能刚被改过,这张单批的仍是当时那一版。
		if ref := ap.CommandRef; ref != nil && len(ref.Parts) > 0 {
			add("window", ref.Args["name"])
			add("schedule", chatRefText(lang, &ref.Parts[0], model.RenderApText(&ref.Parts[0])))
		}
	case ap.ExportJobID > 0:
		what := "export.raw"
		if exportMasked(ap) {
			what = "export.masked"
		}
		add("export", chat.Tr(lang, what))
	}
	return c
}

// sentChatCard 重画一张已经发出的卡片。带没带 SQL、是不是全文,以发出那一刻记在行上的为准:之后改设置,
// 既不能把语句补发到一张没带语句的卡片上,也不能让卡片改口。语言跟当前设置走(整张一起换)。
func (s *Services) sentChatCard(ap *model.Approval, row *model.ChatCard) chat.Card {
	c := s.buildChatCard(ap, chat.Channel(row.Channel), s.cardLang(), row.SourceTruncated, row.WithSQL)
	c.Partial = row.Partial
	return c
}

func chatKind(ap *model.Approval) string {
	switch model.ApprovalType(ap) {
	case model.ApTypeRelease:
		return "release"
	case model.ApTypeWindow:
		return "window"
	case model.ApTypeExport:
		return "export"
	}
	return "command"
}

func chatRiskLabel(lang, level string) string {
	switch level {
	case model.RiskHigh, model.RiskMid, model.RiskLow:
		return chat.Tr(lang, "risk."+level)
	}
	return level
}

// chatRefArgs 是卡片上会出现的系统句子(chat 文案表的 ref.* 键)的参数,按占位符的顺序。
var chatRefArgs = map[string][]string{
	model.WinOnce:         {"start", "end"},
	model.WinOnceUnset:    {},
	model.WinDaily:        {"start", "end", "tz"},
	model.WinWeekly:       {"days", "start", "end", "tz"},
	model.WinUnknownModel: {},
	ruleOSCBigDirect:      {"table", "rows", "size", "why"},
}

// chatRefText 把单子上存的一句系统的话用卡片语言再说一遍;没有机器可读形式、或不认识的,用单子上存的
// 那句中文(canonical)。参数是数据,原样带出。
func chatRefText(lang string, ref *model.RuleRef, canonical string) string {
	if ref == nil {
		return canonical
	}
	names, ok := chatRefArgs[ref.Code]
	if !ok {
		return canonical
	}
	args := make([]any, len(names))
	for i, n := range names {
		args[i] = ref.Args[n]
	}
	return chat.Tr(lang, "ref."+ref.Code, args...)
}

// chatDetailURL 是「查看详情」指向的地址;没配 notify.consoleURL 为空。与群卡片同一个落点。
func (s *Services) chatDetailURL() string {
	base := strings.TrimRight(strings.TrimSpace(s.settingString("notify.consoleURL", "")), "/")
	if base == "" {
		return ""
	}
	return base + "/#/approvals"
}

// chatOutcome 是一张已不在待审批的单的结论。只看单子的状态与记下决定的那一步,不看 decided_via:
// 通过之后又撤回的单,那一列还留着当初那次通过的渠道。
//
// settled 为假:状态已改,谁决定的还没记下(finalizeApproval 先抢状态、后记步骤),这时的文字没有名字。
func (s *Services) chatOutcome(ap *model.Approval, lang string) (text string, settled bool, err error) {
	switch ap.Status {
	case model.StatusApproved, model.StatusRejected:
		steps, err := s.Repo.StepsOf(ap.ID)
		if err != nil {
			return "", false, err
		}
		for _, st := range steps {
			if st.Status == ap.Status && st.ActedAt != nil {
				return chat.Tr(lang, "outcome."+ap.Status, st.Approver, st.ActedAt.Local().Format("01-02 15:04")), true, nil
			}
		}
		return chat.Tr(lang, "outcome."+ap.Status+"Bare"), false, nil
	case model.StatusCancelled:
		// 窗口改过之后另起了新单,旧单是被顶替的,没有人撤回过它。窗口删了则是一并撤回(站内同一口径)。
		if ap.WindowID > 0 {
			w, err := s.Repo.GetExecWindow(ap.WindowID)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return "", false, err // 读不出来不等于删了
			}
			if err == nil && w.ApprovalID != ap.ID {
				return chat.Tr(lang, "outcome.voided"), true, nil
			}
		}
		return chat.Tr(lang, "outcome.cancelled"), true, nil
	}
	return chat.Tr(lang, "outcome.voided"), true, nil
}

// ---------------------------------------------------------------- 扫描

// SweepChatCards 发出到期的卡片,再收敛单子已有结论的卡片。
//
// 各渠道并行、互不等待,同一渠道同时只有一轮:上一轮还没完,这次就跳过那个渠道。返回时这一轮开始的
// 渠道都已处理完,所以周期调用者应当每次各起一个 goroutine,不要排队等 —— 一个渠道慢,另一个照常。
func (s *Services) SweepChatCards(ctx context.Context) {
	var wg sync.WaitGroup
	for _, ch := range chat.Channels {
		t := s.chatTransport(ch)
		if t == nil {
			continue
		}
		if _, busy := s.chatSweeping.LoadOrStore(ch, true); busy {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer s.chatSweeping.Delete(ch)
			defer func() {
				if r := recover(); r != nil {
					slog.Error("审批卡片扫描 panic", "channel", ch, "stack", string(debug.Stack()))
				}
			}()
			s.sweepChatChannel(ctx, ch, t)
		}()
	}
	wg.Wait()
}

// sweepChatChannel 扫一个渠道。机器人还没就绪(标识为空)就先不动:行留在库里,也不算失败。
func (s *Services) sweepChatChannel(ctx context.Context, ch chat.Channel, t chat.Transport) {
	bot := t.BotID()
	if bot == "" {
		return
	}
	phase := func(list func(string, time.Time, int) ([]model.ChatCard, error), do func(*model.ChatCard) error) {
		started := time.Now()
		rows, err := list(string(ch), started, chatSweepBatch)
		if err != nil {
			slog.Error("读审批卡片失败", "channel", ch, "err", err)
			return
		}
		for i := range rows {
			if ctx.Err() != nil || time.Since(started) > chatSweepBudget {
				return
			}
			// 超时多半是整个渠道不通,这一轮不再逐张去等。
			if errors.Is(do(&rows[i]), context.DeadlineExceeded) {
				return
			}
		}
	}
	phase(s.Repo.QueuedChatCards, func(row *model.ChatCard) error { return s.sendChatCard(ctx, ch, t, bot, row) })
	phase(s.Repo.EndedChatCards, func(row *model.ChatCard) error { return s.convergeChatCard(ctx, t, bot, row) })
}

// sendChatCard 发一张排着队的卡片。返回渠道那一次调用的错误。
//
// 能不能发全在此刻判:单子还在等、收件人还该收到、绑定还在且就是当前这个机器人的。
func (s *Services) sendChatCard(ctx context.Context, ch chat.Channel, t chat.Transport, bot string, row *model.ChatCard) error {
	ap, err := s.Repo.GetApproval(row.ApprovalID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		slog.Error("读审批单失败", "card", row.ID, "err", err)
		return nil
	}
	if err != nil || ap.Status != model.StatusPending {
		s.setChatCard(row, model.ChatCardClosed, nil)
		return nil
	}
	ok, err := s.chatMayReceive(ap, row.UserID)
	if err != nil {
		slog.Error("读卡片收件人失败", "card", row.ID, "err", err)
		return nil
	}
	b, err := s.Repo.ChatBindingOfUser(row.UserID, string(ch))
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		slog.Error("读聊天绑定失败", "card", row.ID, "err", err)
		return nil
	}
	if !ok || err != nil || b.BotID != bot {
		s.setChatCard(row, model.ChatCardClosed, nil)
		return nil
	}
	if row.Attempts >= chatMaxAttempts {
		// 次数在调用之前就记下,所以走到这里是最后一次发到一半进程断了,结果不明。
		s.setChatCard(row, model.ChatCardFailed, map[string]any{"last_error": orDefault(row.LastError, "interrupted while sending")})
		return nil
	}

	includeSQL := s.settingBool("chat.telegram.includeSql", false)
	card := s.buildChatCard(ap, ch, s.cardLang(), row.SourceTruncated, includeSQL)
	if card.Token, err = newChatToken(); err != nil {
		slog.Error("生成卡片令牌失败", "err", err)
		return nil
	}
	// 先写令牌摘要再发:反过来,发出之后没写上就崩了,那张卡片的按钮永远认不出来。重发会换掉它,
	// 旧卡片的按钮随之作废。次数与下次时间也在这里先记:发出去却记不下来时,不至于每轮都重发一张。
	attempts := row.Attempts + 1
	if !s.setChatCard(row, model.ChatCardQueued, map[string]any{
		"token_hash": chatCodeHash(card.Token), "bot_id": bot,
		"partial": card.Partial, "with_sql": chatShowsSQL(ch, includeSQL),
		"attempts": attempts, "next_at": time.Now().Add(chatRetryAfter(attempts)),
	}) {
		return nil
	}
	ref, err := chatCall(ctx, ch, func(ctx context.Context) (chat.Ref, error) { return t.Send(ctx, b.AccountID, card) })
	if err != nil {
		s.chatCallFailed(row, ap, attempts, "发送审批卡片失败", err)
		return err
	}
	s.setChatCard(row, model.ChatCardSent, map[string]any{
		"chat_id": ref.ChatID, "message_id": ref.MessageID, "attempts": 0, "next_at": time.Now(), "last_error": "",
	})
	return nil
}

// convergeChatCard 把一张已发出的卡片更新成结论。返回渠道那一次调用的错误。
func (s *Services) convergeChatCard(ctx context.Context, t chat.Transport, bot string, row *model.ChatCard) error {
	ap, err := s.Repo.GetApproval(row.ApprovalID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		slog.Error("读审批单失败", "card", row.ID, "err", err)
		return nil
	}
	// 单子没了没有结论可写;别的机器人发的消息,当前这个更新不了。
	if err != nil || row.BotID != bot || row.MessageID == "" {
		s.setChatCard(row, model.ChatCardClosed, nil)
		return nil
	}
	if row.Attempts >= chatMaxAttempts {
		s.setChatCard(row, model.ChatCardFailed, map[string]any{"last_error": orDefault(row.LastError, "interrupted while updating")})
		return nil
	}
	card := s.sentChatCard(ap, row)
	outcome, settled, err := s.chatOutcome(ap, card.Lang)
	if err != nil {
		slog.Error("读审批结论失败", "card", row.ID, "err", err)
		return nil
	}
	attempts := row.Attempts + 1
	if !settled && attempts == 1 {
		// 等一轮,让决定的那一步记完;算一次尝试,所以不会一直等下去。
		s.setChatCard(row, model.ChatCardSent, map[string]any{"attempts": attempts, "next_at": time.Now().Add(chatDecisionSettle)})
		return nil
	}
	card.Outcome = outcome
	if !s.setChatCard(row, model.ChatCardSent, map[string]any{"attempts": attempts, "next_at": time.Now().Add(chatRetryAfter(attempts))}) {
		return nil
	}
	ref := chat.Ref{ChatID: row.ChatID, MessageID: row.MessageID}
	_, err = chatCall(ctx, chat.Channel(row.Channel), func(ctx context.Context) (chat.Ref, error) { return ref, t.Update(ctx, ref, card) })
	switch {
	case errors.Is(err, chat.ErrGone):
		s.setChatCard(row, model.ChatCardClosed, map[string]any{"last_error": chatErrText(err)})
	case err != nil:
		s.chatCallFailed(row, ap, attempts, "更新审批卡片失败", err)
	default:
		s.setChatCard(row, model.ChatCardClosed, map[string]any{"last_error": ""})
	}
	return err
}

// chatCallFailed 记下一次失败;次数用完就不再试。下次时间从此刻重新算:调用之前记的那个没算上调用本身
// 花掉的时间,一次拖到超时的调用会让重试紧接着就来。
func (s *Services) chatCallFailed(row *model.ChatCard, ap *model.Approval, attempts int, what string, err error) {
	text, state := chatErrText(err), row.State
	if attempts >= chatMaxAttempts {
		state = model.ChatCardFailed
	}
	s.setChatCard(row, state, map[string]any{"last_error": text, "next_at": time.Now().Add(chatRetryAfter(attempts))})
	slog.Warn(what, "channel", row.Channel, "apNo", ap.ApNo, "user", row.UserID, "attempt", attempts, "err", text)
}

// setChatCard 把一张卡片改成 state 并写上 fields;它已经不在读到时的状态(被点击处理收场了)就什么都不做。
// 返回是否改到。
func (s *Services) setChatCard(row *model.ChatCard, state string, fields map[string]any) bool {
	up := map[string]any{"state": state}
	for k, v := range fields {
		up[k] = v
	}
	ok, err := s.Repo.UpdateChatCard(row.ID, row.State, up)
	if err != nil {
		slog.Error("写审批卡片失败", "card", row.ID, "state", state, "err", err)
	}
	return ok && err == nil
}

// chatCall 调一次渠道:有时限,panic 算失败。
//
// 调用放在自己的 goroutine 里,到点就走:渠道不理 ctx 时也不能把扫描挂住。被丢下的那次之后若又发成功了,
// 令牌多半已被重发换掉,那张卡片的按钮不起作用。
func chatCall(ctx context.Context, ch chat.Channel, call func(context.Context) (chat.Ref, error)) (chat.Ref, error) {
	ctx, cancel := context.WithTimeout(ctx, chatCallTimeout)
	defer cancel()
	type result struct {
		ref chat.Ref
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 不记 panic 的内容:里面可能有带机器人令牌的地址。
				slog.Error("聊天渠道 panic", "channel", ch, "stack", string(debug.Stack()))
				done <- result{err: errChatPanic}
			}
		}()
		ref, err := call(ctx)
		done <- result{ref, err}
	}()
	select {
	case r := <-done:
		return r.ref, r.err
	case <-ctx.Done():
		select {
		case r := <-done: // 正好同时回来:以它为准
			return r.ref, r.err
		default:
			return chat.Ref{}, ctx.Err()
		}
	}
}

// chatRetryAfter 是第 n 次(从 1 起)失败之后等多久。
func chatRetryAfter(n int) time.Duration {
	return chatBackoff[min(n, len(chatBackoff))-1]
}

// chatErrText 是渠道错误落库与进日志的样子:一行、没有控制字符、放得进列。渠道自己保证里面没有机密。
func chatErrText(err error) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(err.Error(), ""))
	return clipColumn(strings.TrimSpace(clean), chatErrWidth)
}

// newChatToken 随机出一个卡片按钮令牌;库里只存它的摘要。
func newChatToken() (string, error) {
	b := make([]byte, chatTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
