package handler

// 管理操作的审计(2026-10 黑盒测试,工单 02 第 2 条)。
//
// 连接、分层、环境、风险字典、流水线、系统设置、Webhook、SQL 规范规则 —— 这些配置决定"谁能
// 进来、能碰哪台库、哪条命令要审批、结果推给谁",从前改了不留痕:有人可以改宽、做事、再改回去,
// 审计链上只看得到那件事,看不到让它发生的那次配置。动作名沿用 admin.* 的写法,可 grep。
//
// 敏感值(口令、secret、token)一律不进审计:只记"已修改 / 已清除"或键名。

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"aegisdb/internal/middleware"
)

// auditDetailMax 一条审计里配置值的上限(rune)。配置可能很长(流水线阶段、规则参数),审计要
// 的是"改了什么"的可读摘要,不是一份备份。
const auditDetailMax = 400

// auditJSON 把一个值写成审计里的紧凑 JSON 摘要。
func auditJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return clip(string(b), auditDetailMax)
}

// auditAdmin 以当前登录人记一条管理审计。
func (h *Handler) auditAdmin(c *gin.Context, detail string) {
	h.Svc.AuditAdminAction(middleware.CurrentUser(c), detail)
}
