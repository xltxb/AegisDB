package handler

import (
	"fmt"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/chat"
	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/middleware"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/internal/service"
	"aegisdb/pkg/crypto"
	"aegisdb/pkg/resp"
	"aegisdb/pkg/sqlutil"
)

// encryptedSettingKeys hold secrets encrypted at rest. 审批魔方对接已移除(#137),这两个键
// 没有代码再读;留在这里是为了老调用方整包提交时,新写进去的值仍是密文而不是明文。
var encryptedSettingKeys = map[string]bool{
	"approval.external.token":          true,
	"approval.external.callbackSecret": true,
	"chat.lark.appSecret":              true,
	"chat.telegram.botToken":           true,
	"chat.telegram.proxyToken":         true,
	"sso.clientSecret":                 true,
}

// ---------------------------------------------------------------- Connections

func (h *Handler) ListConnections(c *gin.Context) {
	u := middleware.CurrentUser(c)
	cs, _ := h.Svc.AccessibleConnections(u)
	// 巡检失败的原文只给管理员。它是驱动的原始错误:MySQL 1045 带网关的出口 IP,
	// lib/pq 带 pg_hba 拒绝的 host/user/database,ORA-12514 带服务名 —— 标签范围内的
	// 普通角色能看到"连不上"就够了,不该顺带拿到这些。
	if !h.Svc.IsAdmin(u) {
		for i := range cs {
			cs[i].LastCheckErr = ""
		}
	}
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
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("创建失败:%s", err))
		return
	}
	// 连接口令不进审计,只记"带了口令"。
	h.Svc.AuditAdminAction(middleware.CurrentUser(c), fmt.Sprintf(
		"admin.connection.create id=%d name=%s engine=%s host=%s:%d env=%s policy=%s user=%s%s",
		conn.ID, conn.Name, conn.Engine, conn.Host, conn.Port, conn.Env, conn.Policy, conn.Username,
		passwordNote(req.Password, "口令已设置")))
	resp.OK(c, conn)
}

// passwordNote 是审计里关于口令的那半句:改了只说改了,永远不带值。
func passwordNote(pw, note string) string {
	if strings.TrimSpace(pw) == "" {
		return ""
	}
	return " " + note
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
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("更新失败:%s", err))
		return
	}
	replica := ""
	if req.ReplicaHost != nil {
		replica = " replica=" + strings.TrimSpace(*req.ReplicaHost)
	}
	h.Svc.AuditAdminAction(middleware.CurrentUser(c), fmt.Sprintf(
		"admin.connection.update id=%d name=%s engine=%s host=%s:%d env=%s policy=%s user=%s db=%s%s%s%s",
		conn.ID, conn.Name, conn.Engine, conn.Host, conn.Port, conn.Env, conn.Policy, conn.Username, conn.Database,
		replica, passwordNote(req.Password, "口令已修改"), passwordNote(req.ReplicaPassword, "从库口令已修改")))
	resp.OK(c, conn)
}

func (h *Handler) TestConnection(c *gin.Context) {
	conn, err := h.Repo.GetConnection(pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "连接不存在")
		return
	}
	// 探完就记,记在 CheckConnection 里 —— 这条规则只写一次(见 service/conncheck.go)。
	r := h.Svc.CheckConnection(conn)
	ok, msg := r.OK(), r.Msg
	if !ok {
		// 连不上就以**失败**回,不能用 code=0 包一个 ok:false 回去。
		//
		// 调用方(批量巡检、新建/编辑后的自动测试)判的都是"这次请求有没有抛错",
		// 一个 code=0 的信封会被一律当成成功 —— 于是即便探测真的失败了,界面照样
		// 显示"可连"。判定的依据必须和显示的结论是同一个东西。
		resp.Fail(c, resp.CodeBadRequest, msg)
		return
	}
	resp.OK(c, gin.H{"ok": true, "message": msg})
}

// SetDatabaseProject files ONE of a connection's databases under a project
// (projectId 0 = 取消归属). The unit is the database, not the connection: one
// instance routinely hosts databases owned by different teams, and filing at
// the instance level would force people to split instances along org lines.
func (h *Handler) SetDatabaseProject(c *gin.Context) {
	var req dto.DatabaseProjectReq
	_ = c.ShouldBindJSON(&req)
	if err := h.Svc.SetDatabaseProject(middleware.CurrentUser(c), pathID(c), req.Database, req.ProjectID); err != nil {
		resp.FailErr(c, resp.CodeBadRequest, err)
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

func (h *Handler) PatchConnection(c *gin.Context) {
	var req dto.ConnectionStatusReq
	_ = c.ShouldBindJSON(&req)
	id := pathID(c)
	actor := middleware.CurrentUser(c)
	var what string // 审计里记的那一项(工单 02 第 2 条)
	switch {
	case req.Tags != nil: // replace tags
		if err := h.Svc.SetConnectionTags(id, *req.Tags); err != nil {
			resp.Fail(c, resp.CodeInternalError, "保存标签失败")
			return
		}
		what = "tags"
	case req.Policy != nil: // set gateway policy
		if err := h.Svc.SetConnectionPolicy(id, *req.Policy); err != nil {
			resp.Fail(c, resp.CodeBadRequest, "网关策略无效")
			return
		}
		what = "policy"
	case req.BizQuery != nil: // 业务查询源标记
		if err := h.Svc.SetConnectionBizQuery(actor, id, *req.BizQuery); err != nil {
			resp.FailErr(c, resp.CodeBadRequest, err)
			return
		}
		// 业务查询源标记在 SetConnectionBizQuery 里已经自己留痕
	default: // status toggle / explicit set
		if err := h.Svc.ToggleConnection(id, req.Status); err != nil {
			if ie, ok := service.AsInputError(err); ok {
				resp.FailErr(c, resp.CodeBadRequest, ie)
				return
			}
			resp.Fail(c, resp.CodeBadRequest, "切换失败")
			return
		}
		what = "status"
	}
	conn, _ := h.Repo.GetConnection(id)
	if conn != nil && what != "" {
		val := map[string]string{"tags": conn.Tags, "policy": conn.Policy, "status": conn.Status}[what]
		h.Svc.AuditAdminAction(actor, fmt.Sprintf("admin.connection.%s id=%d name=%s value=%s", what, conn.ID, conn.Name, val))
	}
	resp.OK(c, conn)
}

// GetConnectionSchema returns a connection's database→table tree: introspected
// live from the target when the connection has real credentials, otherwise the
// seeded (simulated) tree. See Services.ConnectionSchema.
func (h *Handler) GetConnectionSchema(c *gin.Context) {
	resp.OK(c, h.Svc.ConnectionSchema(middleware.CurrentUser(c), pathID(c), c.Query("database")))
}

// GetTableColumns 一张表的列名(终端 Tab 补全)。?database= 是会话所在的库,?table= 可带限定名。
func (h *Handler) GetTableColumns(c *gin.Context) {
	cols, err := h.Svc.TableColumns(middleware.CurrentUser(c), pathID(c), c.Query("database"), c.Query("table"))
	switch {
	case err == nil:
		resp.OK(c, gin.H{"columns": cols})
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeBadRequest, "连接不存在")
	case errors.Is(err, service.ErrForbidden):
		resp.Fail(c, resp.CodeForbidden, "无权访问该连接")
	case errors.Is(err, gateway.ErrBadTableName):
		resp.Fail(c, resp.CodeBadRequest, "表名不合法")
	default:
		if ie, ok := service.AsInputError(err); ok { // 例如连接上的引擎已不再支持
			resp.FailErr(c, resp.CodeBadRequest, ie)
			return
		}
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("取列失败: %s", err))
	}
}

// GetConnectionObjects lists a database's programmable objects (functions /
// procedures / packages / triggers) for the terminal tree. `scope` is the
// database (MySQL), schema (PostgreSQL family) or owner (Oracle).
func (h *Handler) GetConnectionObjects(c *gin.Context) {
	resp.OK(c, h.Svc.ConnectionObjects(middleware.CurrentUser(c), pathID(c), c.Query("scope"), c.Query("database")))
}

