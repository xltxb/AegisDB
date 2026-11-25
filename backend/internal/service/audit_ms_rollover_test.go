package service

// 审计时间的毫秒进位(见 auditStamp):MySQL 的 DATETIME(3) 入库四舍五入,小数秒 ≥ .9995 的
// 行读回来进到下一秒,秒级的哈希载荷就对不上了。这里把时钟钉在这些时刻上写审计,再用网关
// 自己的 VerifyAuditChain 校验。
//
// 真 MySQL 的用例要设 AEGISDB_TEST_STORE_MYSQL=user:pw@tcp(host:port)/ 才跑;SQLite 的用例总跑,
// 守住"写进去的时间已经截到毫秒"这一条。

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/pkg/crypto"
)

// 刁钻的小数秒:前三个在修复前也没事,后面的在 MySQL 上都会进位。
var rolloverFracs = []int{0, 499_999_999, 999_499_999, 999_500_000, 999_700_000, 999_999_999}

func pinAuditClock(t *testing.T, at time.Time) {
	t.Helper()
	old := auditClock
	auditClock = func() time.Time { return at }
	t.Cleanup(func() { auditClock = old })
}

func writeRolloverChain(t *testing.T, s *Services) []time.Time {
	t.Helper()
	actor := &model.User{ID: 1, Name: "林薇"}
	base := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	var want []time.Time
	for i, f := range rolloverFracs {
		at := base.Add(time.Duration(i) * time.Minute).Add(time.Duration(f))
		pinAuditClock(t, at)
		if a := s.appendAudit(actor, nil, fmt.Sprintf("SELECT %d", i), "low", "executed", "", "", "exec", ""); a == nil || a.ID == 0 {
			t.Fatalf("frac %d: 审计没写进去", f)
		}
		want = append(want, at.Truncate(time.Millisecond))
	}
	return want
}

func assertRolloverChain(t *testing.T, g *gorm.DB, s *Services, want []time.Time) {
	t.Helper()
	var rows []model.AuditLog
	if err := g.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		t.Fatalf("行数 %d,期望 %d", len(rows), len(want))
	}
	for i, r := range rows {
		if !r.OccurredAt.Equal(want[i]) {
			t.Errorf("第 %d 行(frac %d)读回 %s,期望 %s", i, rolloverFracs[i],
				r.OccurredAt.Format(time.RFC3339Nano), want[i].Format(time.RFC3339Nano))
		}
	}
	rep, err := s.VerifyAuditChain(0)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Checked != int64(len(want)) {
		t.Fatalf("链校验失败:ok=%v checked=%d badId=%d reason=%s", rep.OK, rep.Checked, rep.BadID, rep.BadReason)
	}
}

func TestAppendAudit_SubMillisecondTruncatedBeforeHash(t *testing.T) {
	crypto.SetSecretKey("test-secret")
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	s := NewForOps(repository.New(g))
	assertRolloverChain(t, g, s, writeRolloverChain(t, s))
}

func TestAppendAudit_MillisecondRolloverKeepsChain_RealMySQL(t *testing.T) {
	base := os.Getenv("AEGISDB_TEST_STORE_MYSQL")
	if base == "" {
		t.Skip("未设置 AEGISDB_TEST_STORE_MYSQL,跳过真实 MySQL 存储库用例")
	}
	crypto.SetSecretKey("test-secret")
	name := fmt.Sprintf("vt_auditms_%d", os.Getpid())
	admin, err := sql.Open("mysql", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name)
	if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP DATABASE IF EXISTS " + name)

	g, err := gorm.Open(mysql.Open(base+name+"?charset=utf8mb4&parseTime=true&loc=Local"),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if s, e := g.DB(); e == nil {
		defer s.Close()
	}
	// 与迁移 0001 同列型:DATETIME(3)。
	if err := g.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	var colType string
	if err := g.Raw(`SELECT column_type FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'tbl_audit_log' AND column_name = 'occurred_at'`).Scan(&colType).Error; err != nil {
		t.Fatal(err)
	}
	if colType != "datetime(3)" {
		t.Fatalf("occurred_at 列型 %q,与生产的 datetime(3) 不一致,这条用例就不说明问题了", colType)
	}

	// 前提:这个库确实会进位(否则下面的用例修不修都会过)。
	probe := time.Date(2026, 3, 1, 8, 0, 0, 999_700_000, time.Local)
	var back time.Time
	if err := g.Raw("SELECT CAST(? AS DATETIME(3))", probe).Scan(&back).Error; err != nil {
		t.Fatal(err)
	}
	if back.Second() != 1 {
		t.Fatalf("MySQL 没有把 .9997 进位(读回 %s),前提不成立", back.Format(time.RFC3339Nano))
	}

	s := NewForOps(repository.New(g))
	want := writeRolloverChain(t, s)
	assertRolloverChain(t, g, s, want)

	// 修复前生产库里的样子:旧写法(纳秒时间直接入库)由 MySQL 自己进位,校验要认得出来。
	var tip model.AuditLog
	if err := g.Order("id DESC").First(&tip).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 1, 10, 0, 0, 999_700_000, time.Local)
	h := legacyRow(t, g, tip.Hash, at, at, nil) // 存的就是 .9997,交给 MySQL 去进位
	legacyRow(t, g, h, at.Add(time.Minute).Truncate(time.Second), at.Add(time.Minute).Truncate(time.Second), nil)
	rep, err := s.VerifyAuditChain(0)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.RolloverRows != 1 || rep.Checked != int64(len(want)+2) {
		t.Fatalf("旧进位行:ok=%v rolloverRows=%d checked=%d reason=%s", rep.OK, rep.RolloverRows, rep.Checked, rep.BadReason)
	}
}

