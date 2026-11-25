package handler

import (
	"github.com/gin-gonic/gin"

	"velagateway/internal/apimsg"
	"velagateway/internal/dto"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// Tier / environment administration.
//
// Reads are open to any authenticated caller: the console renders instance
// labels, the database tree and the connection form from these lists, so gating
// them behind the admin menu would blank out the terminal for everyone else.
// Every mutation is envtier-menu + admin, because a tier decides how strictly
// its instances are governed.
//
// Failures surface the service message verbatim — "仍有环境绑定该分层标签" tells the
// operator what to do next, where a generic 400 would not.

func (h *Handler) ListEnvTiers(c *gin.Context) {
	ts, err := h.Svc.ListEnvTiers()
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "读取分层标签失败")
		return
	}
	resp.OK(c, ts)
}

func (h *Handler) CreateEnvTier(c *gin.Context) {
	var req dto.EnvTierCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	t, err := h.Svc.CreateEnvTier(req)
	if err != nil {
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("创建失败:%s", err))
		return
	}
	h.auditAdmin(c, "admin.envtier.create code="+t.Code+" template="+req.TemplateCode+" value="+auditJSON(t))
	resp.OK(c, t)
}

func (h *Handler) UpdateEnvTier(c *gin.Context) {
	var req dto.EnvTierUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	t, err := h.Svc.UpdateEnvTier(c.Param("code"), req)
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "分层标签不存在")
		return
	}
	if err != nil {
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("更新失败:%s", err))
		return
	}
	// 记改完之后的整份开关:局部更新时,只看请求说不清这一层此刻是什么样。
	h.auditAdmin(c, "admin.envtier.update code="+t.Code+" value="+auditJSON(t))
	resp.OK(c, t)
}

func (h *Handler) DeleteEnvTier(c *gin.Context) {
	err := h.Svc.DeleteEnvTier(c.Param("code"))
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "分层标签不存在")
		return
	}
	if err != nil {
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("删除失败:%s", err))
		return
	}
	h.auditAdmin(c, "admin.envtier.delete code="+c.Param("code"))
	resp.OK(c, gin.H{"deleted": c.Param("code")})
}

func (h *Handler) ListEnvironments(c *gin.Context) {
	es, err := h.Svc.ListEnvironments()
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "读取环境失败")
		return
	}
	resp.OK(c, es)
}

// EnvironmentUsage returns instance counts per environment so the delete dialog
// can state how many instances it is about to move.
func (h *Handler) EnvironmentUsage(c *gin.Context) {
	u, err := h.Svc.EnvironmentUsage()
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, "读取环境使用情况失败")
		return
	}
	resp.OK(c, u)
}

func (h *Handler) CreateEnvironment(c *gin.Context) {
	var req dto.EnvironmentCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	e, err := h.Svc.CreateEnvironment(req)
	if err != nil {
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("创建失败:%s", err))
		return
	}
	h.auditAdmin(c, "admin.environment.create code="+e.Code+" tier="+e.TierCode+" name="+e.DisplayName)
	resp.OK(c, e)
}

func (h *Handler) UpdateEnvironment(c *gin.Context) {
	var req dto.EnvironmentUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	e, err := h.Svc.UpdateEnvironment(c.Param("code"), req)
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "环境不存在")
		return
	}
	if err != nil {
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("更新失败:%s", err))
		return
	}
	// 改绑分层会改变这个环境里每台实例的管控方式,与建、删同样要留痕。
	h.auditAdmin(c, "admin.environment.update code="+e.Code+" tier="+e.TierCode+" name="+e.DisplayName)
	resp.OK(c, e)
}

// DeleteEnvironment needs a move-to target in the body: instances must land in a
// real environment, since an unresolvable one has no tier and thus no rules.
func (h *Handler) DeleteEnvironment(c *gin.Context) {
	var req dto.EnvironmentDeleteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误:需指定实例迁移目标环境")
		return
	}
	err := h.Svc.DeleteEnvironment(c.Param("code"), req.MoveTo)
	if err == service.ErrNotFound {
		resp.Fail(c, resp.CodeBadRequest, "环境不存在")
		return
	}
	if err != nil {
		resp.FailErr(c, resp.CodeBadRequest, apimsg.Errorf("删除失败:%s", err))
		return
	}
	h.auditAdmin(c, "admin.environment.delete code="+c.Param("code")+" moveTo="+req.MoveTo)
	resp.OK(c, gin.H{"deleted": c.Param("code"), "movedTo": req.MoveTo})
}