// GetConnectionObjectSource returns one programmable object's source text.
func (h *Handler) GetConnectionObjectSource(c *gin.Context) {
	typ, name := c.Query("type"), c.Query("name")
	if typ == "" || name == "" {
		resp.Fail(c, resp.CodeBadRequest, "缺少 type / name 参数")
		return
	}
	r, err := h.Svc.ConnectionObjectSource(middleware.CurrentUser(c), pathID(c), c.Query("scope"), typ, name, c.Query("database"))
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "连接不存在")
		return
	}
	if err == service.ErrForbidden {
		resp.Fail(c, resp.CodeForbidden, "无权访问该连接")
		return
	}
	if err != nil {
		resp.FailErr(c, resp.CodeBadRequest, err)
		return
	}
	resp.OK(c, r)
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

// CreateRole 新建角色:什么权限都不带(矩阵全 deny、无菜单),由管理员随后逐项打开。
func (h *Handler) CreateRole(c *gin.Context) {
	var req dto.RoleCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	role, err := h.Svc.CreateRole(middleware.CurrentUser(c), req)
	if err != nil {
		if ie, ok := service.AsInputError(err); ok {
			resp.FailErr(c, resp.CodeBadRequest, ie)
			return
		}
		resp.Fail(c, resp.CodeInternalError, "创建失败")
		return
	}
	r, _ := h.Svc.RoleDetail(role.ID)
	resp.OK(c, r)
}

// DeleteRole 删一个自定义角色。有成员、被流程引用、内置角色都会被拒,原因原样回给界面。
func (h *Handler) DeleteRole(c *gin.Context) {
	err := h.Svc.DeleteRole(middleware.CurrentUser(c), pathID(c))
	switch {
	case err == nil:
		resp.OK(c, gin.H{"ok": true})
	case errors.Is(err, service.ErrNotFound):
		resp.Fail(c, resp.CodeNotFound, "角色不存在")
	default:
		if ie, ok := service.AsInputError(err); ok {
			resp.FailErr(c, resp.CodeBadRequest, ie)
			return
		}
		resp.Fail(c, resp.CodeInternalError, "删除失败")
	}
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
	approveBefore := role.CanApprove
	if req.CanApprove != nil {
		role.CanApprove = *req.CanApprove
	}
	if err := h.Repo.UpdateRole(role); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	// 留痕(2026-10 安全审计 L-AUTH1):"可审批"决定谁能签生产变更,是权限变更;从前改了不写审计。
	// 与菜单 / 能力矩阵的改动同一个口径(AuditRoleChange)。
	change := map[string]any{"name": role.Name, "defaultConnRole": role.DefaultConnRole}
	if approveBefore != role.CanApprove {
		change["canApprove"] = map[string]bool{"from": approveBefore, "to": role.CanApprove}
	}
	h.Svc.AuditRoleChange(middleware.CurrentUser(c), role.ID, "update", change)
	r, _ := h.Svc.RoleDetail(role.ID)
	resp.OK(c, r)
}

func (h *Handler) SetRoleMenus(c *gin.Context) {
	var req dto.MenusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	// 管理类菜单只给平台管理员角色(工单 03):勾给别的角色时拒绝,并说清是哪几项。
	// 关掉它们照常允许 —— 收回从来不该被挡。
	role, err := h.Repo.GetRole(pathID(c))
	if err != nil || role == nil {
		resp.Fail(c, resp.CodeBadRequest, "角色不存在")
		return
	}
	// 关掉 admin 角色的 perms 菜单,权限页就进不去了,而打开它正要进权限页 —— 只能直接改库。
	if on, ok := req.Menus["perms"]; ok && !on && role.Code == "admin" {
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("不能关闭平台管理员角色的「权限」菜单:关掉后没有人能再改回来"))
		return
	}
	if role.Code != "admin" {
		var bad []string
		for k, on := range req.Menus {
			if on && model.IsAdminOnlyMenu(k) {
				bad = append(bad, k)
			}
		}
		if len(bad) > 0 {
			sort.Strings(bad)
			resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("以下菜单仅平台管理员可用,不能授予其他角色:%s", strings.Join(bad, "、")))
			return
		}
	}
	if err := h.Repo.SetMenus(pathID(c), req.Menus); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	h.Svc.AuditRoleChange(middleware.CurrentUser(c), pathID(c), "menus", req.Menus)
	resp.OK(c, gin.H{"ok": true})
}

func (h *Handler) SetRoleCapabilities(c *gin.Context) {
	var req dto.CapabilitiesReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	// 不存在的角色:404,不写矩阵、不写审计(工单 02 第 13 条)。从前照写一份幽灵矩阵还回成功。
	if !h.roleExists(c) {
		return
	}
	if err := h.Repo.SetMatrix(pathID(c), req.Matrix); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	h.Svc.AuditRoleChange(middleware.CurrentUser(c), pathID(c), "capabilities", req.Matrix)
	resp.OK(c, gin.H{"ok": true})
}

// UserTags returns one user's direct data-access scope.
func (h *Handler) UserTags(c *gin.Context) {
	tags, err := h.Svc.UserTags(pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "用户不存在")
		return
	}
	resp.OK(c, tags)
}

// SetUserTags scopes one user's data access, overriding their role scope.
func (h *Handler) SetUserTags(c *gin.Context) {
	var req dto.RoleTagsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.SetUserTags(middleware.CurrentUser(c), pathID(c), req.Tags); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "保存失败")
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
	if !h.roleExists(c) {
		return
	}
	if err := h.Svc.SetRoleTags(pathID(c), req.Tags); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	h.Svc.AuditRoleChange(middleware.CurrentUser(c), pathID(c), "tags", req.Tags)
	r, _ := h.Svc.RoleDetail(pathID(c))
	resp.OK(c, r)
}

func (h *Handler) AddRoleMember(c *gin.Context) {
	var req dto.MemberReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	// 角色必须真的存在。一个不存在的角色 id 写进成员表,会让这个用户的每一格
	// 能力都读成"放行"——因为"查无规则行 = 放行"是这套矩阵的基础语义,而幽灵角色
	// 永远查无规则行。入口不挡,后面每一层都拦不住它。
	if _, rerr := h.Repo.GetRole(pathID(c)); rerr != nil {
		resp.Fail(c, resp.CodeBadRequest, "角色不存在")
		return
	}
	// 与 PATCH /users 同一道闸:被启用凭据绑定的账号 / 服务账号,加不上 admin。
	if target, uerr := h.Repo.GetUserByID(req.UserID); uerr == nil {
		if gerr := h.Svc.GuardAdminGrant(target, append(h.Repo.EffectiveRoleIDs(target), pathID(c))); gerr != nil {
			resp.FailErr(c, resp.CodeBadRequest, gerr)
			return
		}
	}
	if err := h.Repo.AddMember(pathID(c), req.UserID); err != nil {
		resp.Fail(c, resp.CodeInternalError, "添加失败")
		return
	}
	h.Svc.AuditRoleChange(middleware.CurrentUser(c), pathID(c), "member.add", req.UserID)
	r, _ := h.Svc.RoleDetail(pathID(c))
	resp.OK(c, r)
}

