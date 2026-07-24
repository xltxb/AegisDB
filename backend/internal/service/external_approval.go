package service

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"strings"

	"velagateway/internal/dto"
	"velagateway/internal/model"
	"velagateway/pkg/crypto"
)

// extApprovalConfig is the resolved (decrypted) external-approval configuration
// read from settings. See ADR-0003.
type extApprovalConfig struct {
	enabled        bool
	baseURL        string
	token          string
	aiGroup        string
	callbackURL    string
	callbackSecret string
	allowIPs       string
}

func (s *Services) extApprovalConfig() extApprovalConfig {
	base := strings.TrimRight(strings.TrimSpace(s.settingString("approval.external.callbackBaseURL", "")), "/")
	cb := ""
	if base != "" {
		cb = base + "/api/v1/approvals/lark/callback"
	}
	return extApprovalConfig{
		enabled:        s.settingBool("approval.external.enabled", false),
		baseURL:        strings.TrimRight(strings.TrimSpace(s.settingString("approval.external.baseURL", "")), "/"),
		token:          s.decryptSetting("approval.external.token"),
		aiGroup:        s.settingString("approval.external.aiGroup", ""),
		callbackURL:    cb,
		callbackSecret: s.decryptSetting("approval.external.callbackSecret"),
		allowIPs:       s.settingString("approval.external.callbackAllowIPs", ""),
	}
}

// decryptSetting reads a setting and decrypts it (token/callbackSecret are stored
// encrypted at rest via crypto.EncryptSecret on save). DecryptSecret returns
// empty/legacy-plaintext values unchanged, so a blank setting is safe.
func (s *Services) decryptSetting(key string) string {
	raw := s.settingString(key, "")
	if raw == "" {
		return ""
	}
	if v, err := crypto.DecryptSecret(raw); err == nil {
		return v
	}
	return raw
}

// dispatchExternalApproval pushes a high-risk ticket to审批魔方 for interactive
// 飞书 approval. Best-effort + async: a failure never blocks or fails建单 (the
// in-app approval path stays usable). On success the vendor task_id is stored for
// the Phase-2 write-back.
func (s *Services) dispatchExternalApproval(u *model.User, ap *model.Approval) {
	cfg := s.extApprovalConfig()
	if !cfg.enabled || cfg.baseURL == "" || cfg.token == "" || cfg.callbackURL == "" {
		return
	}
	account := ap.Initiator
	if u != nil && u.Email != "" {
		account = u.Email // 出站 user = 发起人网关账户(email)
	}
	go func() {
		taskID, err := s.Webhook.SendExternalApproval(cfg.baseURL, cfg.token, cfg.aiGroup, cfg.callbackURL, account, ap)
		if err != nil {
			slog.Warn("external approval dispatch failed", "apNo", ap.ApNo, "err", err)
			return
		}
		if taskID != "" {
			_ = s.Repo.SetApprovalExternalTask(ap.ID, taskID)
		}
	}()
}

// cancelExternalApproval tells审批魔方 to cancel a ticket the gateway resolved on
// its own (internal timeout), so the飞书 card doesn't linger as "pending". Takes
// the approval by value to be goroutine-safe under the sweep loop; no-op unless
// external approval is configured and this ticket was dispatched externally.
func (s *Services) cancelExternalApproval(ap model.Approval) {
	if ap.ExternalTaskID == "" {
		return
	}
	cfg := s.extApprovalConfig()
	if !cfg.enabled || cfg.baseURL == "" || cfg.token == "" {
		return
	}
	go func() {
		if err := s.Webhook.PatchExternalStatus(cfg.baseURL, cfg.token, ap.ExternalTaskID); err != nil {
			slog.Warn("external approval cancel failed", "apNo", ap.ApNo, "taskId", ap.ExternalTaskID, "err", err)
		}
	}()
}

