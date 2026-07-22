package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/model"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
	"velagateway/pkg/sqlutil"
)

// ---------------------------------------------------------------- Connections

func (h *Handler) ListConnections(c *gin.Context) {
	cs, _ := h.Svc.AccessibleConnections(middleware.CurrentUser(c))
	resp.OK(c, cs)
}

// ListTags returns every distinct connection tag (for tag pickers).
func (h *Handler) ListTags(c *gin.Context) {
	resp.OK(c, h.Svc.AllTags())
}

func (h *Handler) CreateConnection(c *gin.Context) {
	var req dto.ConnectionCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	conn, err := h.Svc.CreateConnection(req)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "创建失败:"+err.Error())
		return
	}
	resp.OK(c, conn)
}

// UpdateConnection edits an existing instance's config (admin only).
func (h *Handler) UpdateConnection(c *gin.Context) {
	var req dto.ConnectionUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	conn, err := h.Svc.UpdateConnection(pathID(c), req)
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "连接不存在")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "更新失败:"+err.Error())
		return
	}
	resp.OK(c, conn)
}

func (h *Handler) TestConnection(c *gin.Context) {
	conn, err := h.Repo.GetConnection(pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "连接不存在")
		return
	}
	ok, msg := h.Svc.Executor.Test(conn)
	resp.OK(c, gin.H{"ok": ok, "message": msg})
}

func (h *Handler) PatchConnection(c *gin.Context) {
	var req dto.ConnectionStatusReq
	_ = c.ShouldBindJSON(&req)
	id := pathID(c)
	switch {
	case req.Tags != nil: // replace tags
		if err := h.Svc.SetConnectionTags(id, *req.Tags); err != nil {
			resp.Fail(c, resp.CodeInternalError, "保存标签失败")
			return
		}
	case req.Policy != nil: // set gateway policy
		if err := h.Svc.SetConnectionPolicy(id, *req.Policy); err != nil {
			resp.Fail(c, resp.CodeBadRequest, "网关策略无效")
			return
		}
	default: // status toggle / explicit set
		if err := h.Svc.ToggleConnection(id, req.Status); err != nil {
			resp.Fail(c, resp.CodeBadRequest, "切换失败")
			return
		}
	}
	conn, _ := h.Repo.GetConnection(id)
	resp.OK(c, conn)
}

// GetConnectionSchema returns a connection's database→table tree: introspected
// live from the target when the connection has real credentials, otherwise the
// seeded (simulated) tree. See Services.ConnectionSchema.
func (h *Handler) GetConnectionSchema(c *gin.Context) {
	resp.OK(c, h.Svc.ConnectionSchema(middleware.CurrentUser(c), pathID(c), c.Query("database")))
}

// ---------------------------------------------------------------- Roles

func (h *Handler) ListRoles(c *gin.Context) {
	roles, _ := h.Svc.ListRolesBrief()
	resp.OK(c, roles)
}

func (h *Handler) GetRole(c *gin.Context) {
	r, err := h.Svc.RoleDetail(pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "角色不存在")
		return
	}
	resp.OK(c, r)
}

func (h *Handler) UpdateRole(c *gin.Context) {
	var req dto.RoleUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	role, err := h.Repo.GetRole(pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "角色不存在")
		return
	}
	if req.Name != "" {
		role.Name = req.Name
	}
	if req.Description != "" {
		role.Description = req.Description
	}
	if req.DefaultConnRole != "" {
		role.DefaultConnRole = req.DefaultConnRole
	}
	if req.CanApprove != nil {
		role.CanApprove = *req.CanApprove
	}
	if err := h.Repo.UpdateRole(role); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	r, _ := h.Svc.RoleDetail(role.ID)
	resp.OK(c, r)
}

