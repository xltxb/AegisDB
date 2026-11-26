package repository

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/model"
	"aegisdb/pkg/crypto"
)

func newUserRepo(t *testing.T) (*Repo, *model.User) {
	t.Helper()
	crypto.SetSecretKey("mfa-cas-test")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "u.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	u := &model.User{Email: "a@x.io", Name: "A"}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	// 0001 起 mfa_secret 可为 NULL,老账号就是 NULL
	if err := db.Exec("UPDATE tbl_user SET mfa_secret = NULL WHERE id = ?", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	return New(db), u
}

func storedMFA(t *testing.T, r *Repo, id int64) (bool, string) {
	t.Helper()
	u, err := r.GetUserByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return u.MFAEnabled, u.MFASecret
}

func TestUpdateUserMFAIf(t *testing.T) {
	r, u := newUserRepo(t)

	// NULL 的密钥按空看
	if ok, err := r.UpdateUserMFAIf(u.ID, false, "", false, "S1"); err != nil || !ok {
		t.Fatalf("NULL 行应能写: ok=%v err=%v", ok, err)
	}
	if en, s := storedMFA(t, r, u.ID); en || s != "S1" {
		t.Fatalf("got %v %q", en, s)
	}

	// 期望对不上(别人已改成 S2 并启用):不写
	if err := r.UpdateUserMFA(u.ID, true, "S2"); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.UpdateUserMFAIf(u.ID, false, "S1", true, "S1"); err != nil || ok {
		t.Fatalf("状态已变不该写: ok=%v err=%v", ok, err)
	}
	if en, s := storedMFA(t, r, u.ID); !en || s != "S2" {
		t.Fatalf("应保持 S2 启用, got %v %q", en, s)
	}

	// 期望对得上:写
	if ok, err := r.UpdateUserMFAIf(u.ID, true, "S2", false, ""); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if en, s := storedMFA(t, r, u.ID); en || s != "" {
		t.Fatalf("应已停用, got %v %q", en, s)
	}
}