// DecideApprovalExternal applies an审批魔方 callback decision to our approval.
// Correlates by ApNo (echoed back as external_task_id), is idempotent (a
// non-pending ticket returns its current status), enforces the禁自审 net locally
// (the vendor does not guarantee非发起人审批), then reuses finalizeApproval to
// execute/reject. Returns the resulting approval status.
func (s *Services) DecideApprovalExternal(cb dto.LarkApprovalCallbackReq) (string, error) {
	apNo := strings.TrimSpace(cb.ExternalTaskID)
	if apNo == "" {
		apNo = strings.TrimSpace(cb.RequestID)
	}
	if apNo == "" {
		return "", ErrNotFound
	}
	ap, err := s.Repo.GetApprovalByApNo(apNo)
	if err != nil || ap == nil {
		return "", ErrNotFound
	}
	// Idempotent: an already-decided ticket just echoes its current status.
	if ap.Status != model.StatusPending {
		return ap.Status, nil
	}
	approved := cb.Approved
	// SoD net: if every approver is the initiator themselves, honour
	// approval.allowSelfApprove (default off) → block the self-approval.
	if approved && s.approversAreInitiator(cb.Approver, ap) && !s.settingBool("approval.allowSelfApprove", false) {
		slog.Warn("external self-approval blocked", "apNo", ap.ApNo, "approver", cb.Approver)
		approved = false
	}
	operator := strings.Join(cb.Approver, ",")
	if strings.TrimSpace(operator) == "" {
		operator = "审批魔方"
	}
	if _, ferr := s.finalizeApproval(ap, approved, operator); ferr != nil {
		if ferr == ErrAlreadyDecided {
			// Lost the race with the in-app path / another callback — return current.
			if cur, e := s.Repo.GetApproval(ap.ID); e == nil {
				return cur.Status, nil
			}
		}
		return "", ferr
	}
	if approved {
		return model.StatusApproved, nil
	}
	return model.StatusRejected, nil
}

// approversAreInitiator reports whether the approver set is non-empty and every
// member equals the ticket's initiator account (email) — i.e. a self-approval.
func (s *Services) approversAreInitiator(approvers []string, ap *model.Approval) bool {
	if len(approvers) == 0 {
		return false
	}
	init := ""
	if u, err := s.Repo.GetUserByID(ap.InitiatorID); err == nil && u != nil {
		init = strings.ToLower(strings.TrimSpace(u.Email))
	}
	if init == "" {
		return false // can't determine the initiator account — don't over-block
	}
	for _, a := range approvers {
		if strings.ToLower(strings.TrimSpace(a)) != init {
			return false
		}
	}
	return true
}

// VerifyExternalCallback authenticates an inbound审批魔方 callback: the
// X-Callback-Secret must match (fail closed — an unconfigured secret rejects
// everything), and, when a callback IP allowlist is configured, the client IP
// must be listed. Returns ErrForbidden on any failure.
func (s *Services) VerifyExternalCallback(providedSecret, clientIP string) error {
	cfg := s.extApprovalConfig()
	if cfg.callbackSecret == "" {
		return ErrForbidden // never accept unauthenticated callbacks
	}
	if subtle.ConstantTimeCompare([]byte(providedSecret), []byte(cfg.callbackSecret)) != 1 {
		return ErrForbidden
	}
	if !ipAllowed(cfg.allowIPs, clientIP) {
		return ErrForbidden
	}
	return nil
}

// ipAllowed reports whether clientIP is permitted by a comma-separated allowlist
// of IPs / CIDRs. An empty allowlist permits any source (the secret is the
// primary control).
func ipAllowed(list, clientIP string) bool {
	list = strings.TrimSpace(list)
	if list == "" {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(clientIP))
	if ip == nil {
		return false
	}
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			if _, cidr, err := net.ParseCIDR(e); err == nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		if pip := net.ParseIP(e); pip != nil && pip.Equal(ip) {
			return true
		}
	}
	return false
}
