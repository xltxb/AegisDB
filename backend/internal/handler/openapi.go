package handler

import (
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"

	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/model"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// 开放接口 —— 外部系统提交 SQL 升级单。
//
// Everything here runs behind middleware.APIClientAuth: the caller is an
// external SYSTEM holding a key/secret, acting as a service account. From the
// service layer down, the request is indistinguishable from a console one — same
// flow templates, same review rules, same approval chain, same execute-time
// re-judgement, same audit chain — which is the only way this door can exist
// without becoming the way people get around the console.

// maxOpenUploadBytes bounds a multipart script. It matches the inline bound the
// service layer enforces (15MB), so the two channels refuse at the same size
// rather than one of them dying inside the metadata database.
const maxOpenUploadBytes = 15 << 20

// OpenCreateRelease raises a release ticket from SQL or a script file.
//
// Two body shapes are accepted because both are what real callers send: JSON
// (an orchestrator posting a payload) and multipart/form-data (a CI job running
// `curl -F file=@upgrade.sql`).
func (h *Handler) OpenCreateRelease(c *gin.Context) {
	cl := middleware.CurrentAPIClient(c)
	u := middleware.CurrentUser(c)
	var req dto.OpenReleaseReq

	if strings.HasPrefix(c.ContentType(), "multipart/form-data") {
		req = dto.OpenReleaseReq{
			Title: c.PostForm("title"), ExternalRef: c.PostForm("externalRef"),
			Instance: c.PostForm("instance"), Database: c.PostForm("database"),
			Pipeline: c.PostForm("pipeline"), Reason: c.PostForm("reason"),
			SQL: c.PostForm("sql"), MfaCode: c.PostForm("mfaCode"),
		}
		if fh, err := c.FormFile("file"); err == nil {
			if fh.Size > maxOpenUploadBytes {
				resp.Fail(c, resp.CodeBadRequest, "脚本超过 15MB 上限")
				return
			}
			f, oerr := fh.Open()
			if oerr != nil {
				resp.Fail(c, resp.CodeBadRequest, "脚本读取失败")
				return
			}
			defer f.Close()
			// io.ReadAll under an explicit cap — trusting fh.Size and sizing a
			// buffer from it lets a lying Content-Length allocate what it likes.
			body, rerr := io.ReadAll(io.LimitReader(f, maxOpenUploadBytes+1))
			if rerr != nil || len(body) > maxOpenUploadBytes {
				resp.Fail(c, resp.CodeBadRequest, "脚本读取失败或超过 15MB 上限")
				return
			}
			req.Script, req.Filename = string(body), fh.Filename
		}
	} else if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误: "+err.Error())
		return
	}

	rel, err := h.Svc.CreateReleaseFromAPI(u, cl, req)
	if err != nil {
		slog.Warn("open api: create release rejected",
			"client", clientName(cl), "externalRef", req.ExternalRef, "err", err)
		resp.Fail(c, openErrCode(err), openErrMsg(err))
		return
	}
	slog.Info("open api: release created",
		"client", clientName(cl), "relNo", rel.RelNo, "externalRef", rel.ExternalRef,
		"instance", rel.Instance, "status", rel.Status)
	resp.OK(c, h.Svc.OpenReleaseResp(rel, false))
}

// OpenGetRelease is the status poll: where the run is, and what each stage said.
func (h *Handler) OpenGetRelease(c *gin.Context) {
	v, err := h.Svc.ReleaseStatusForClient(
		middleware.CurrentUser(c), middleware.CurrentAPIClient(c), c.Param("relNo"))
	if err != nil {
		resp.Fail(c, openErrCode(err), "升级单不存在或无权查看")
		return
	}
	resp.OK(c, h.Svc.OpenReleaseResp(&v.Release, true))
}