// ---- 校验对修复前留下的进位行的容忍(auditRolloverHit)----

// legacyRow 按旧写法造一行:哈希按 hashAt 算,库里存 storedAt(edit 可在算完哈希后再改字段)。
func legacyRow(t *testing.T, g *gorm.DB, prev string, hashAt, storedAt time.Time, edit func(*model.AuditLog)) string {
	t.Helper()
	a := &model.AuditLog{OccurredAt: hashAt, ActorID: 1, ActorName: "林薇", Command: "SELECT 1",
		Risk: "low", Result: "executed", EventType: "exec", DataAction: "read", PrevHash: prev}
	a.Hash = crypto.ChainHash(prev, auditHashPayload(a))
	a.OccurredAt = storedAt
	if edit != nil {
		edit(a)
	}
	if err := g.Create(a).Error; err != nil {
		t.Fatal(err)
	}
	return a.Hash
}

func sqliteAuditStore(t *testing.T) (*gorm.DB, *Services) {
	t.Helper()
	crypto.SetSecretKey("test-secret")
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	return g, NewForOps(repository.New(g))
}

func TestVerifyAuditChain_RolloverTolerance(t *testing.T) {
	sec := time.Date(2026, 3, 1, 9, 0, 0, 0, time.Local)
	frac := sec.Add(999_700_000) // 旧写法算哈希时的时间
	next := sec.Add(time.Second) // MySQL 进位后读回的整秒
	cases := []struct {
		name     string
		storedAt time.Time
		edit     func(*model.AuditLog)
		ok       bool
	}{
		{"进位成整秒:放行", next, nil, true},
		{"晚 1 秒但不是整秒:不放行", next.Add(time.Millisecond), nil, false},
		{"晚整 2 秒:不放行", next.Add(time.Second), nil, false},
		{"早 1 秒:不放行", sec.Add(-time.Second), nil, false},
		{"整秒进位但改了命令:不放行", next, func(a *model.AuditLog) { a.Command = "DROP TABLE t" }, false},
		{"整秒进位但改了操作人:不放行", next, func(a *model.AuditLog) { a.ActorName = "别人" }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, s := sqliteAuditStore(t)
			h := legacyRow(t, g, "", sec, sec, nil) // 一行正常的
			h = legacyRow(t, g, h, frac, c.storedAt, c.edit)
			legacyRow(t, g, h, sec.Add(time.Minute), sec.Add(time.Minute), nil) // 后面再接一行正常的
			rep, err := s.VerifyAuditChain(0)
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK != c.ok {
				t.Fatalf("ok=%v,期望 %v(reason=%s badId=%d)", rep.OK, c.ok, rep.BadReason, rep.BadID)
			}
			if c.ok && (rep.RolloverRows != 1 || rep.Checked != 3) {
				t.Fatalf("rolloverRows=%d checked=%d,期望 1 / 3", rep.RolloverRows, rep.Checked)
			}
			if !c.ok && (rep.BadID != 2 || rep.BadReason != auditBadContent) {
				t.Fatalf("应断在第 2 行 content,实际 badId=%d reason=%s", rep.BadID, rep.BadReason)
			}
		})
	}
}

// 正常写出来的链不该动用这条放宽。
func TestVerifyAuditChain_NoRolloverOnCleanChain(t *testing.T) {
	g, s := sqliteAuditStore(t)
	writeRolloverChain(t, s)
	rep, err := s.VerifyAuditChain(0)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.RolloverRows != 0 {
		t.Fatalf("ok=%v rolloverRows=%d,期望 true / 0", rep.OK, rep.RolloverRows)
	}
	_ = g
}