func (h *Handler) SetRoleMenus(c *gin.Context) {
	var req dto.MenusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Repo.SetMenus(pathID(c), req.Menus); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

func (h *Handler) SetRoleCapabilities(c *gin.Context) {
	var req dto.CapabilitiesReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Repo.SetMatrix(pathID(c), req.Matrix); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// SetRoleTags assigns the DB tags a role (user group) may access.
func (h *Handler) SetRoleTags(c *gin.Context) {
	var req dto.RoleTagsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.SetRoleTags(pathID(c), req.Tags); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	r, _ := h.Svc.RoleDetail(pathID(c))
	resp.OK(c, r)
}

func (h *Handler) AddRoleMember(c *gin.Context) {
	var req dto.MemberReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Repo.AddMember(pathID(c), req.UserID); err != nil {
		resp.Fail(c, resp.CodeInternalError, "添加失败")
		return
	}
	r, _ := h.Svc.RoleDetail(pathID(c))
	resp.OK(c, r)
}

func (h *Handler) RemoveRoleMember(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Param("userId"), 10, 64)
	if err := h.Repo.RemoveMember(pathID(c), userID); err != nil {
		resp.Fail(c, resp.CodeInternalError, "移除失败")
		return
	}
	r, _ := h.Svc.RoleDetail(pathID(c))
	resp.OK(c, r)
}

// ---------------------------------------------------------------- Risk dictionary

func (h *Handler) ListRiskCommands(c *gin.Context) {
	resp.OK(c, h.Svc.RiskDictView())
}

func (h *Handler) UpsertRiskCommand(c *gin.Context) {
	var req dto.RiskCommandUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Repo.UpsertRiskCommand(req.Command, req.Env); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	resp.OK(c, h.Svc.RiskDictView())
}

func (h *Handler) PatchRiskCommand(c *gin.Context) {
	name := strings.ToUpper(c.Param("name"))
	var req dto.RiskCommandPatchReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Repo.PatchRiskLevel(name, req.Env, req.Level); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	resp.OK(c, h.Svc.RiskDictView())
}

func (h *Handler) DeleteRiskCommand(c *gin.Context) {
	if err := h.Repo.DeleteRiskCommand(c.Param("name")); err != nil {
		resp.Fail(c, resp.CodeInternalError, "删除失败")
		return
	}
	resp.OK(c, h.Svc.RiskDictView())
}

// ---------------------------------------------------------------- Approvals

// ApprovalChain returns the default approver chain (any authenticated user).
func (h *Handler) ApprovalChain(c *gin.Context) {
	resp.OK(c, gin.H{"chain": h.Svc.DefaultApprovers()})
}

func (h *Handler) ListApprovals(c *gin.Context) {
	scope := c.DefaultQuery("scope", "all")
	u := middleware.CurrentUser(c)
	aps, _ := h.Repo.ListApprovals(scope, u.ID)
	type apView struct {
		model.Approval
		Steps []model.ApprovalStep `json:"steps"`
	}
	out := []apView{}
	for _, a := range aps {
		steps, _ := h.Repo.StepsOf(a.ID)
		// Mask credentials for display; the real command stays in the DB for
		// execution after approval (a is a copy, so this doesn't touch storage).
		a.Command = sqlutil.RedactSecrets(a.Command)
		out = append(out, apView{Approval: a, Steps: steps})
	}
	resp.OK(c, out)
}

func (h *Handler) ApproveApproval(c *gin.Context) {
	res, err := h.Svc.DecideApproval(middleware.CurrentUser(c), pathID(c), true)
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "仅审批链成员可审批")
		return
	}
	if err == service.ErrAlreadyDecided {
		resp.Fail(c, resp.CodeBadRequest, "该工单已被处理,请刷新")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "审批失败")
		return
	}
	resp.OK(c, gin.H{"ok": true, "status": "approved", "result": res})
}

func (h *Handler) RejectApproval(c *gin.Context) {
	_, err := h.Svc.DecideApproval(middleware.CurrentUser(c), pathID(c), false)
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "仅审批链成员可驳回")
		return
	}
	if err == service.ErrAlreadyDecided {
		resp.Fail(c, resp.CodeBadRequest, "该工单已被处理,请刷新")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "驳回失败")
		return
	}
	resp.OK(c, gin.H{"ok": true, "status": "rejected"})
}