// OpenAbortRelease cancels a ticket the caller's own system raised.
func (h *Handler) OpenAbortRelease(c *gin.Context) {
	err := h.Svc.AbortReleaseForClient(
		middleware.CurrentUser(c), middleware.CurrentAPIClient(c), c.Param("relNo"))
	if err != nil {
		resp.Fail(c, openErrCode(err), openErrMsg(err))
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// OpenReviewCheck runs the 规范审查 without raising anything — the gate a CI job
// wants before it decides whether to create a change at all.
func (h *Handler) OpenReviewCheck(c *gin.Context) {
	var req dto.OpenReviewReq
	if strings.HasPrefix(c.ContentType(), "multipart/form-data") {
		req = dto.OpenReviewReq{
			Instance: c.PostForm("instance"), Dialect: c.PostForm("dialect"), SQL: c.PostForm("sql"),
		}
		if fh, err := c.FormFile("file"); err == nil && fh.Size <= maxOpenUploadBytes {
			if f, oerr := fh.Open(); oerr == nil {
				defer f.Close()
				if body, rerr := io.ReadAll(io.LimitReader(f, maxOpenUploadBytes+1)); rerr == nil && len(body) <= maxOpenUploadBytes {
					req.Script, req.Filename = string(body), fh.Filename
				}
			}
		}
	} else if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误: "+err.Error())
		return
	}
	res, err := h.Svc.CheckSQLForClient(middleware.CurrentUser(c), req)
	if err != nil {
		resp.Fail(c, openErrCode(err), openErrMsg(err))
		return
	}
	resp.OK(c, res)
}

// OpenListInstances tells an integrator which instances this credential may
// target, by the names the create call accepts. Without it, wiring up a new
// integration is a guessing game against a 400.
func (h *Handler) OpenListInstances(c *gin.Context) {
	u := middleware.CurrentUser(c)
	conns, _ := h.Svc.AccessibleConnections(u)
	out := make([]gin.H, 0, len(conns))
	for _, cn := range conns {
		out = append(out, gin.H{
			"instance": cn.Name, "env": cn.Env, "engine": cn.Engine,
			"database": cn.Database, "status": cn.Status,
		})
	}
	resp.OK(c, out)
}

// OpenListPipelines lists the flows a ticket may name.
func (h *Handler) OpenListPipelines(c *gin.Context) {
	flows := h.Svc.ListPipelines()
	out := make([]gin.H, 0, len(flows))
	for _, p := range flows {
		if !p.Enabled {
			continue
		}
		types := make([]string, 0, len(p.Stages))
		for _, st := range p.Stages {
			types = append(types, st.Type)
		}
		out = append(out, gin.H{
			"pipeline": p.Name, "tier": p.TierCode, "isDefault": p.IsDefault, "stages": types,
		})
	}
	resp.OK(c, out)
}

// ---------------------------------------------------------------- 凭据管理(控制台)

// ListAPIClients returns the credentials. There is no secret in the payload —
// the table stores only a bcrypt hash, so there is nothing to leak here even by
// accident.
func (h *Handler) ListAPIClients(c *gin.Context) {
	resp.OK(c, h.Svc.ListAPIClients())
}

// CreateAPIClient issues a credential and returns its ONLY plaintext copy.
func (h *Handler) CreateAPIClient(c *gin.Context) {
	var req dto.APIClientReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	cl, token, err := h.Svc.CreateAPIClient(middleware.CurrentUser(c), req)
	if err != nil {
		resp.Fail(c, resp.CodeBadRequest, err.Error())
		return
	}
	slog.Info("api client created", "name", cl.Name, "key", cl.Key,
		"serviceAccount", cl.UserName, "by", middleware.CurrentUser(c).Name)
	resp.OK(c, dto.APIClientCreated{Client: *cl, Token: token})
}

// UpdateAPIClient edits name / scopes / IP allowlist / enabled.
func (h *Handler) UpdateAPIClient(c *gin.Context) {
	var req dto.APIClientReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	cl, err := h.Svc.UpdateAPIClient(pathID(c), req)
	if err != nil {
		resp.Fail(c, openErrCode(err), err.Error())
		return
	}
	resp.OK(c, cl)
}

// DeleteAPIClient revokes a credential outright.
func (h *Handler) DeleteAPIClient(c *gin.Context) {
	if err := h.Svc.DeleteAPIClient(pathID(c)); err != nil {
		resp.Fail(c, openErrCode(err), "删除失败")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// clientName is the log label for a credential (nil-safe: an unauthenticated
// request never reaches these handlers, but a log line must never panic).
func clientName(cl *model.APIClient) string {
	if cl == nil {
		return "?"
	}
	return cl.Name
}

func openErrCode(err error) int {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return resp.CodeForbidden
	case errors.Is(err, service.ErrMFARequired), errors.Is(err, service.ErrMFAInvalid):
		return resp.CodeMFARequired
	case errors.Is(err, service.ErrScriptPathUnset):
		return resp.CodeScriptPathUnset
	case errors.Is(err, service.ErrNotFound):
		return resp.CodeBadRequest
	default:
		return resp.CodeBadRequest
	}
}

// openErrMsg passes the service's own wording through: these messages name the
// actual problem ("实例不存在: order-clstr", "该变更命中…需审批,请选择包含审批阶段的
// 发布流程") and an integrator can act on every one of them. Flattening them into
// "创建失败" is what turns an integration into a support ticket.
func openErrMsg(err error) string {
	switch {
	case errors.Is(err, service.ErrForbidden):
		return "该服务账号无权对目标实例执行此变更"
	case errors.Is(err, service.ErrMFARequired), errors.Is(err, service.ErrMFAInvalid):
		return "目标实例要求二次验证,请在请求中提供 mfaCode"
	case errors.Is(err, service.ErrScriptPathUnset):
		return "网关尚未配置脚本保存路径,无法接收脚本文件"
	default:
		return err.Error()
	}
}
