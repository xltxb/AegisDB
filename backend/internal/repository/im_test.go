package repository

import (
	"errors"
	"testing"
	"time"

	"velagateway/internal/model"
	"velagateway/internal/testsupport"
)

func newIMRepo(t *testing.T) (*Repo, int64, int64) {
	t.Helper()
	r := New(testsupport.NewDB(t))
	a := model.User{Name: "甲", Email: "a@x.io", RoleID: 1}
	b := model.User{Name: "乙", Email: "b@x.io", RoleID: 1}
	for _, u := range []*model.User{&a, &b} {
		if err := r.db.Create(u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	return r, a.ID, b.ID
}

func TestIMIdentity_BindRebindAndConflict(t *testing.T) {
	r, a, b := newIMRepo(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(r.BindIMIdentity(&model.IMIdentity{UserID: a, Channel: "fake", PlatformUserID: "p1", BoundBy: model.IMBoundSelf}))
	// 同一人同渠道重新绑定 = 覆盖
	must(r.BindIMIdentity(&model.IMIdentity{UserID: a, Channel: "fake", PlatformUserID: "p2", BoundBy: model.IMBoundAdmin}))
	got, err := r.IMIdentityByPlatform("fake", "p2")
	if err != nil || got.UserID != a || got.BoundBy != model.IMBoundAdmin {
		t.Fatalf("lookup p2 = %+v, %v", got, err)
	}
	if _, err := r.IMIdentityByPlatform("fake", "p1"); err == nil {
		t.Fatal("the overwritten platform id must no longer resolve")
	}
	// 别人的平台账号不能再挂到另一个网关账户上
	if err := r.BindIMIdentity(&model.IMIdentity{UserID: b, Channel: "fake", PlatformUserID: "p2", BoundBy: model.IMBoundSelf}); !errors.Is(err, ErrIMIdentityTaken) {
		t.Fatalf("err = %v, want ErrIMIdentityTaken", err)
	}
	ids, err := r.IMPlatformIDs("fake", []int64{a, b})
	if err != nil || len(ids) != 1 || ids[a] != "p2" {
		t.Fatalf("platform ids = %v, %v", ids, err)
	}
	if ok, _ := r.UnbindIMIdentity(a, "fake"); !ok {
		t.Fatal("unbind should report a removed row")
	}
	if ok, _ := r.UnbindIMIdentity(a, "fake"); ok {
		t.Fatal("unbinding twice removes nothing")
	}
}

func TestIMBindCode_SingleUseReplaceAndExpiry(t *testing.T) {
	r, a, _ := newIMRepo(t)
	now := time.Now()
	put := func(hash string, exp time.Time) {
		t.Helper()
		if err := r.PutIMBindCode(&model.IMBindCode{UserID: a, Channel: "fake", CodeHash: hash, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	put("h1", now.Add(10*time.Minute))
	put("h2", now.Add(10*time.Minute)) // 重新生成作废 h1
	if _, ok, _ := r.ConsumeIMBindCode("fake", "h1", now); ok {
		t.Fatal("a replaced code must not be consumable")
	}
	uid, ok, err := r.ConsumeIMBindCode("fake", "h2", now)
	if err != nil || !ok || uid != a {
		t.Fatalf("consume h2 = %d %v %v", uid, ok, err)
	}
	if _, ok, _ := r.ConsumeIMBindCode("fake", "h2", now); ok {
		t.Fatal("a code is single-use")
	}
	put("h3", now.Add(-time.Second))
	if _, ok, _ := r.ConsumeIMBindCode("fake", "h3", now); ok {
		t.Fatal("an expired code must not be consumable")
	}
	if n, _ := r.PurgeExpiredIMBindCodes(now); n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
}

func TestIMCard_OpenAndClose(t *testing.T) {
	r, a, b := newIMRepo(t)
	for _, u := range []int64{a, b} {
		if err := r.CreateIMCard(&model.IMCard{ApprovalID: 7, Channel: "fake", UserID: u, MsgRef: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	open, _ := r.OpenIMCards(7)
	if len(open) != 2 {
		t.Fatalf("open = %d", len(open))
	}
	if err := r.CloseIMCard(open[0].ID); err != nil {
		t.Fatal(err)
	}
	if open, _ = r.OpenIMCards(7); len(open) != 1 {
		t.Fatalf("open after close = %d", len(open))
	}
}
