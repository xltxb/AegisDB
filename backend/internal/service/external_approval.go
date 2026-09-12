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
		slog.Warn("external callback: no approval for correlation key", "key", apNo)
		return "", ErrNotFound
	}
	// 这张单**外发过吗**,以及回调说的那张卡片**是不是它**。
	//
	// 关联跑在我们自己的 ApNo 上,而 ApNo 是一个可预测的计数器 —— 提交人在自己的执行
	// 响应里就看得到,往前往后数几个就是别人的单号。所以 ApNo 对得上只说明"确实有这么
	// 一张单",完全不说明"这张单外发过"。把它和 callbackSecret 放在一起,持有密钥的人
	// 就能终审站内任意一张待审工单。
	//
	// 因此 fail closed:手上没有厂商任务号,就不认外部回调。
	//
	// 这里收紧过两次,两次都值得记下来:
	//
	//  1. 最早的条件是 `ap.ExternalTaskID != "" && cb.TaskID != ""` —— 两个都非空才比。
	//     于是回调方只要**省掉 task_id**,整条交叉校验就被跳过。
	//  2. 然后是 `ap.ExternalTaskID != ""`,留这个口子是为了照顾一个竞态:出站在
	//     goroutine 里跑(见 dispatchExternalApproval),task id 是异步写回的,理论上
	//     回调可能比我们自己的写入更快。
	//
	// 第 2 条的代价和它挡住的东西不成比例。ExternalTaskID 为空的真实来源不是那一瞬,
	// 而是三种**永久**为空的情况:出站失败(厂商不可达)、厂商没回 task id、总开关打开
	// 之前建的单。这三种单子在旧规则下一律可被外部终审。
	//
	// 现在那个竞态里的回调会被拒 —— 厂商重试即可,而站内审批这条路始终可用。用一个
	// 会自愈的失败,换掉一个不会自愈的洞。
	if ap.ExternalTaskID == "" {
		slog.Warn("external callback: ticket was never dispatched externally", "apNo", ap.ApNo)
		return "", ErrForbidden
	}
	if strings.TrimSpace(cb.TaskID) != ap.ExternalTaskID {
		slog.Warn("external callback: vendor task mismatch or missing",
			"apNo", ap.ApNo, "expected", ap.ExternalTaskID, "got", cb.TaskID)
		return "", ErrForbidden
	}
	// Idempotent: an already-decided ticket just echoes its current status.
	if ap.Status != model.StatusPending {
		slog.Info("external callback: ticket already decided (idempotent)", "apNo", ap.ApNo, "status", ap.Status)
		return ap.Status, nil
	}
	approved := cb.Approved
	// 通过,但没说是谁批的 —— 不认。
	//
	// 禁自审那道网靠比对 approver 与发起人。approver 为空时那个比对什么也比不出来,
	// 而旧代码把"比不出来"当成了"不是自审"直接放行:发起人在自己的卡片上点通过,厂商
	// 回调不带 approver,这道网就整个落空,审计里留下的审批人是「审批魔方」四个字 ——
	// 事后谁也说不出到底是谁签的字。
	//
	// 驳回不受这条约束:它是安全方向,而外部系统超时自动关单这类正当场景恰恰不带
	// 审批人。
	if approved && len(nonBlank(cb.Approver)) == 0 {
		slog.Warn("external callback: approval carries no approver — refused", "apNo", ap.ApNo)
		return "", ErrForbidden
	}
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
	// 截断到列宽以内。tbl_audit_log.operator 是 size:128,回调方塞进一个更长的
	// approver 会让审计 INSERT 在 STRICT 模式的 MySQL 上报错 —— 而审批已经生效了,
	// 于是哈希链上缺一行。**由外部输入决定审计写不写得进去**,这件事本身就是问题。
	operator = clip(operator, 128)
	slog.Info("external callback: finalizing ticket", "apNo", ap.ApNo, "approved", approved, "operator", operator)
	if _, ferr := s.finalizeApproval(ap, approved, operator); ferr != nil {
		if ferr == ErrAlreadyDecided {
			// Lost the race with the in-app path / another callback — return current.
			if cur, e := s.Repo.GetApproval(ap.ID); e == nil {
				slog.Info("external callback: lost decide race", "apNo", ap.ApNo, "status", cur.Status)
				return cur.Status, nil
			}
		}
		slog.Error("external callback: finalize failed", "apNo", ap.ApNo, "err", ferr)
		return "", ferr
	}
	final := model.StatusRejected
	if approved {
		final = model.StatusApproved
	}
	slog.Info("external callback: ticket finalized", "apNo", ap.ApNo, "status", final)
	return final, nil
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

// VerifyExternalCallback authenticates an inbound审批魔方 callback: the provided
// secret (from Authorization: Bearer, or a ?secret= query fallback) must match
// (fail closed — an unconfigured secret rejects everything), and, when a callback
// IP allowlist is configured, the client IP must be listed. Returns ErrForbidden
// on any failure.
func (s *Services) VerifyExternalCallback(providedSecret, clientIP string) error {
	cfg := s.extApprovalConfig()
	// The feature switch governs the INBOUND side too. dispatch/cancel already
	// bail when it is off, but the callback used to stay live off a leftover
	// callbackSecret, so switching the integration off did not close the endpoint
	// it opened — and that endpoint drives commands to execution (EA1).
	if !cfg.enabled {
		return ErrForbidden
	}
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

// nonBlank 去掉列表里的空白项 —— 一个 [""] 和一个 [] 说的是同一件事:没人签字。
func nonBlank(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}