func (h *Handler) RemoveRoleMember(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Param("userId"), 10, 64)
	if !h.roleExists(c) {
		return
	}
	target, uerr := h.Repo.GetUserByID(userID)
	if uerr != nil {
		resp.Fail(c, resp.CodeNotFound, "用户不存在")
		return
	}
	err := h.Svc.RemoveRoleMember(pathID(c), userID)
	if d, ok := err.(*service.DisableRefusal); ok {
		resp.Fail(c, resp.CodeBadRequest, d.Reason)
		return
	}
	if err == service.ErrBadRequest {
		resp.Fail(c, resp.CodeBadRequest, "该用户仅剩此一个角色,请先分配其他角色再移除")
		return
	}
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeNotFound, "用户不存在")
		return
	}
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "移除失败")
		return
	}
	// 收回角色与授予角色同等重要,一样进审计(工单 02 第 2 条)。
	h.Svc.AuditRoleChange(middleware.CurrentUser(c), pathID(c), "member.remove", target.Email)
	r, _ := h.Svc.RoleDetail(pathID(c))
	resp.OK(c, r)
}

// roleExists 检查路径里的角色在不在;不在时已写好 40400 响应。
func (h *Handler) roleExists(c *gin.Context) bool {
	if r, err := h.Repo.GetRole(pathID(c)); err != nil || r == nil {
		resp.Fail(c, resp.CodeNotFound, "角色不存在")
		return false
	}
	return true
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
	if err := h.Svc.UpsertRiskCommand(req.Command, req.Tiers); err != nil {
		if ie, ok := service.AsInputError(err); ok {
			resp.FailErr(c, resp.CodeBadRequest, ie)
			return
		}
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	h.auditAdmin(c, "admin.riskcmd.upsert command="+strings.ToUpper(strings.TrimSpace(req.Command))+" tiers="+auditJSON(req.Tiers))
	resp.OK(c, h.Svc.RiskDictView())
}

func (h *Handler) PatchRiskCommand(c *gin.Context) {
	name := strings.ToUpper(c.Param("name"))
	var req dto.RiskCommandPatchReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.PatchRiskCommand(name, req.Tier, req.Level); err != nil {
		if errors.Is(err, service.ErrNotFound) {
			resp.FailErr(c, resp.CodeNotFound, apimsg.Errorf("字典里没有这条命令:%s", name))
			return
		}
		if ie, ok := service.AsInputError(err); ok {
			resp.FailErr(c, resp.CodeBadRequest, ie)
			return
		}
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	h.auditAdmin(c, "admin.riskcmd.patch command="+name+" tier="+req.Tier+" level="+req.Level)
	resp.OK(c, h.Svc.RiskDictView())
}

func (h *Handler) DeleteRiskCommand(c *gin.Context) {
	if err := h.Repo.DeleteRiskCommand(c.Param("name")); err != nil {
		resp.Fail(c, resp.CodeInternalError, "删除失败")
		return
	}
	h.auditAdmin(c, "admin.riskcmd.delete command="+strings.ToUpper(c.Param("name")))
	resp.OK(c, h.Svc.RiskDictView())
}

// ---------------------------------------------------------------- Approvals

// ApprovalChain returns the default approver chain (any authenticated user).
func (h *Handler) ApprovalChain(c *gin.Context) {
	resp.OK(c, gin.H{"chain": h.Svc.DefaultApprovers()})
}

func (h *Handler) ListApprovals(c *gin.Context) {
	u := middleware.CurrentUser(c)
	scope := c.DefaultQuery("scope", "all")
	// chain:与本人相关的那一份(即平台管理员升级成全量之前的「全部」)。总览的"等我审批 / 等我执行"
	// 只关心自己能动的单,取全量的前 20 条再筛,全局待审一多,管理员自己的待办就被挤出第一页
	// (2026-10-26 回归排查)。
	chain := scope == "chain"
	if scope != "mine" {
		scope = "all" // 只认 mine / all / chain;global 不是客户端能直接传的值
	}
	// 平台管理员的「全部」是全量工单(只读,canDecide 仍按审批链)—— 不在链上的管理员此前
	// 在总览与审批页上看到的待审批是 0(2026-10 黑盒,工单 01 第 7 条)。
	if scope == "all" && !chain && h.Svc.IsPlatformAdmin(u) {
		scope = repository.ApprovalScopeGlobal
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "50"))
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	// A ticket looked up by number is a single row wherever it happens to sit.
	apNo := strings.TrimSpace(c.Query("ap"))
	if apNo != "" {
		page, pageSize = 1, 1
	}
	// status 只认已知的四个状态。一个拼错的值若原样进 WHERE,查出来的是空列表 ——
	// 而空列表和"确实没有"长得一模一样,调用方看不出自己写错了。
	// type 同理,只认四个已知类型。它不是表上的一列,而是由 releaseId / windowId /
	// exportJobId 推出来的(见 model.ApprovalType)。
	typ := strings.TrimSpace(c.Query("type"))
	switch typ {
	case "", model.ApTypeCommand, model.ApTypeRelease, model.ApTypeWindow, model.ApTypeExport:
	default:
		resp.Fail(c, resp.CodeBadRequest, "type 取值无效")
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	switch status {
	case "", model.StatusPending, model.StatusApproved, model.StatusRejected, model.StatusExpired:
	default:
		resp.Fail(c, resp.CodeBadRequest, "status 取值无效")
		return
	}
	// 搜索词有上限:它进 LIKE,而一个几 KB 的模式只会让数据库白扫一遍全表。
	q := strings.TrimSpace(c.Query("q"))
	if len(q) > 128 {
		q = q[:128]
	}
	// 第三类可见性:已批准待执行、且落在他够得到的实例上的工单。执行权已经放开成
	// "谁能在这台实例上跑,谁就能执行",而看不见的单子没法接手。
	aps, total, err := h.Repo.ListApprovalsPaged(scope, u.ID, apNo, status, typ, q,
		(page-1)*pageSize, pageSize, h.Svc.AccessibleConnIDs(u))
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	// canDecide / blockReason 由服务端算好带下来。UI 照着它决定按钮亮不亮 ——
	// 让前端自己再判一遍规则,两份判断迟早会不一致。
	// canExecute 同理:通过之后命令并没有跑,要发起人自己来执行(见
	// service.ExecuteApproved)。哪张工单在等他,由服务端说了算 —— 前端照着这一位
	// 决定"执行"按钮亮不亮,而不是自己去拼 status / executedAt / 是不是发起人。
	type apView struct {
		model.Approval
		Steps       []model.ApprovalStep `json:"steps"`
		CanDecide   bool                 `json:"canDecide"`
		BlockReason string               `json:"blockReason"`
		// BlockReasonRef:BlockReason 的机器可读身份(apiMsg),界面按读者语言显示
		BlockReasonRef *model.RuleRef `json:"blockReasonRef,omitempty"`
		CanExecute  bool                 `json:"canExecute"`
		// canCancel:发起人自己撤回。与 canDecide 分开算 —— 撤回不是"决定",
		// 审批人手里的动作是驳回(见 service.canCancel 的注释)。
		CanCancel bool `json:"canCancel"`
		// Type 由服务端推,不让前端照着三个外键再推一遍:筛选的 WHERE 在服务端,
		// 标签若按另一份推断来画,迟早会出现"筛出来的行挂着别的标签"。
		Type string `json:"type"`
	}
	out := []apView{}
	for _, a := range aps {
		steps, _ := h.Repo.StepsOf(a.ID)
		// Mask credentials for display; the real command stays in the DB for
		// execution after approval (a is a copy, so this doesn't touch storage).
		a.Command = sqlutil.RedactSecrets(a.Command)
		row := a // DecideBlockFor 读的是这一行,不能把循环变量的地址交出去
		block := h.Svc.DecideBlockFor(u, &row)
		out = append(out, apView{
			Approval: a, Steps: steps,
			CanDecide: block == service.BlockNone, BlockReason: block.Reason(), BlockReasonRef: apimsg.Lookup(block.Reason()),
			CanExecute: h.Svc.CanExecuteApproved(u, &row),
			CanCancel:  h.Svc.CanCancelApproval(u, &row),
			Type:       model.ApprovalType(&row),
		})
	}
	pending, _ := h.Repo.CountPendingApprovals(scope, u.ID)
	resp.OK(c, gin.H{"items": out, "total": total, "pending": pending, "page": page, "pageSize": pageSize})
}

// todoPreview 是待办面板里一行能放下的东西。命令截短、脱敏 —— 面板是给人扫一眼的,
// 完整的命令在工单详情里。
const todoPreview = 160

// TodoApprovals 回答"此刻有哪些工单在等我":
//
//   - decide:待审批,且我**此刻**能决定(在链上、不是自己发起的 —— DecideBlockFor);
//   - execute:已批准、还没跑、我能执行(CanExecuteApproved);
//   - confirm:发布单停在人工闸上等我点「继续」(mayConfirmGate)。它不是审批工单,
//     但对人来说是同一件事 —— 有东西停在那里等我动手。
//
// 它和 ListApprovals 的 pending 不是一回事:pending 数的是"我看得见的待审批",里面
// 有我自己发起、正在等别人审的单子 —— 那不是我的待办,把它算进"待办"会让人去找
// 一个根本没有的按钮。
//
// 判定逐张走 service 的同一条规则,不在这里另写一份(见 approval_decidability.go 开头)。
//
// 三类各自还要过**菜单闸**:动作端点分别挂在 approve / terminal / pipeline 上,一个够不到
// 那个菜单的人点下去只会拿到「无菜单权限」。待办的全部意义是"这条我此刻动得了",列一条
// 动不了的进去,人会学着无视整枚徽章 —— 那比不列更糟。
//
// (够得到菜单、却被路由到一道自己没有菜单的闸上,是一种**配置错误**:那条流水线会永远
// 停在那儿而没人被通知。这里只做到不谎报;真正的修法在 validateConfirmRole —— 见
// .scratch/aegisdb/issues/25。)
func (h *Handler) TodoApprovals(c *gin.Context) {
	u := middleware.CurrentUser(c)
	decideRows, execRows, err := h.Repo.TodoCandidates(u.ID, h.Svc.AccessibleConnIDs(u))
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	menus, _ := h.Repo.MenusForRoles(h.Repo.EffectiveRoleIDs(u))
	type item struct {
		ID   int64  `json:"id"`
		ApNo string `json:"apNo"` // 工单号,或发布单号(confirm 那一类)
		// decide = 等我审批;execute = 批过了等我执行;confirm = 发布单停在人工闸等我放行
		Kind      string `json:"kind"`
		Initiator string `json:"initiator"`
		// InitiatorID 是**发起人是谁**,不是显示用的那个名字。
		//
		// 界面要按"这张单是不是我自己提的"分出一档(登录提示音只对发起人响),而按姓名比
		// 在同名的两个人身上会悄悄比错 —— 那种错不会报任何东西,只是某个人莫名其妙地听
		// 不到、或者莫名其妙地听到。
		InitiatorID int64 `json:"initiatorId"`
		Instance  string `json:"instance"`
		Database  string `json:"database"`
		RiskLevel string `json:"riskLevel"`
		Command   string `json:"command"`
		// CommandRef 是描述类单子(执行窗口)的机器可读描述,界面按读者语言显示,见 model.RenderApText。
		CommandRef *model.RuleRef `json:"commandRef,omitempty"`
		// Stage 只在 confirm 那一类有值:停在**哪一道闸**上(manual / execute)。没有它,
		// 一条「等你确认」说不出人要去确认什么 —— 是确认继续,还是确认执行。
		//
		// 送的是**类型**而不是阶段名。阶段名是按类型派生的规范中文(见
		// service.stageTypeLabel 的注释:它要进记录,所以语言必须稳定),界面按读者的
		// 语言渲染同一个类型 —— 把那份中文直接摆到界面上,英文界面就会夹一句中文。
		Stage     string    `json:"stage,omitempty"`
		CreatedAt time.Time `json:"createdAt"`
	}
	items := []item{}
	clip := func(raw string) string {
		cmd := strings.Join(strings.Fields(sqlutil.RedactSecrets(raw)), " ")
		if r := []rune(cmd); len(r) > todoPreview {
			// 按 rune 截,别把一个多字节字符切成半个。
			cmd = string(r[:todoPreview]) + "…"
		}
		return cmd
	}
	add := func(a model.Approval, kind string) {
		items = append(items, item{
			ID: a.ID, ApNo: a.ApNo, Kind: kind, Initiator: a.Initiator, InitiatorID: a.InitiatorID,
			Instance: a.Instance, Database: a.Database, RiskLevel: a.RiskLevel,
			Command: clip(a.Command), CommandRef: a.CommandRef, CreatedAt: a.CreatedAt,
		})
	}
	decide, execute := 0, 0
	if menus["approve"] {
		for _, a := range decideRows {
			row := a
			if h.Svc.DecideBlockFor(u, &row) == service.BlockNone {
				decide++
				add(a, "decide")
			}
		}
	}
	if menus["terminal"] {
		for _, a := range execRows {
			row := a
			if h.Svc.CanExecuteApproved(u, &row) {
				execute++
				add(a, "execute")
			}
		}
	}
	// 第三类:发布单停在人工闸上等他放行。它不是审批工单,所以不走上面那两条路 ——
	// 但对人来说是同一件事:有东西停在那里等我动手(见 service/release_confirm.go)。
	confirm := 0
	for _, tdo := range confirmTodosFor(h, u, menus) {
		confirm++
		items = append(items, item{
			ID: tdo.Release.ID, ApNo: tdo.Release.RelNo, Kind: "confirm",
			Initiator: tdo.Release.Creator, InitiatorID: tdo.Release.CreatorID, Instance: tdo.Release.Instance,
			Database: tdo.Release.Database, RiskLevel: tdo.Release.Risk,
			Command: clip(tdo.Release.SQL), Stage: tdo.Stage.Type,
			CreatedAt: tdo.Release.CreatedAt,
		})
	}
	resp.OK(c, gin.H{"decide": decide, "execute": execute, "confirm": confirm, "items": items})
}

// CancelApproval —— 发起人撤回自己的待审批工单。
func (h *Handler) CancelApproval(c *gin.Context) {
	err := h.Svc.CancelApproval(middleware.CurrentUser(c), pathID(c))
	switch {
	case err == nil:
		resp.OK(c, gin.H{"ok": true})
	case err == service.ErrNotFound:
		resp.Fail(c, resp.CodeBadRequest, "工单不存在")
	case err == service.ErrAlreadyDecided:
		resp.Fail(c, resp.CodeBadRequest, "该工单已被处理,请刷新")
	case err == service.ErrForbidden:
		resp.Fail(c, resp.CodeForbidden, "无权撤回")
	default:
		// canCancel 的拒绝理由是要给人看的:三种情况要人去做的事完全不同。
		resp.FailErr(c, resp.CodeForbidden, err)
	}
}

func (h *Handler) ApproveApproval(c *gin.Context) {
	res, err := h.Svc.DecideApproval(middleware.CurrentUser(c), pathID(c), true)
	if r, ok := err.(*service.DecideRefusal); ok {
		resp.FailErr(c, resp.CodeForbidden, r)
		return
	}
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
	if r, ok := err.(*service.DecideRefusal); ok {
		resp.FailErr(c, resp.CodeForbidden, r)
		return
	}
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
	// 请求体可选:有本地口令的账号要带上当前登录密码(L-AUTH4);第一次可以不带,回 42801 再要
	var req struct {
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&req)
	out, err := h.Svc.MFASetup(middleware.CurrentUser(c), req.Password)
	switch {
	case err == service.ErrForbidden:
		resp.Fail(c, resp.CodeForbidden, "已启用二次验证,如需重新绑定请先关闭,或联系管理员重置")
		return
	case errors.Is(err, service.ErrCurrentPasswordRequired):
		resp.Fail(c, resp.CodePasswordRequired, "请输入当前登录密码")
		return
	case errors.Is(err, service.ErrSSOReauthRequired):
		// 同一个 42801(前端照旧给出口令框,知道口令的人仍可输入),另带 ssoReauth 让界面
		// 直接说明"请重新 SSO 登录"(2026-10-26 回归 03)。
		resp.FailData(c, resp.CodePasswordRequired, "SSO 登录已超过 15 分钟,请退出后重新通过 SSO 登录再绑定二次验证",
			gin.H{"ssoReauth": true})
		return
	case errors.Is(err, service.ErrCurrentPasswordWrong):
		resp.Fail(c, resp.CodePasswordRequired, "当前登录密码错误")
		return
	case errors.Is(err, service.ErrMFALocked):
		resp.Fail(c, resp.CodeTooManyReqs, mfaLockedMsg)
		return
	}
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
		if errors.Is(err, service.ErrMFALocked) {
			resp.Fail(c, resp.CodeTooManyReqs, mfaLockedMsg)
			return
		}
		resp.Fail(c, resp.CodeBadRequest, "验证码错误,请重试")
		return
	}
	resp.OK(c, gin.H{"ok": true, "mfaEnabled": false})
}

// ---------------------------------------------------------------- Users

func (h *Handler) ListUsers(c *gin.Context) {
	us, _ := h.Svc.UsersView(middleware.CurrentUser(c))
	resp.OK(c, us)
}

func (h *Handler) PatchUser(c *gin.Context) {
	var req dto.UserPatchReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.PatchUser(middleware.CurrentUser(c), pathID(c), req); err != nil {
		// 停用被拦下时,理由原样透出:"换个管理员来点"和"先指派一位管理员"要人做的
		// 事完全不同,一句"更新失败"会让人去修错的东西 —— 或者以为是系统坏了。
		if d, ok := err.(*service.DisableRefusal); ok {
			resp.FailErr(c, resp.CodeForbidden, d)
			return
		}
		if g, ok := err.(*service.AdminGrantRefusal); ok {
			resp.FailErr(c, resp.CodeBadRequest, g)
			return
		}
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
		if errors.Is(err, service.ErrBizNoLocalPassword) {
			resp.Fail(c, resp.CodeBadRequest, "创建失败:只有业务查询角色的账号不设本地口令 —— 它由外部审批下发授权时自动开通,业务方通过 SSO 登录进入网关")
			return
		}
		if ie, ok := service.AsInputError(err); ok {
			resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("创建失败:%s", ie))
			return
		}
		resp.Fail(c, resp.CodeBadRequest, "创建失败")
		return
	}
	// 建号是权限变更:一个能登录的账户从无到有。口令不进审计。
	h.Svc.AuditAdminAction(middleware.CurrentUser(c), "admin.user.create user="+u.Email+" roleIds="+joinIDs(req.RoleIDs))
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
		if ie, ok := service.AsInputError(err); ok {
			resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("邀请失败:%s", ie))
			return
		}
		resp.Fail(c, resp.CodeBadRequest, "邀请失败")
		return
	}
	h.Svc.AuditAdminAction(middleware.CurrentUser(c), "admin.user.invite user="+u.Email+" roleId="+strconv.FormatInt(req.RoleID, 10))
	resp.OK(c, u)
}