// ---------------------------------------------------------------- Notifications

func (h *Handler) ListNotifications(c *gin.Context) {
	u := middleware.CurrentUser(c)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	items, unread := h.Svc.ListNotifications(u.ID, limit)
	if items == nil {
		items = []model.Notification{}
	}
	resp.OK(c, dto.NotificationsResp{Items: items, Unread: unread})
}

func (h *Handler) MarkNotificationsRead(c *gin.Context) {
	u := middleware.CurrentUser(c)
	var req dto.MarkReadReq
	_ = c.ShouldBindJSON(&req)
	if err := h.Svc.MarkNotificationsRead(u.ID, req.IDs); err != nil {
		resp.Fail(c, resp.CodeInternalError, "标记失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// ---------------------------------------------------------------- MFA (TOTP)

func (h *Handler) MFASetup(c *gin.Context) {
	out, err := h.Svc.MFASetup(middleware.CurrentUser(c))
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "生成失败")
		return
	}
	resp.OK(c, out)
}

func (h *Handler) MFAEnable(c *gin.Context) {
	var req dto.MFACodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.MFAEnable(middleware.CurrentUser(c), req.Code); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "验证码错误,请重试")
		return
	}
	resp.OK(c, gin.H{"ok": true, "mfaEnabled": true})
}

func (h *Handler) MFADisable(c *gin.Context) {
	var req dto.MFACodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.MFADisable(middleware.CurrentUser(c), req.Code); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "验证码错误,请重试")
		return
	}
	resp.OK(c, gin.H{"ok": true, "mfaEnabled": false})
}

// ---------------------------------------------------------------- Users

func (h *Handler) ListUsers(c *gin.Context) {
	us, _ := h.Svc.UsersView()
	resp.OK(c, us)
}

func (h *Handler) PatchUser(c *gin.Context) {
	var req dto.UserPatchReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.PatchUser(pathID(c), req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "更新失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// CreateUser provisions an active account directly (admin), with an initial
// password and one or more roles — an alternative to invite-only onboarding.
func (h *Handler) CreateUser(c *gin.Context) {
	var req dto.UserCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	u, err := h.Svc.CreateUser(req)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "创建失败:邮箱可能已存在,或密码至少 8 位")
		return
	}
	resp.OK(c, u)
}

func (h *Handler) InviteUser(c *gin.Context) {
	var req dto.InviteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	u, err := h.Svc.Invite(req)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "邀请失败:邮箱可能已存在")
		return
	}
	resp.OK(c, u)
}

// SetUserPassword resets a user's password (admin).
func (h *Handler) SetUserPassword(c *gin.Context) {
	var req dto.AdminPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.AdminSetPassword(pathID(c), req.Password); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "重置失败:密码至少 6 位")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// ResetUserMFA unbinds a user's OTP (admin).
func (h *Handler) ResetUserMFA(c *gin.Context) {
	if err := h.Svc.AdminResetMFA(pathID(c)); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "解绑失败")
		return
	}
	resp.OK(c, gin.H{"ok": true, "mfaEnabled": false})
}

// BindUserMFA generates + binds a fresh OTP secret for a user (admin), returning
// the secret + otpauth URI to hand over.
func (h *Handler) BindUserMFA(c *gin.Context) {
	out, err := h.Svc.AdminBindMFA(pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "绑定失败")
		return
	}
	resp.OK(c, out)
}

// ---------------------------------------------------------------- Audit

