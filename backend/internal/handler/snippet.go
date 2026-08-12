package handler

import (
	"errors"

	"github.com/gin-gonic/gin"

	"velagateway/internal/dto"
	"velagateway/internal/middleware"
	"velagateway/internal/model"
	"velagateway/internal/service"
	"velagateway/pkg/resp"
)

// Terminal snippets — per-user saved scripts bound to hotkeys 1-9.
//
// Every route here is scoped to the caller: the list is theirs, and a write
// checks the owner before touching the row (service.SaveSnippet /
// DeleteSnippet). There is no admin view and no execute endpoint — pressing a
// hotkey submits the snippet's text through /risk/check + /terminal/exec like
// anything else typed, so the judgement happens against the target instance at
// the moment it runs, not against whatever was true when it was saved.

// Snippets lists the current user's own snippets.
func (h *Handler) Snippets(c *gin.Context) {
	resp.OK(c, h.Svc.ListSnippets(middleware.CurrentUser(c)))
}

// SnippetSave creates (POST) or updates (PUT /:id) one of the user's snippets.
func (h *Handler) SnippetSave(c *gin.Context) {
	var req dto.SnippetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数错误")
		return
	}
	s, err := h.Svc.SaveSnippet(middleware.CurrentUser(c), pathID(c), req.Name, req.Body, req.Slot)
	if err != nil {
		// The validation errors say precisely what is wrong with the input (name
		// empty, body too big, slot out of range) and the operator can act on every
		// one of them, so they are passed through rather than flattened into
		// "保存失败" — that was what made the 6MB script failure unreadable.
		switch {
		case errors.Is(err, service.ErrForbidden):
			resp.Fail(c, resp.CodeForbidden, "无权修改该脚本片段")
		case errors.Is(err, service.ErrSnippetLimit):
			resp.Fail(c, resp.CodeBadRequest, "脚本片段数量已达上限")
		default:
			resp.Fail(c, resp.CodeBadRequest, err.Error())
		}
		return
	}
	resp.OK(c, s)
}

// SnippetDelete removes one of the user's own snippets.
func (h *Handler) SnippetDelete(c *gin.Context) {
	if err := h.Svc.DeleteSnippet(middleware.CurrentUser(c), pathID(c)); err != nil {
		resp.Fail(c, resp.CodeForbidden, "无权删除该脚本片段")
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// SnippetLimits reports the caps the editor enforces client-side, so the two
// cannot drift: a form that lets someone type 20KB and then rejects it on save
// has wasted their work.
func (h *Handler) SnippetLimits(c *gin.Context) {
	resp.OK(c, gin.H{
		"maxBytes": model.MaxSnippetBytes,
		"maxName":  model.MaxSnippetName,
		"maxSlot":  model.MaxSnippetSlot,
		"max":      model.MaxSnippets,
	})
}