// joinIDs 把一组 id 写成审计里的 "1|2|3"(与 admin.user.patch 的 roleIds 同一写法)。
func joinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, "|")
}

// SetUserPassword resets a user's password (admin).
func (h *Handler) SetUserPassword(c *gin.Context) {
	var req dto.AdminPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	if err := h.Svc.AdminSetPassword(middleware.CurrentUser(c), pathID(c), req.Password); err != nil {
		if errors.Is(err, service.ErrBizNoLocalPassword) {
			resp.Fail(c, resp.CodeBadRequest, "重置失败:业务查询账号不设本地口令,业务方通过 SSO 登录进入网关")
			return
		}
		if ie, ok := service.AsInputError(err); ok {
			resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("重置失败:%s", ie))
			return
		}
		resp.Fail(c, resp.CodeBadRequest, "重置失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// ResetUserMFA unbinds a user's OTP (admin).
func (h *Handler) ResetUserMFA(c *gin.Context) {
	if err := h.Svc.AdminResetMFA(middleware.CurrentUser(c), pathID(c)); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "解绑失败")
		return
	}
	resp.OK(c, gin.H{"ok": true, "mfaEnabled": false})
}

// BindUserMFA generates + binds a fresh OTP secret for a user (admin), returning
// the secret + otpauth URI to hand over.
func (h *Handler) BindUserMFA(c *gin.Context) {
	out, err := h.Svc.AdminBindMFA(middleware.CurrentUser(c), pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "绑定失败")
		return
	}
	resp.OK(c, out)
}

