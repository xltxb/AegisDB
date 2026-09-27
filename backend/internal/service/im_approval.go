package service

import (
	"strings"

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
		console = base + "/#/approvals"
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
