package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
	"velagateway/internal/repository"
)

// ErrIMChannelOff:渠道的交互审批没打开(机器人没在跑),生成的码发出去没人收。
var ErrIMChannelOff = fmt.Errorf("im channel approval is off")

const (
	imBindTTL      = 10 * time.Minute
	bindCodeAlpha  = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 去掉 0/O、1/I:念给别人听时不出错
	bindCodePrefix = "BIND-"
)

var bindCodeRe = regexp.MustCompile(`^BIND-[A-HJ-NP-Z2-9]{8}$`)

type IMIdentityView struct {
	Channel        string     `json:"channel"`
	Label          string     `json:"label"`
	Enabled        bool       `json:"enabled"`
	Bound          bool       `json:"bound"`
	PlatformUserID string     `json:"platformUserId,omitempty"`
	BoundBy        string     `json:"boundBy,omitempty"`
	BotName        string     `json:"botName,omitempty"`
	BoundAt        *time.Time `json:"boundAt,omitempty"`
}

func newBindCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i, v := range b {
		out[i] = bindCodeAlpha[int(v)%len(bindCodeAlpha)] // 256 % 32 == 0,没有取模偏差
	}
	return bindCodePrefix + string(out), nil
}

func hashBindCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// IMIdentities 列出每个能做交互审批的渠道,以及这个人在上面绑没绑。
func (s *Services) IMIdentities(userID int64) []IMIdentityView {
	rows, _ := s.Repo.IMIdentitiesOfUser(userID)
	byCh := map[string]model.IMIdentity{}
	for _, r := range rows {
		byCh[r.Channel] = r
	}
	kinds := s.IM.ApproveKinds()
	out := make([]IMIdentityView, 0, len(kinds))
	for _, k := range kinds {
		v := IMIdentityView{Channel: k, Label: imchannel.Label(k), Enabled: s.IM.ApprovalEnabled(k), BotName: s.IM.BotName(k)}
		if r, ok := byCh[k]; ok {
			at := r.CreatedAt
			v.Bound, v.PlatformUserID, v.BoundBy, v.BoundAt = true, r.PlatformUserID, r.BoundBy, &at
		}
		out = append(out, v)
	}
	return out
}

// IssueIMBindCode 生成一个一次性码,只在这里回显一次。
//
// 渠道没打开时拒绝:机器人没在跑,码发出去没人收,表现就是「按了没反应」—— 这类静默
// 失败最难排查,不如当场说清楚。
func (s *Services) IssueIMBindCode(u *model.User, channel string) (string, time.Time, error) {
	if !s.IM.CanApprove(channel) {
		return "", time.Time{}, ErrNotFound
	}
	if !s.IM.ApprovalEnabled(channel) {
		return "", time.Time{}, ErrIMChannelOff
	}
	code, err := newBindCode()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(imBindTTL)
	if err := s.Repo.PutIMBindCode(&model.IMBindCode{UserID: u.ID, Channel: channel, CodeHash: hashBindCode(code), ExpiresAt: exp}); err != nil {
		return "", time.Time{}, err
	}
	return code, exp, nil
}

// OnDirectMessage 处理私聊里发来的绑定码。
//
// 发送者的平台 ID 由平台保证(签名 / 连接本身),伪造不了 —— 所以「用自己的账号把码发给
// 机器人」就证明了这个平台账号归申请人所有。不匹配的一律回同一句话,不透露码存不存在、
// 过没过期。
func (s *Services) OnDirectMessage(_ context.Context, dm imchannel.DM) string {
	code := strings.ToUpper(strings.TrimSpace(dm.Text))
	if !bindCodeRe.MatchString(code) {
		return imBindInvalidReply
	}
	uid, ok, err := s.Repo.ConsumeIMBindCode(dm.Channel, hashBindCode(code), time.Now())
	if err != nil || !ok {
		return imBindInvalidReply
	}
	u, err := s.Repo.GetUserByID(uid)
	if err != nil || u == nil || u.Status != "active" || u.Kind == model.UserKindService {
		return imBindInvalidReply
	}
	label := imchannel.Label(dm.Channel)
	err = s.Repo.BindIMIdentity(&model.IMIdentity{UserID: uid, Channel: dm.Channel, PlatformUserID: dm.PlatformUserID, BoundBy: model.IMBoundSelf})
	if errors.Is(err, repository.ErrIMIdentityTaken) {
		return "这个" + label + "账号已绑定到其他网关账户,请联系管理员"
	}
	if err != nil {
		return "绑定失败,请稍后重试"
	}
	s.auditAdminAction(u, "im.bind channel="+dm.Channel+" user="+u.Email+" by=self")
	return "已绑定到 " + u.Name + "(" + u.Email + ")"
}

func (s *Services) UnbindOwnIM(u *model.User, channel string) error {
	if !s.IM.CanApprove(channel) {
		return ErrNotFound
	}
	if ok, err := s.Repo.UnbindIMIdentity(u.ID, channel); err != nil {
		return err
	} else if ok {
		s.auditAdminAction(u, "im.unbind channel="+channel+" user="+u.Email+" by=self")
	}
	return nil
}

// AdminBindIM 管理员声明「这个平台账号是这个人的」。与代绑 MFA 同一个信任假设。
func (s *Services) AdminBindIM(actor *model.User, userID int64, channel, pid string) error {
	if !s.IM.CanApprove(channel) {
		return ErrNotFound
	}
	pid = strings.TrimSpace(pid)
	if pid == "" || len(pid) > 128 {
		return ErrBadRequest
	}
	u, err := s.Repo.GetUserByID(userID)
	if err != nil || u == nil {
		return ErrNotFound
	}
	if err := s.Repo.BindIMIdentity(&model.IMIdentity{UserID: u.ID, Channel: channel, PlatformUserID: pid, BoundBy: model.IMBoundAdmin}); err != nil {
		return err
	}
	s.auditAdminAction(actor, "admin.im.bind channel="+channel+" user="+u.Email+" platformUserId="+clip(pid, 64))
	return nil
}

func (s *Services) AdminUnbindIM(actor *model.User, userID int64, channel string) error {
	if !s.IM.CanApprove(channel) {
		return ErrNotFound
	}
	u, err := s.Repo.GetUserByID(userID)
	if err != nil || u == nil {
		return ErrNotFound
	}
	if _, err := s.Repo.UnbindIMIdentity(u.ID, channel); err != nil {
		return err
	}
	s.auditAdminAction(actor, "admin.im.unbind channel="+channel+" user="+u.Email)
	return nil
}