// ---------------------------------------------------------------- Audit

// auditEventQuery reads the ?event= filter and refuses a value nobody could have
// written. 认不出就报错,不是原样进 WHERE —— 查出来会是空列表,而空列表和"确实没有"
// 长得一模一样,调用方看不出自己写错了(与 status / type 同一条原则)。
//
// **多选**:逗号分隔("intercept,exec")。空串与 "all" 都表示不筛,单个取值照旧。
func auditEventQuery(c *gin.Context) ([]string, bool) {
	return auditMultiQuery(c, "event", model.IsAuditEventType)
}

// auditActionQuery 读 ?action= 这个筛选,并挡掉认不出的取值。与 event 同一条原则:
// 原样进 WHERE 查出来是空列表,而空列表和"确实没有"长得一模一样。
//
// 同样支持逗号分隔的多选。
func auditActionQuery(c *gin.Context) ([]string, bool) {
	return auditMultiQuery(c, "action", gateway.IsDataAction)
}

// auditSourceQuery 读 ?source= 这个筛选(多选、逗号分隔),认不出的取值报错 —— 与 event /
// action 同一条原则。"unknown" 合法:筛 0063 之前、没记来源的旧行。
func auditSourceQuery(c *gin.Context) ([]string, bool) {
	return auditMultiQuery(c, "source", model.IsAuditSourceFilter)
}

// auditMultiQuery 是上面几个共用的读法:逗号切开、逐个过白名单、去重。
//
// 一条都认不出才报错,而不是"有一个不认得就整条拒绝"?——不,反过来:**任何一个**认不
// 出都报错。半懂半不懂地筛,给出的结果比报错更难发现是错的。
func auditMultiQuery(c *gin.Context, key string, valid func(string) bool) ([]string, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" || raw == "all" {
		return nil, true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		v := strings.TrimSpace(part)
		if v == "" || seen[v] {
			continue
		}
		if !valid(v) {
			resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("%s 取值无效", key))
			return nil, false
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, true
}

// auditFilterQuery 把一整串查询参数读成一组筛选条件。
//
// 列表与导出共用它 —— 人是看着筛过的列表按下导出的,两边各读各的参数,迟早会导出一份
// 他没在看的东西。
func auditFilterQuery(c *gin.Context) (repository.AuditFilter, bool) {
	events, ok := auditEventQuery(c)
	if !ok {
		return repository.AuditFilter{}, false
	}
	actions, ok := auditActionQuery(c)
	if !ok {
		return repository.AuditFilter{}, false
	}
	sources, ok := auditSourceQuery(c)
	if !ok {
		return repository.AuditFilter{}, false
	}
	since, until := service.AuditWindow(c.Query("range"), c.Query("from"), c.Query("to"))
	return repository.AuditFilter{
		Risk:     c.DefaultQuery("risk", "all"),
		Events:   events,
		Actions:  actions,
		Sources:  sources,
		Instance: strings.TrimSpace(c.Query("instance")),
		// 操作人 / IP 的模糊词。长度掐在 128:再长也不会是一个人名或一个地址,而 LIKE
		// 的代价是跟着串长走的。
		Keyword: clip(strings.TrimSpace(c.Query("q")), 128),
		Since:   since,
		Until:   until,
		// mine=1:用户后台的「我的操作记录」,谁来都只看自己的(管理后台拆分,工单 04)
		// 管理员 / 审计员在管理后台白名单外、或管理后台已空闲超时时,也只给自己的(见 middleware.AuditListGate)
		OnlyOwn: c.Query("mine") == "1" || middleware.AuditOwnOnly(c),
	}, true
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// AuditInstances godoc
// @Summary 审计筛选用的实例清单
// @Router  /audit/instances [get]
//
// 只列这个人在审计里**见得到的**实例:照着连接表列的话,下拉里会有他够不到的实例,而
// 一个筛出来永远是空的选项,读起来像"这台机器上什么都没发生过"。
func (h *Handler) AuditInstances(c *gin.Context) {
	out, err := h.Svc.AuditInstancesFor(middleware.CurrentUser(c), c.Query("mine") == "1" || middleware.AuditOwnOnly(c))
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, out)
}

