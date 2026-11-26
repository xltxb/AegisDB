package bootstrap

import (
	"errors"
	"testing"
	"time"

	"aegisdb/internal/model"
	"aegisdb/internal/service"
	"aegisdb/pkg/totp"
)

// 本人自助领取 / 启用 / 停用 MFA,判断用的是请求开始时读到的状态。管理员在这中间替他绑定了
// 新因子,本人这一笔不能把它覆盖掉:报冲突,库里留着管理员绑的那一份。

const mfaRaceUser = "chenhao@aegisdb.io"

func mfaRaceSetup(t *testing.T, enabled bool, secret string) (*testApp, *model.User, *model.User) {
	t.Helper()
	app := newTestApp(t)
	u := app.storedUser(mfaRaceUser)
	if err := app.repo.UpdateUserMFA(u.ID, enabled, secret); err != nil {
		t.Fatal(err)
	}
	stale := app.storedUser(mfaRaceUser) // 本人这次请求读到的状态
	return app, stale, app.storedUser("linwei@aegisdb.io")
}

func assertAdminBindingKept(t *testing.T, app *testApp, adminSecret string) {
	t.Helper()
	got := app.storedUser(mfaRaceUser)
	if !got.MFAEnabled || got.MFASecret != adminSecret {
		t.Errorf("管理员绑定的因子被覆盖了: enabled=%v secretKept=%v", got.MFAEnabled, got.MFASecret == adminSecret)
	}
}

func TestSelfMFASetup_DoesNotDisarmAdminBinding(t *testing.T) {
	app, stale, admin := mfaRaceSetup(t, false, "")
	bound, err := app.svc.AdminBindMFA(admin, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.svc.MFASetup(stale, "aegisdb123"); !errors.Is(err, service.ErrMFAChanged) {
		t.Fatalf("应报 ErrMFAChanged, got %v", err)
	}
	assertAdminBindingKept(t, app, bound.Secret)
}

func TestSelfMFAEnable_DoesNotReplaceAdminBinding(t *testing.T) {
	mine := totp.GenerateSecret()
	app, stale, admin := mfaRaceSetup(t, false, mine) // 本人领了密钥、还没启用
	bound, err := app.svc.AdminBindMFA(admin, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.svc.MFAEnable(stale, totp.Code(mine, time.Now())); !errors.Is(err, service.ErrMFAChanged) {
		t.Fatalf("应报 ErrMFAChanged, got %v", err)
	}
	assertAdminBindingKept(t, app, bound.Secret)
}

func TestSelfMFADisable_DoesNotClearAdminBinding(t *testing.T) {
	mine := totp.GenerateSecret()
	app, stale, admin := mfaRaceSetup(t, true, mine)
	bound, err := app.svc.AdminBindMFA(admin, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.svc.MFADisable(stale, totp.Code(mine, time.Now())); !errors.Is(err, service.ErrMFAChanged) {
		t.Fatalf("应报 ErrMFAChanged, got %v", err)
	}
	assertAdminBindingKept(t, app, bound.Secret)
}

// 没人插手时照常:停用之后库里关掉、密钥清空。
func TestSelfMFADisable_StillWorksWithoutInterference(t *testing.T) {
	mine := totp.GenerateSecret()
	app, u, _ := mfaRaceSetup(t, true, mine)
	if err := app.svc.MFADisable(u, totp.Code(mine, time.Now())); err != nil {
		t.Fatal(err)
	}
	if got := app.storedUser(mfaRaceUser); got.MFAEnabled || got.MFASecret != "" {
		t.Errorf("停用没生效: %+v", got.MFAEnabled)
	}
}
