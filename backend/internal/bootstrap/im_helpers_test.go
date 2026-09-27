package bootstrap

import (
	"sort"

	"velagateway/internal/imchannel"
	"velagateway/internal/imchannel/imtest"
	"velagateway/internal/model"
)

// fakeIM 登记一个假渠道(能通知、能审批)并立即 Reload。开关仍是关的,用 enableIM 打开。
func (a *testApp) fakeIM(kind string) *imtest.Fake {
	a.t.Helper()
	f := imtest.New(kind)
	a.svc.IM.Register(kind, imchannel.Caps{Notify: true, Approve: true}, f.Builder())
	a.svc.IM.Reload()
	return f
}

// enableIM / disableIM 走设置接口,与管理员在界面上做的一样 —— 保存会触发 Reload。
func (a *testApp) enableIM(token, kind string) {
	a.t.Helper()
	a.setSettings(token, map[string]any{imchannel.Key(kind, "approval"): true})
}

func (a *testApp) disableIM(token, kind string) {
	a.t.Helper()
	a.setSettings(token, map[string]any{imchannel.Key(kind, "approval"): false})
}

// bindIM 直接写绑定表(代绑语义),返回该用户 id。
func (a *testApp) bindIM(email, kind, pid string) int64 {
	a.t.Helper()
	u, err := a.repo.GetUserByEmail(email)
	if err != nil || u == nil {
		a.t.Fatalf("user %s: %v", email, err)
	}
	if err := a.repo.BindIMIdentity(&model.IMIdentity{
		UserID: u.ID, Channel: kind, PlatformUserID: pid, BoundBy: model.IMBoundAdmin,
	}); err != nil {
		a.t.Fatalf("bind %s on %s: %v", email, kind, err)
	}
	return u.ID
}

// flushIM 等 Hub 把已派出的发卡 / 收卡 / 通知全部做完。
func (a *testApp) flushIM() { a.svc.IM.Flush() }

func sentPIDs(sent []imtest.Sent) []string {
	out := make([]string, 0, len(sent))
	for _, s := range sent {
		out = append(out, s.PlatformUserID)
	}
	sort.Strings(out)
	return out
}
