// Package handler holds Gin HTTP/WS handlers (bind params, call service, respond).
package handler

import (
	"encoding/json"
	"strconv"

	"github.com/gin-gonic/gin"

	"velagateway/internal/repository"
	"velagateway/internal/service"
)

// Handler bundles dependencies for all routes.
type Handler struct {
	Svc      *service.Services
	Repo     *repository.Repo
	loginLim *loginLimiter
}

func New(svc *service.Services, repo *repository.Repo) *Handler {
	return &Handler{Svc: svc, Repo: repo, loginLim: newLoginLimiter()}
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
