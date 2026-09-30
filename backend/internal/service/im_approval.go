package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
	"velagateway/internal/repository"
	"velagateway/pkg/crypto"
)

// imSettings 把设置表交给渠道读。Secret 走解密。
type imSettings struct{ s *Services }

func (x imSettings) String(k string) string { return x.s.Repo.SettingString(k, "") }
func (x imSettings) Bool(k string) bool     { return x.s.Repo.SettingBool(k, false) }
func (x imSettings) Secret(k string) string { return x.s.decryptSetting(k) }

// imStore 是 Hub 的持久化,落在 tbl_im_identity / tbl_im_card。
type imStore struct{ repo *repository.Repo }

func (x imStore) PlatformIDs(ch string, ids []int64) (map[int64]string, error) {
	return x.repo.IMPlatformIDs(ch, ids)
}

func (x imStore) RecordCard(c imchannel.Card) error {
	return x.repo.CreateIMCard(&model.IMCard{ApprovalID: c.ApprovalID, Channel: c.Channel, UserID: c.UserID, MsgRef: c.MsgRef})
}

func (x imStore) OpenCards(approvalID int64) ([]imchannel.Card, error) {
	rows, err := x.repo.OpenIMCards(approvalID)
	if err != nil {
		return nil, err
	}
	out := make([]imchannel.Card, 0, len(rows))
	for _, r := range rows {
		out = append(out, imchannel.Card{ID: r.ID, ApprovalID: r.ApprovalID, Channel: r.Channel, UserID: r.UserID, MsgRef: r.MsgRef})
	}
	return out, nil
}

func (x imStore) MarkCardClosed(id int64) error { return x.repo.CloseIMCard(id) }

// newIMHub 造 Hub。出站客户端就是 webhook 用的那个带 SSRF 防护的客户端。
func newIMHub(s *Services) *imchannel.Hub {
	return imchannel.NewHub(imchannel.Deps{
		Settings:    imSettings{s},
		HTTP:        newOutboundClient(),
		ValidateURL: validateOutboundURL,
	}, imStore{s.Repo})
}

// decryptSetting reads a setting and decrypts it (secrets are stored encrypted at
// rest via crypto.EncryptSecret on save). DecryptSecret returns empty/legacy-
// plaintext values unchanged, so a blank setting is safe.
func (s *Services) decryptSetting(key string) string {
	raw := s.Repo.SettingString(key, "")
	if raw == "" {
		return ""
	}
	if v, err := crypto.DecryptSecret(raw); err == nil {
		return v
	}
	return raw
}

func approvalKind(ap *model.Approval) string {
	switch {
	case ap.WindowID > 0:
		return "window"
	case ap.ExportJobID > 0:
		return "export"
	case ap.ReleaseID > 0:
		return "release"
	}
	return "sql"
}

// imCardFor 把工单翻成卡片。命令在**这里**先脱敏再截断(safeClip),渠道拿到的已是能出
// 网关的文本 —— 口令的处理只有这一处,不指望五个渠道各自记得。
func (s *Services) imCardFor(ap *model.Approval) imchannel.ApprovalCard {
	console := ""
	if base := strings.TrimRight(strings.TrimSpace(s.Repo.SettingString("notify.consoleURL", "")), "/"); base != "" {
		// 前端是 BrowserRouter:/#/approvals 会落到根路径(总览页),而不是审批页。
		console = base + "/approvals"
	}
	return imchannel.ApprovalCard{
		ApNo: ap.ApNo, Kind: approvalKind(ap), Env: ap.Env, Tier: ap.TierCode,
		Instance: ap.Instance, Database: ap.Database, Risk: ap.RiskLevel, Initiator: ap.Initiator,
		Reason: clip(ap.Reason, 200), Command: safeClip(ap.Command, 800), ConsoleURL: console,
	}
}

// imApprovalCreated:群通知 + 私信链上成员。发起人本人即使在链上也不发 —— 他点了也会被
// BlockSelf 拒,发给他只会多一个点不动的按钮。
func (s *Services) imApprovalCreated(ap *model.Approval) {
	card := s.imCardFor(ap)
	s.IM.NotifyApproval(card)
	steps, _ := s.Repo.StepsOf(ap.ID)
	seen := map[int64]bool{}
	var recipients []int64
	for _, st := range steps {
		if st.ApproverID == 0 || st.ApproverID == ap.InitiatorID || seen[st.ApproverID] {
			continue
		}
		seen[st.ApproverID] = true
		recipients = append(recipients, st.ApproverID)
	}
	s.IM.ApprovalCreated(ap.ID, card, recipients)
}

// imApprovalSettled 收起这张单在所有渠道上的卡片。status 是工单的新状态。
func (s *Services) imApprovalSettled(ap *model.Approval, status, operator string) {
	s.IM.ApprovalSettled(ap.ID, imchannel.CardState{Status: status, Text: settledText(status, operator)})
}

func settledText(status, operator string) string {
	switch status {
	case model.StatusApproved:
		return "已由 " + operator + " 通过"
	case model.StatusRejected:
		return "已由 " + operator + " 驳回"
	case model.StatusExpired:
		return "已超时作废"
	case model.StatusCancelled:
		return "已撤回"
	}
	return "已处理"
}

// imTestApNo 是测试卡片上的单号。点它只回一句话,不碰任何工单。
const imTestApNo = "AP-TEST"

// imSampleCard 是测试通知 / 测试卡片用的样例工单。
func (s *Services) imSampleCard() imchannel.ApprovalCard {
	return s.imCardFor(&model.Approval{
		ApNo: imTestApNo, Instance: "order-cluster", Env: "prod", TierCode: "prod", RiskLevel: model.RiskHigh,
		Initiator: "Vela", Command: "DROP TABLE orders_2024_q3;", Reason: "测试审批卡片推送",
	})
}