// auditVerifyWindow 是界面按一下时验多少行。
//
// 全表校验是给命令行的(`aegisdb verify-audit`,几十万行慢慢走);界面上那一下要
// 在几秒内有结果,所以只验最近这一段。这依然是有意义的:每一行的哈希覆盖它自己的内容加
// 上前一行的哈希,窗口内的任何改动都跑不掉 —— 跑掉的只是更早的行,而返回里如实说了验过
// 多少、一共多少。
const auditVerifyWindow = 5000

// VerifyAudit godoc
// @Summary 校验审计哈希链(最近一段)
// @Router  /audit/verify [get]
//
// 限**监督角色**(admin/owner/audit)。校验要扫的是全表 —— 链是一条,少看几行就验不成 ——
// 所以它天然越过了"没有监督角色只看得见自己"那条线,门槛得单独立,不能靠可见性收窄代劳。
// 它也是一趟全表扫描,不该是随便谁都能按着玩的按钮。
func (h *Handler) VerifyAudit(c *gin.Context) {
	if !h.Svc.CanVerifyAuditChain(middleware.CurrentUser(c)) {
		resp.Fail(c, resp.CodeForbidden, "无权校验:审计链校验限监督角色(管理员 / 负责人 / 审计员)")
		return
	}
	rep, err := h.Svc.VerifyAuditChain(auditVerifyWindow)
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "校验失败")
		return
	}
	// 机器读的取值照原样带上,再附一句人话 —— 前端不该照着三个取值各写一份文案,
	// 而"链断了"和"内容被改"要人做的事不同,必须说清是哪一种。
	reason := service.AuditChainReason(rep.BadReason)
	resp.OK(c, gin.H{"report": rep, "reasonText": reason, "reasonRef": apimsg.Lookup(reason)})
}

func (h *Handler) ListAudit(c *gin.Context) {
	f, ok := auditFilterQuery(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "100"))
	if pageSize < 1 || pageSize > 500 {
		pageSize = 100
	}
	list := h.Svc.ListAuditPaged
	if c.Query("group") == "approval" { // 同一张审批单的操作合成一条
		list = h.Svc.ListAuditGrouped
	}
	res, err := list(middleware.CurrentUser(c), f, page, pageSize)
	if err != nil {
		// 曾经是 res, _ := …:查询失败被吞掉,照样 OK 回一个空页。于是生产上 LIKE 语法
		// 错误的表现不是"查询失败",而是"这个人没有任何记录" —— 一个会说谎的空列表,
		// 比报错糟得多。审批列表那条路一直是正确处理的,这里对齐。
		slog.Error("audit list failed", "err", err)
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, res)
}

// AuditTrail 一张审批单的全部操作记录(按时间先后),审计列表里点开合并条目时用。
func (h *Handler) AuditTrail(c *gin.Context) {
	rows, err := h.Svc.AuditTrail(middleware.CurrentUser(c), c.Param("apNo"))
	if err != nil {
		slog.Error("audit trail failed", "err", err)
		resp.Fail(c, resp.CodeInternalError, "加载失败")
		return
	}
	resp.OK(c, rows)
}

func (h *Handler) ExportAudit(c *gin.Context) {
	// 导出用的是和列表**同一组筛选条件**(同一个 auditFilterQuery)。
	f, ok := auditFilterQuery(c)
	if !ok {
		return
	}
	c.Header("Content-Disposition", "attachment; filename=audit_export.csv")
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Status(200)
	// Prepend a UTF-8 BOM so Excel renders CJK correctly.
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	// 流式写出(M9):响应已经开始写,中途出错只能记日志 —— 下载到的文件会是截断的
	if err := h.Svc.ExportCSVTo(c.Writer, middleware.CurrentUser(c), f); err != nil {
		slog.Error("audit export failed", "err", err)
	}
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
		"settings": all, "webhook": wh,
		"secretsSet": secretsSet, "webhookHasSecret": webhookHasSecret,
		// 能订阅哪些,由服务端说了算 —— 界面自己列一份的话,只要有一处对不上就是静默失效。
		"webhookActions": gateway.DataActions,
		// 部署配置拉了管理后台 IP 白名单的总闸:设置页据此提示"这里的白名单暂不生效"
		"adminIpAllowlistForcedOff": middleware.AdminIPAllowlistForcedOff,
	})
}