func (h *Handler) ListAudit(c *gin.Context) {
	risk := c.DefaultQuery("risk", "all")
	since := service.RangeSince(c.Query("range"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	rows, _ := h.Svc.ListAudit(middleware.CurrentUser(c), risk, since, limit)
	resp.OK(c, rows)
}

func (h *Handler) ExportAudit(c *gin.Context) {
	risk := c.DefaultQuery("risk", "all")
	since := service.RangeSince(c.Query("range"))
	csv, _ := h.Svc.ExportCSV(middleware.CurrentUser(c), risk, since)
	c.Header("Content-Disposition", "attachment; filename=audit_export.csv")
	// Prepend a UTF-8 BOM so Excel renders CJK correctly.
	c.Data(200, "text/csv; charset=utf-8", append([]byte{0xEF, 0xBB, 0xBF}, []byte(csv)...))
}

// ---------------------------------------------------------------- Settings / Webhook

// isSecretKey reports whether a setting key holds a sensitive value that must
// never be returned to a client (M2/R3).
func isSecretKey(k string) bool {
	l := strings.ToLower(k)
	return strings.Contains(l, "secret") || strings.Contains(l, "password") || strings.Contains(l, "token")
}

// settingHasValue reports whether a stored setting holds a non-empty value
// (values are JSON-encoded, so an empty string is stored as `""`).
func settingHasValue(v string) bool { return v != "" && v != `""` }

func (h *Handler) GetSettings(c *gin.Context) {
	all, _ := h.Repo.AllSettings()
	// Never return secret values to the client. Instead omit them from `settings`
	// and expose a boolean presence map so the UI can show "已配置" and leave the
	// password input blank (blank on save == keep unchanged). Returning a masked
	// sentinel string here would break the client's JSON.parse and get echoed back
	// as "", wiping the real secret (R3).
	secretsSet := map[string]bool{}
	for k, v := range all {
		if isSecretKey(k) {
			secretsSet[k] = settingHasValue(v)
			delete(all, k)
		}
	}
	wh, _ := h.Repo.GetWebhook()
	webhookHasSecret := false
	if wh != nil {
		webhookHasSecret = wh.Secret != ""
		wh.Secret = "" // GetWebhook returns a fresh row; never expose the secret
	}
	resp.OK(c, gin.H{
		"settings": all, "webhook": wh, "strictMode": h.Svc.StrictMode.Load(),
		"secretsSet": secretsSet, "webhookHasSecret": webhookHasSecret,
	})
}

func (h *Handler) SaveSettings(c *gin.Context) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	for k, v := range body {
		if k == "strictMode" {
			if b, ok := v.(bool); ok {
				h.Svc.SetStrict(b)
			}
			continue
		}
		// A secret key with an empty value means "keep the existing secret" — the
		// client never receives the real value, so a blank field is not a clear
		// request (R3). Only a non-empty value replaces it.
		if isSecretKey(k) {
			if sv, ok := v.(string); ok && strings.TrimSpace(sv) == "" {
				continue
			}
		}
		s := toJSON(v)
		_ = h.Repo.SetSetting(k, s)
	}
	resp.OK(c, gin.H{"ok": true})
}

func (h *Handler) SaveWebhook(c *gin.Context) {
	var req dto.WebhookConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	wh, _ := h.Repo.GetWebhook()
	if wh == nil {
		wh = &model.WebhookConfig{}
	}
	if req.Endpoint != "" {
		wh.Endpoint = req.Endpoint
	}
	if req.Secret != "" {
		wh.Secret = req.Secret // empty = keep the existing secret (never returned to the client, R3)
	}
	if req.Events != "" {
		wh.Events = req.Events
	}
	if req.RetryMax > 0 {
		wh.RetryMax = req.RetryMax
	}
	wh.Enabled = req.Enabled
	if err := h.Repo.SaveWebhook(wh); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	resp.OK(c, wh)
}

func (h *Handler) TestWebhook(c *gin.Context) {
	ok, msg := h.Svc.Webhook.Test()
	resp.OK(c, gin.H{"ok": ok, "message": msg})
}

// TestLark sends a sample approval card to the configured Lark bot webhook.
func (h *Handler) TestLark(c *gin.Context) {
	ok, msg := h.Svc.Webhook.TestLark()
	resp.OK(c, gin.H{"ok": ok, "message": msg})
}

func (h *Handler) WebhookDeliveries(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	rows, _ := h.Repo.ListWebhookDeliveries(limit)
	resp.OK(c, rows)
}
