// Package handler holds Gin HTTP/WS handlers (bind params, call service, respond).
package handler

import (
	"encoding/json"
	"strconv"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/repository"
	"aegisdb/internal/service"
)

// Handler bundles dependencies for all routes.
type Handler struct {
	Svc      *service.Services
	Repo     *repository.Repo
	loginLim *loginThrottle
}

func New(svc *service.Services, repo *repository.Repo) *Handler {
	return &Handler{Svc: svc, Repo: repo, loginLim: newLoginThrottle()}
}

func pathID(c *gin.Context) int64 {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	return id
}

// toJSON encodes a settings value as a JSON string for storage.
func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// mfaLockedMsg:MFA 验证码连续错误被锁定时的提示(service.ErrMFALocked,2026-10 安全审计 H4)。
// tooManyJobsMsg:按人限额(L-API2)。数字与 service 里的 maxActiveJobsPerUser 一致。
const tooManyJobsMsg = "你已有 5 个任务在排队或执行中,请等它们完成后再提交"

const mfaLockedMsg = "MFA 验证码错误次数过多,已临时锁定,请 15 分钟后再试"