// TestIMNotify 同步发一条测试群通知,把结果原话告诉管理员。
func (s *Services) TestIMNotify(channel string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.IM.TestNotify(ctx, channel, s.imSampleCard())
	switch {
	case errors.Is(err, imchannel.ErrNotConfigured):
		return false, "未配置" + imchannel.Label(channel) + "群通知 Webhook"
	case err != nil:
		return false, "发送失败 · " + err.Error()
	}
	return true, "测试消息已发送到" + imchannel.Label(channel)
}

// TestIMCard 给管理员**自己**私信一张测试卡片。点它只回一句话(见 OnAction 对 AP-TEST
// 的处理),顺带验证了「这个账号认得出是我」。
func (s *Services) TestIMCard(actor *model.User, channel string) (bool, string) {
	ids, err := s.Repo.IMPlatformIDs(channel, []int64{actor.ID})
	pid, ok := ids[actor.ID]
	if err != nil || !ok {
		return false, "你还没有绑定" + imchannel.Label(channel) + "账号,请先在账户菜单「IM 账号绑定」中绑定"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.IM.TestCard(ctx, channel, pid, s.imSampleCard()); err != nil {
		if errors.Is(err, imchannel.ErrNotConfigured) {
			return false, imchannel.Label(channel) + "交互审批未配置"
		}
		return false, "发送失败 · " + err.Error()
	}
	return true, "测试卡片已私信给你"
}

const imBindInvalidReply = "无效或已过期的绑定码,请在控制台重新生成"

// OnAction 是所有渠道按钮点击的唯一入口。
//
// 这里新增的判断只有一条:这个平台账号能不能认成一个启用的真人网关账户。认出来之后,
// 能不能批完全交给 DecideApprovalAs → DecideBlockFor,和控制台走同一条路。
func (s *Services) OnAction(_ context.Context, a imchannel.Action) imchannel.ActionResult {
	label := imchannel.Label(a.Channel)
	u := s.imUser(a.Channel, a.PlatformUserID)
	if u == nil {
		s.auditIMStranger(a)
		return imchannel.ActionResult{Toast: "你的" + label + "账号未绑定网关账户,请先在控制台「IM 账号绑定」中完成绑定"}
	}
	if a.ApNo == imTestApNo {
		return imchannel.ActionResult{Toast: "测试卡片 · 点击已送达网关,认出的账户是 " + u.Name}
	}
	// 单号不存在,与「不在链上」回同一句话:按钮回调可以被伪造,一个绑定过的人不该
	// 靠换单号来探出哪些单存在、最后怎样了。
	outsider := imchannel.ActionResult{Toast: BlockNotInChain.Reason()}
	ap, err := s.Repo.GetApprovalByApNo(a.ApNo)
	if err != nil || ap == nil {
		return outsider
	}
	_, err = s.DecideApprovalAs(u, ap.ID, a.Approve, clip(u.Name+"("+label+")", 127))
	var refusal *DecideRefusal
	switch {
	case err == nil:
		if a.Approve {
			return imchannel.ActionResult{Toast: "已批准,请发起人回控制台执行"}
		}
		return imchannel.ActionResult{Toast: "已驳回"}
	case errors.Is(err, ErrAlreadyDecided):
		// 终态只说给发起人和链成员听 —— 其他人拿到的与一张待审批、他不在链上的单一样。
		if u.ID != ap.InitiatorID && !s.isChainMember(ap.ID, u) {
			return outsider
		}
		status := ap.Status
		if cur, e := s.Repo.GetApproval(ap.ID); e == nil {
			status = cur.Status
		}
		return imchannel.ActionResult{Toast: "该工单已处理(" + approvalStatusLabel(status) + ")"}
	case errors.As(err, &refusal):
		return imchannel.ActionResult{Toast: refusal.Block.Reason()}
	default:
		slog.Error("im action failed", "channel", a.Channel, "apNo", ap.ApNo, "err", err)
		return imchannel.ActionResult{Toast: "处理失败,请回控制台重试"}
	}
}

// imUser 把平台账号认成网关账户。停用的、服务账号一律认不出 —— 与登录的规则一致。
func (s *Services) imUser(channel, pid string) *model.User {
	id, err := s.Repo.IMIdentityByPlatform(channel, pid)
	if err != nil || id == nil {
		return nil
	}
	u, err := s.Repo.GetUserByID(id.UserID)
	if err != nil || u == nil || u.Status != "active" || u.Kind == model.UserKindService {
		return nil
	}
	return u
}

// auditIMStranger 记下一次认不出是谁的点击。
//
// 与失败登录把输入的邮箱记进 actor 是同一个取舍:认不出是谁,也要留下「有人试过」。一个
// 陌生账号反复点生产库工单的按钮,是需要被看见的事。平台 ID 来自外部,按列宽截断 ——
// 由外部输入决定审计写不写得进去,这件事本身就是问题。
func (s *Services) auditIMStranger(a imchannel.Action) {
	verb := "驳回"
	if a.Approve {
		verb = "通过"
	}
	actor := &model.User{Name: clip(a.Channel+":"+a.PlatformUserID, 63)}
	s.recordAuditBy(actor, imchannel.Label(a.Channel)+"(未绑定)", nil,
		"im.action "+verb+" "+clip(a.ApNo, 32), model.RiskMid, model.ResultRejected, clip(a.ApNo, 31), "")
}

func approvalStatusLabel(status string) string {
	switch status {
	case model.StatusApproved:
		return "已通过"
	case model.StatusRejected:
		return "已驳回"
	case model.StatusExpired:
		return "已超时"
	case model.StatusCancelled:
		return "已撤回"
	}
	return status
}