func (h *Handler) SaveSettings(c *gin.Context) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	// 逐键校验:未知键、非法值,整次拒绝、一项不存(工单 02 第 7 条,见 settings_validate.go)。
	values, verr := validateSettings(body, func(k string) string { old, _ := h.Repo.GetSetting(k); return old })
	if verr != nil {
		resp.FailErr(c, resp.CodeBadRequest, verr)
		return
	}
	// 聊天审批机器人:启用时必填项要齐,不齐这次一项都不存。
	chatStored, err := h.Repo.SettingsByPrefix("chat.")
	if err != nil {
		slog.Error("read chat settings failed", "err", err)
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	if err := checkChatSettings(values, chatStored); err != nil {
		resp.FailErr(c, resp.CodeBadRequest, err)
		return
	}
	// 先把要加密的都加好再开始存:加密不成就一项都不存,绝不退回去存明文。
	for k := range encryptedSettingKeys {
		sv, ok := values[k].(string)
		if !ok || strings.TrimSpace(sv) == "" {
			continue
		}
		enc, err := crypto.EncryptSecret(sv)
		if err != nil {
			slog.Error("encrypt setting failed", "key", k, "err", err)
			resp.Fail(c, resp.CodeInternalError, "保存失败")
			return
		}
		values[k] = enc
	}
	// SSO 那几项决定"谁能绕开 SSO、谁会被挡在门外",存之前整体校验一遍:任何一项不对,
	// 这一次一项都不存 —— 半存半不存的 SSO 配置,比没配更难查。
	ssoChanged, ssoErr := h.checkSSOSettings(body)
	if ssoErr != nil {
		resp.FailErr(c, resp.CodeBadRequest, ssoErr)
		return
	}
	// 业务方数据查询的全局值:叠到当前值上整体校验,任何一项不对,这次一项都不存。
	bizChange, berr := h.Svc.CheckBizQuerySettings(body)
	if berr != nil {
		resp.FailErr(c, resp.CodeBadRequest, berr)
		return
	}
	// 审计只记**真的变了**的键(工单 02 第 2 条):设置页每次保存都把几十个键整包发来,全记下来
	// 等于什么都没记。secret 类只记键名与"已修改 / 已清除",值永远不进审计。
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// 先算好要写的,再在一个事务里一起写:写到一半失败,或者连接管理恰好在中途来读,都不能看到新旧
	// 混着的一份(新的 App Secret 配旧的地区,密钥就发去了另一个平台)。
	writes := map[string]string{}
	var changed []string
	for _, k := range keys {
		v := values[k]
		// "无 WHERE 的 DELETE / UPDATE" 不再是这里的全局开关,它按分层存在 tbl_env_tier
		// 上(迁移 0030)。旧客户端可能还在发这个键,静默丢弃 —— 若把它写进 tbl_setting,
		// 界面上会出现一个看着像开关、其实什么也不控制的东西。
		if k == "strictMode" {
			continue
		}
		old, _ := h.Repo.GetSetting(k)
		if isSecretKey(k) {
			// 显式 null = 清除(工单 02 第 8 条)。从前 "" 与 null 都表示"不改",存上的密钥
			// 就再也清不掉了。
			if _, clear := v.(settingClear); clear {
				if settingHasValue(old) {
					writes[k] = `""`
					changed = append(changed, k+"=已清除")
				}
				continue
			}
			// A secret key with an empty value means "keep the existing secret" — the
			// client never receives the real value, so a blank field is not a clear
			// request (R3). Only a non-empty value replaces it.
			if sv, ok := v.(string); ok && strings.TrimSpace(sv) == "" {
				continue
			}
			writes[k] = toJSON(v)
			changed = append(changed, k+"=已修改")
			continue
		}
		s := toJSON(v)
		writes[k] = s
		if s != old {
			if k == service.SettingBrandLogo {
				// LOGO 是几百 KB 的 data URI,截 200 字只剩一段 base64,什么也说明不了。
				// 记大小和摘要:能对上"换成了哪一张"。
				changed = append(changed, k+"="+brandLogoAuditNote(v))
				continue
			}
			changed = append(changed, k+"="+clip(s, 200))
		}
	}
	if err := h.Repo.SetSettings(writes); err != nil {
		slog.Error("save settings failed", "err", err)
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	if len(changed) > 0 {
		h.auditAdmin(c, "admin.settings.update "+clip(strings.Join(changed, " "), 2000))
	}
	if slices.ContainsFunc(changed, func(s string) bool { return strings.HasPrefix(s, "chat.") }) {
		h.Svc.ReloadChat() // 马上生效,不等轮询
	}
	if ssoChanged != "" {
		// 开关 SSO、改本地登录范围是权限级别的变更:谁能从哪扇门进来。进审计链。
		h.Svc.AuditAdminAction(middleware.CurrentUser(c), "settings.sso "+ssoChanged)
	}
	if bizChange != "" {
		h.Svc.AuditSettingsChange(middleware.CurrentUser(c), "settings.bizquery "+bizChange)
	}
	resp.OK(c, gin.H{"ok": true})
}

// checkSSOSettings 把本次提交里的 sso.* 叠到当前设置上,整体校验。返回变更摘要(无变更为空)。
func (h *Handler) checkSSOSettings(body map[string]any) (string, error) {
	touched := false
	for k := range body {
		if strings.HasPrefix(k, "sso.") {
			touched = true
			break
		}
	}
	if !touched {
		return "", nil
	}
	cur := h.Svc.SSOConfig()
	// SSOConfig 在地址为空时会把 Enabled 读成 false;校验要看的是"存进去之后"的样子,
	// 所以开关按原始值取。
	next := cur
	next.Enabled = settingBoolRaw(h, service.SettingSSOEnabled)
	str := func(k string, dst *string) {
		if v, ok := body[k]; ok {
			if sv, ok := v.(string); ok {
				*dst = strings.TrimSpace(sv)
			}
		}
	}
	if v, ok := body[service.SettingSSOEnabled]; ok {
		b, isBool := v.(bool)
		if !isBool {
			return "", errors.New("SSO 开关必须是布尔值")
		}
		next.Enabled = b
	}
	str(service.SettingSSOIssuer, &next.Issuer)
	str(service.SettingSSOClientID, &next.ClientID)
	str(service.SettingSSORedirectURL, &next.RedirectURL)
	str(service.SettingSSOEmailClaim, &next.EmailClaim)
	if err := service.ValidateSSOSettings(next, service.AllowInsecureSSO); err != nil {
		return "", err
	}
	// Client Secret 不进审计,只记它在这次提交里有没有动。
	secretNote := ""
	if v, ok := body[service.SettingSSOClientSecret]; ok {
		if sv, isStr := v.(string); !isStr || strings.TrimSpace(sv) != "" {
			secretNote = " clientSecret=已修改"
		}
	}
	return fmt.Sprintf("enabled=%v issuer=%s clientId=%s redirectUrl=%s emailClaim=%s%s",
		next.Enabled, next.Issuer, next.ClientID, next.RedirectURL, next.EmailClaim, secretNote), nil
}

// chatRequired 是各渠道开着时必须有的设置。
var chatRequired = map[chat.Channel][]struct {
	key     string
	missing error
}{
	chat.Lark: {
		{"appId", errors.New("启用 Lark 审批机器人必须填写 App ID")},
		{"appSecret", errors.New("启用 Lark 审批机器人必须填写 App Secret")},
	},
	chat.Telegram: {
		{"botToken", errors.New("启用 Telegram 审批机器人必须填写 Bot Token")},
	},
}

// checkChatSettings 按「存进去之后」的样子核对聊天审批机器人:开着的渠道必填项要齐。
//
// 这次没改到的渠道不核对:设置页每次整包发回来,库里一份坏配置不该挡住改别的设置。关着的不核对,所以关掉
// 永远放行。
func checkChatSettings(values map[string]any, stored map[string]string) error {
	for _, ch := range chat.Channels {
		if err := checkChatChannel(ch, values, stored); err != nil {
			return err
		}
	}
	return nil
}

func checkChatChannel(ch chat.Channel, values map[string]any, stored map[string]string) error {
	prefix := "chat." + string(ch) + "."
	next, changed := map[string]string{}, false
	for k, v := range stored {
		next[k] = v
	}
	for k, v := range values {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		raw := toJSON(v)
		if isSecretKey(k) {
			if sv, ok := v.(string); ok && sv == "" {
				continue // 不改
			}
			if _, clear := v.(settingClear); clear {
				raw = `""`
			}
		}
		changed = changed || raw != stored[k]
		next[k] = raw
	}
	if !changed || next[prefix+"enabled"] != "true" {
		return nil
	}
	for _, r := range chatRequired[ch] {
		if !settingHasValue(next[prefix+r.key]) {
			return r.missing
		}
	}
	return nil
}

func settingBoolRaw(h *Handler, key string) bool {
	v, err := h.Repo.GetSetting(key)
	return err == nil && strings.TrimSpace(v) == "true"
}

// 能订阅的,是**审计行上「增删改查」那一列的取值**(gateway.DataActions),不另列一份。
//
// 为什么是这一维而不是事件类型:一个把审计流接到 SIEM 的人想说的通常是"所有删除都推给我"。
// 按事件类型订阅答不了这句话 —— 被拦下来的 DROP 和执行掉的 DROP 是两个类型、同一件事。
//
// 白名单从那一列派生,界面上的清单也由服务端下发(见 GetSettings):同一组字符串各写一遍的话,
// 只要有一处对不上,表现都是**静默的** —— 勾得到、存下来、永远不推。
func isWebhookAction(a string) bool { return gateway.IsDataAction(a) }

// normalizeEvents cleans a comma-separated subscription list: trims/lowercases
// each entry, drops blanks/unknowns and de-duplicates, preserving order. This
// keeps the stored list tidy so the delivery-time type filter stays predictable.
func normalizeEvents(raw string) string {
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		e := strings.ToLower(strings.TrimSpace(part))
		if e == "" || !isWebhookAction(e) || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return strings.Join(out, ",")
}

// normalizeEnvs 整理推送环境清单:去空白、去重、去掉不存在的环境,保持原顺序。
// 环境代码区分大小写(是 tbl_environment 的主键),不做大小写转换。
func normalizeEnvs(raw string, known map[string]bool) string {
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		e := strings.TrimSpace(part)
		if e == "" || !known[e] || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return strings.Join(out, ",")
}

func (h *Handler) SaveWebhook(c *gin.Context) {
	var req dto.WebhookConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	// 地址只认 http/https,拒绝 link-local 与云元数据;重试上限 10(工单 02 第 4 条)。
	// 从前 ftp://、javascript:、169.254.169.254、retryMax=1000000 都照存。
	if strings.TrimSpace(req.Endpoint) != "" {
		if err := service.ValidateWebhookEndpoint(req.Endpoint); err != nil {
			resp.FailErr(c, resp.CodeBadRequest, err)
			return
		}
	}
	if req.RetryMax < 0 || req.RetryMax > maxWebhookRetry {
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("重试次数只能是 0 ~ %d(0 = 保持原值)", maxWebhookRetry))
		return
	}
	// 加密后要放进 VARCHAR(512)(迁移 0054):明文 256 字符以内,密文约 370。
	if len(req.Secret) > 256 {
		resp.Fail(c, resp.CodeBadRequest, "推送口令不能超过 256 个字符")
		return
	}
	wh, _ := h.Repo.GetWebhook()
	if wh == nil {
		wh = &model.WebhookConfig{}
	}
	if req.Endpoint != "" {
		wh.Endpoint = strings.TrimSpace(req.Endpoint)
	}
	secretChanged := req.Secret != ""
	if req.Secret != "" {
		wh.Secret = req.Secret // empty = keep the existing secret (never returned to the client, R3)
	}
	if req.Events != nil {
		// Authoritative subscription list — honour it verbatim, including an empty
		// list (deliver nothing) and removals like turning off login events. Only a
		// fully omitted field keeps the stored list.
		wh.Events = normalizeEvents(*req.Events)
	}
	if req.Envs != nil {
		// 只留存在的环境:写错的代码存下来也永远匹配不上,表现是那个环境**静默地**不推。
		envs, _ := h.Repo.ListEnvironments()
		known := map[string]bool{}
		for _, e := range envs {
			known[e.Code] = true
		}
		wh.Envs = normalizeEnvs(*req.Envs, known)
	}
	if req.RetryMax > 0 {
		wh.RetryMax = req.RetryMax
	}
	wh.Enabled = req.Enabled
	if err := h.Repo.SaveWebhook(wh); err != nil {
		resp.Fail(c, resp.CodeInternalError, "保存失败")
		return
	}
	note := ""
	if secretChanged {
		note = " secret=已修改"
	}
	h.auditAdmin(c, fmt.Sprintf("admin.webhook.update endpoint=%s events=%s envs=%s retryMax=%d enabled=%v%s",
		wh.Endpoint, wh.Events, wh.Envs, wh.RetryMax, wh.Enabled, note))
	// 与 GET 一致:不回 secret 明文,只回有没有(工单 02 第 3 条)。wh 是这次读出来的副本,清掉不影响库里。
	hasSecret := wh.Secret != ""
	wh.Secret = ""
	resp.OK(c, webhookView{WebhookConfig: *wh, HasSecret: hasSecret})
}

