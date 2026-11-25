package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"velagateway/internal/presence"
	"velagateway/pkg/resp"
)

type onlineTerm struct {
	ConnectionID int64      `json:"connectionId,omitempty"`
	Instance     string     `json:"instance"`
	OpenedAt     time.Time  `json:"openedAt"`
	LastExecAt   *time.Time `json:"lastExecAt,omitempty"`
	Running      bool       `json:"running"`
}

type onlineSession struct {
	UserID     int64        `json:"userId"`
	Name       string       `json:"name"`
	Email      string       `json:"email"`
	Dept       string       `json:"dept"`
	Initials   string       `json:"initials"`
	IP         string       `json:"ip"`
	Browser    string       `json:"browser"`
	LoginAt    *time.Time   `json:"loginAt,omitempty"`
	LastSeen   time.Time    `json:"lastSeen"`
	LastAction *time.Time   `json:"lastAction,omitempty"`
	Terminals  []onlineTerm `json:"terminals"`
}

// OnlineUsers 列出此刻在线的登录会话(见 presence):谁、从哪、用什么浏览器、最近一次操作、
// 开着哪些终端。只在内存里,进程重启清空;多副本部署时只看得到本副本。
func (h *Handler) OnlineUsers(c *gin.Context) {
	snaps := presence.Default.Online()
	out := make([]onlineSession, 0, len(snaps))
	users := map[int64]bool{}
	terms := 0
	for _, s := range snaps {
		row := onlineSession{UserID: s.UserID, Name: s.Name, Email: s.Email, IP: s.IP,
			Browser: presence.ShortUA(s.UserAgent), LastSeen: s.LastSeen, Terminals: []onlineTerm{}}
		if u, err := h.Repo.GetUserByID(s.UserID); err == nil {
			row.Dept, row.Initials = u.Dept, u.Initials
		}
		if !s.LoginAt.IsZero() {
			t := s.LoginAt
			row.LoginAt = &t
		}
		if !s.LastAction.IsZero() {
			t := s.LastAction
			row.LastAction = &t
		}
		for _, tm := range s.Terms {
			ot := onlineTerm{ConnectionID: tm.ConnID, Instance: tm.Instance, OpenedAt: tm.OpenedAt, Running: tm.Running}
			if !tm.LastExecAt.IsZero() {
				t := tm.LastExecAt
				ot.LastExecAt = &t
			}
			row.Terminals = append(row.Terminals, ot)
		}
		users[s.UserID] = true
		terms += len(s.Terms)
		out = append(out, row)
	}
	resp.OK(c, gin.H{"sessions": out, "users": len(users), "terminals": terms,
		"windowSec": int(presence.OnlineWindow.Seconds())})
}