// maxWebhookRetry 推送失败的重试上限。再多也只是把一个挂掉的接收方多打几遍。
const maxWebhookRetry = 10

// webhookView 是保存 Webhook 后回给界面的样子:secret 恒为空,另带 hasSecret。
type webhookView struct {
	model.WebhookConfig
	HasSecret bool `json:"hasSecret"`
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

// CompileObject recompiles an Oracle stored program (package / procedure /
// function / trigger / type) and reports the resulting status plus any
// compilation errors. It is a DDL against the target, so it goes through the
// same three-layer gate as the terminal — see service.CompileObject.
func (h *Handler) CompileObject(c *gin.Context) {
	var req dto.CompileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	rep, err := h.Svc.CompileObject(middleware.CurrentUser(c), pathID(c),
		req.Scope, req.Type, req.Name, req.Database, req.MfaCode)
	if err != nil {
		resp.FailErr(c, compileErrCode(err), err)
		return
	}
	resp.OK(c, gin.H{"ok": rep.OK(), "report": rep})
}

// DownloadPackages 把选中的 Oracle 程序包(包头 + 包体)作为 .pck 下载;选了多个就打成
// 一个 zip。只读,走与查看源码相同的访问控制,另记一条导出审计 —— 见
// service.DownloadPackages。失败时回的仍是 JSON 信封,前端据 Content-Type 区分。
func (h *Handler) DownloadPackages(c *gin.Context) {
	var req dto.PackageDownloadReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	f, err := h.Svc.DownloadPackages(middleware.CurrentUser(c), pathID(c), req.Scope, req.Database, req.Names)
	if err != nil {
		resp.FailErr(c, compileErrCode(err), err)
		return
	}
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.Filename))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, f.ContentType, f.Body)
}

// InvalidObjects lists the INVALID compilable objects under one Oracle schema.
// Read-only: it goes through access control but not the DDL gate.
func (h *Handler) InvalidObjects(c *gin.Context) {
	items, err := h.Svc.InvalidObjects(middleware.CurrentUser(c), pathID(c),
		c.Query("scope"), c.Query("database"))
	if err != nil {
		resp.FailErr(c, compileErrCode(err), err)
		return
	}
	resp.OK(c, gin.H{"items": items, "total": len(items)})
}

// RecompileInvalid recompiles the INVALID objects under one Oracle schema.
// It is a batch of DDL against the target and carries the same three-layer gate
// as a single compile, judged over the WHOLE batch — see service.RecompileInvalid.
func (h *Handler) RecompileInvalid(c *gin.Context) {
	var req dto.RecompileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	rep, err := h.Svc.RecompileInvalid(middleware.CurrentUser(c), pathID(c),
		req.Scope, req.Database, req.Names, req.MfaCode)
	if err != nil {
		resp.FailErr(c, compileErrCode(err), err)
		return
	}
	// ok 的定义是"这次没有留下编不过的对象",而不是"请求成功返回了" —— 与单个编译
	// 同一个口径:失败的编译也会正常返回,只是对象还是 INVALID。
	resp.OK(c, gin.H{"ok": rep.Failed == 0, "report": rep})
}

// confirmTodosFor 把「等你确认」那一类收在一处:没有发布菜单的人一条都不该拿到 ——
// 放行端点挂在 menu("pipeline") 上,他点下去只会拿到「无菜单权限」。
func confirmTodosFor(h *Handler, u *model.User, menus map[string]bool) []service.ConfirmTodo {
	if !menus["pipeline"] {
		return nil
	}
	return h.Svc.ConfirmTodos(u)
}

func compileErrCode(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return resp.CodeForbidden
	case errors.Is(err, service.ErrNotFound):
		return resp.CodeBadRequest
	default:
		return resp.CodeBadRequest
	}
}

// ---------------------------------------------------------------- 项目(Project)

// ListProjects returns every project with what it currently owns. Read is open
// to the db menu: knowing which team owns which database is the point of the
// feature, and there is nothing sensitive in a name.
func (h *Handler) ListProjects(c *gin.Context) {
	resp.OK(c, h.Svc.ListProjects())
}

func (h *Handler) CreateProject(c *gin.Context) {
	var req dto.ProjectReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	p, err := h.Svc.CreateProject(middleware.CurrentUser(c), req)
	if err != nil {
		resp.FailErr(c, resp.CodeBadRequest, err)
		return
	}
	resp.OK(c, p)
}

func (h *Handler) UpdateProject(c *gin.Context) {
	var req dto.ProjectReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	p, err := h.Svc.UpdateProject(middleware.CurrentUser(c), pathID(c), req)
	if err != nil {
		resp.FailErr(c, projectErrCode(err), err)
		return
	}
	resp.OK(c, p)
}

func (h *Handler) DeleteProject(c *gin.Context) {
	if err := h.Svc.DeleteProject(middleware.CurrentUser(c), pathID(c)); err != nil {
		resp.FailErr(c, projectErrCode(err), err)
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

func projectErrCode(err error) int {
	if errors.Is(err, service.ErrNotFound) {
		return resp.CodeBadRequest
	}
	return resp.CodeBadRequest
}
