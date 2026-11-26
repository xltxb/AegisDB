package bootstrap

// migrate-store 端到端:一个真 MySQL 存储库(迁移 + 播种 + 夹具 + 走接口产生的数据)
// 搬到一个空的 PG 库,网关在 PG 上起来后,接口看到的东西与在 MySQL 上一模一样。
//
//	AEGISDB_TEST_STORE_MYSQL=root:pw@tcp(127.0.0.1:3407)/
//	AEGISDB_TEST_STORE_PG=host=127.0.0.1 port=5437 user=postgres password=pw sslmode=disable

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/internal/service"
	"aegisdb/internal/sqlmigrate"
	"aegisdb/internal/storemigrate"
	"aegisdb/pkg/crypto"
)

type storePair struct {
	myDSN, pgDSN string
	my           *sql.DB
	pgAdmin      *sql.DB
	pgName       string
}

// newStorePair 建一个迁移 + 播种 + 夹具都齐了的 MySQL 存储库,和一个空的 PG 库。
func newStorePair(t *testing.T) *storePair {
	t.Helper()
	myBase, pgBase := os.Getenv("AEGISDB_TEST_STORE_MYSQL"), os.Getenv("AEGISDB_TEST_STORE_PG")
	if myBase == "" || pgBase == "" {
		t.Skip("需要 AEGISDB_TEST_STORE_MYSQL 与 AEGISDB_TEST_STORE_PG")
	}
	p := &storePair{}
	myName := fmt.Sprintf("vt_msrc_%d_%d", os.Getpid(), time.Now().UnixNano()%1e6)
	p.my = migratedMySQLStore(t, myBase, myName)
	p.myDSN = myBase + myName + "?charset=utf8mb4&parseTime=true&loc=Local"
	cfg := &Config{}
	cfg.Database.Driver = "mysql"
	cfg.Database.MySQLDSN = p.myDSN
	g, err := OpenDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.New(g)
	if err := Seed(repo, cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedTestFixtures(t, repo)
	if s, e := g.DB(); e == nil {
		s.Close()
	}

	p.pgName = fmt.Sprintf("vt_mdst_%d_%d", os.Getpid(), time.Now().UnixNano()%1e6)
	if p.pgAdmin, err = pgAdmin(pgBase); err != nil {
		t.Fatal(err)
	}
	_, _ = p.pgAdmin.Exec("DROP DATABASE IF EXISTS " + p.pgName + " WITH (FORCE)")
	if _, err := p.pgAdmin.Exec("CREATE DATABASE " + p.pgName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.pgAdmin.Exec("DROP DATABASE IF EXISTS " + p.pgName + " WITH (FORCE)"); p.pgAdmin.Close() })
	p.pgDSN = pgBase + " dbname=" + p.pgName
	return p
}

func (p *storePair) appOnMySQL(t *testing.T) *testApp {
	return newTestApp(t, func(c *Config) {
		c.Database.Driver, c.Database.MySQLDSN, c.Database.AutoMigrate = "mysql", p.myDSN, false
	})
}

func (p *storePair) appOnPG(t *testing.T) *testApp {
	return newTestApp(t, func(c *Config) {
		c.Database.Driver, c.Database.PostgresDSN, c.Database.AutoMigrate = "postgres", p.pgDSN, false
	})
}

func (p *storePair) migrate(t *testing.T, mod func(*storemigrate.Options)) (*storemigrate.Report, error) {
	t.Helper()
	o := storemigrate.Options{
		MySQLDSN: p.myDSN, PostgresDSN: p.pgDSN, Workers: 3, ApplySchema: sqlmigrate.Run,
		Log: func(f string, a ...any) { t.Logf(f, a...) },
	}
	if mod != nil {
		mod(&o)
	}
	return storemigrate.Run(context.Background(), o)
}

// getData 取一个 GET 接口的 data 原文。
func (a *testApp) getData(token, path string) string {
	a.t.Helper()
	r := a.do(http.MethodGet, path, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("GET %s: code=%d msg=%s", path, r.Code, r.Msg)
	}
	return string(r.Data)
}

func TestMigrateStore_MySQLToPG_EndToEnd(t *testing.T) {
	p := newStorePair(t)

	// 1) 网关跑在 MySQL 上,走接口产生一批数据(含刁钻内容、大小写、各类事件)。
	my := p.appOnMySQL(t)
	admin := my.login("linwei@aegisdb.io", "aegisdb123")
	dev, prod := my.connIDByEnv(admin, "dev"), my.connIDByEnv(admin, "prod")
	for i := 0; i < 15; i++ {
		for _, q := range []string{
			"SELECT * FROM orders WHERE note = '尾随空格  '",
			"SELECT '😀', 'it''s', \"q\", 'tab\there'",
			"UPDATE orders SET status='x' WHERE id = " + fmt.Sprint(i),
			"DROP TABLE orders_2024_q3",
			"SELECT 1;\nSELECT 2",
		} {
			my.execSQL(admin, dev, q)
			my.execSQL(admin, prod, q)
		}
		if i < 3 { // 登录失败计数有限流,几次就够产生事件
			my.loginCode("linwei@aegisdb.io", "wrong-password")
		}
	}
	// 大小写不敏感是 MySQL 这边的现状(*_ci),搬过去要保持。
	if code := my.loginCode("LinWei@AegisDB.IO", "aegisdb123"); code != 0 {
		t.Fatalf("MySQL 上大写邮箱登录 code=%d —— 前提变了,先查清", code)
	}
	// 聊天审批卡片的三张表与审批单的两列(迁移 0065):都有内容,空表搬得对不对看不出来。
	if _, err := my.svc.DecideApprovalVia(my.storedUser("zhangwei@aegisdb.io"),
		my.submitProdHighRisk(admin).ID, false, "lark", "影响大促"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO tbl_chat_binding(user_id, channel, bot_id, account_id, chat_id, created_at) VALUES (1, 'lark', 'cli_a', 'ou_1', 'oc_1', NOW(3))",
		"INSERT INTO tbl_chat_bind_code(user_id, channel, code_hash, expires_at) VALUES (1, 'lark', REPEAT('a', 64), NOW(3))",
		"INSERT INTO tbl_chat_card(approval_id, user_id, channel, partial, next_at, last_error) VALUES (1, 1, 'lark', 1, NOW(3), '超时')",
	} {
		if _, err := p.my.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{
		"/api/v1/audit?pageSize=200",
		"/api/v1/audit?keyword=" + "ORDERS",
		"/api/v1/users",
		"/api/v1/connections",
		"/api/v1/approvals?scope=all",
		"/api/v1/roles",
		"/api/v1/env-tiers",
		"/api/v1/environments",
	}
	before := map[string]string{}
	for _, path := range paths {
		before[path] = my.getData(admin, path)
	}
	beforeVerify := my.getData(admin, "/api/v1/audit/verify")
	my.srv.Close()

	// 2) 搬。
	rep, err := p.migrate(t, nil)
	if err != nil {
		t.Fatalf("migrate-store: %v", err)
	}
	if !rep.SourceChain.OK || !rep.TargetChain.OK {
		t.Fatalf("审计链:源 %s / 目标 %s", rep.SourceChain, rep.TargetChain)
	}
	var rows int64
	for _, tb := range rep.Tables {
		rows += tb.TargetRows
		if strings.HasPrefix(tb.Name, "tbl_chat_") && tb.TargetRows != 1 {
			t.Errorf("%s 搬过去 %d 行,应为 1", tb.Name, tb.TargetRows)
		}
	}
	t.Logf("搬了 %d 张表、%d 行,用时 %s", len(rep.Tables), rows, rep.Took)

	// 3) 网关跑在 PG 上:接口看到的一模一样。
	pg := p.appOnPG(t)
	// 沿用切换前在 MySQL 上签发的登录态:会话随存储库一起搬过来,切换不该把在线的人踢下去;
	// 也避免这里再登录一次往审计里多写一行,让"前后一模一样"的比对失真。
	admin2 := admin
	for _, path := range paths {
		if got := pg.getData(admin2, path); got != before[path] {
			i := 0
			for i < len(got) && i < len(before[path]) && got[i] == before[path][i] {
				i++
			}
			from := max(0, i-150)
			t.Errorf("%s 在 PG 上与 MySQL 上不同(第 %d 字节起)\nMySQL: …%.300s\nPG:    …%.300s", path, i, before[path][from:], got[from:])
		}
	}
	if got := pg.getData(admin2, "/api/v1/audit/verify"); !strings.Contains(got, `"ok":true`) {
		t.Errorf("PG 上链校验:%s(MySQL 上:%s)", got, beforeVerify)
	}
	if code := pg.loginCode("LinWei@AegisDB.IO", "aegisdb123"); code != 0 {
		t.Errorf("PG 上大写邮箱登录 code=%d,MySQL 上是 0", code)
	}
	// 4) 继续写:序列已对齐(不撞主键),链接得上。
	for i := 0; i < 5; i++ {
		if r := pg.execSQL(admin2, dev, "SELECT "+fmt.Sprint(i)); r.Code != 0 {
			t.Fatalf("PG 上执行 code=%d msg=%s", r.Code, r.Msg)
		}
	}
	if got := pg.getData(admin2, "/api/v1/audit/verify"); !strings.Contains(got, `"ok":true`) {
		t.Errorf("PG 上续写后链校验:%s", got)
	}
}

// dropMarker 模拟"确认要回退 / 重搬"时人工删掉搬迁标记。
func (p *storePair) dropMarker(t *testing.T) {
	t.Helper()
	if _, err := p.my.Exec("DROP TABLE IF EXISTS " + storemigrate.MovedMarkerTable); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateStore_Refusals(t *testing.T) {
	p := newStorePair(t)

	if _, err := p.migrate(t, nil); err != nil {
		t.Fatalf("第一次搬: %v", err)
	}
	// 搬完:源库留了标记,网关再在这个 MySQL 上启动会被拒绝;再搬一次也被拒绝。
	cfg := &Config{}
	cfg.Database.Driver, cfg.Database.MySQLDSN = "mysql", p.myDSN
	g, err := OpenDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := RefuseIfStoreMoved(cfg, g); err == nil || !strings.Contains(err.Error(), "AEGISDB_DB_DRIVER") {
		t.Fatalf("搬完后网关在 MySQL 上启动应被拒绝,得到 %v", err)
	}
	if _, err := p.migrate(t, nil); err == nil || !strings.Contains(err.Error(), storemigrate.MovedMarkerTable) {
		t.Fatalf("已搬过时应拒绝重搬,得到 %v", err)
	}
	// 人工删掉标记(有意回退):MySQL 上又能启动了。
	p.dropMarker(t)
	if err := RefuseIfStoreMoved(cfg, g); err != nil {
		t.Fatalf("删掉标记后应能启动: %v", err)
	}
	if s, e := g.DB(); e == nil {
		s.Close()
	}

	// 目标里已有数据:不带 --reset 拒绝,带上就清空重搬。
	if _, err := p.migrate(t, nil); err == nil || !strings.Contains(err.Error(), "--reset") {
		t.Fatalf("目标非空时应要求 --reset,得到 %v", err)
	}
	if _, err := p.migrate(t, func(o *storemigrate.Options) { o.Reset = true }); err != nil {
		t.Fatalf("--reset 重搬: %v", err)
	}
	p.dropMarker(t)

	// 时区填错:源审计链验不过 → 拒绝(内容摘要两边照样相等,拦得住它的只有链)。
	old := time.Local
	wrong, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = wrong
	_, err = p.migrate(t, func(o *storemigrate.Options) { o.Reset = true })
	time.Local = old
	if err == nil || !strings.Contains(err.Error(), "--tz") {
		t.Fatalf("时区错时应拒绝并提示 --tz,得到 %v", err)
	}

	// 源库少一个迁移:拒绝。
	if _, err := p.my.Exec("DELETE FROM schema_migrations WHERE version = '0064_store_column_width.sql'"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.migrate(t, func(o *storemigrate.Options) { o.Reset = true }); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("源库缺迁移时应拒绝,得到 %v", err)
	}
	p.my.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES ('0064_store_column_width.sql', NOW())")

	// 源库多一个本版本不认识的迁移:拒绝。
	p.my.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES ('9999_future.sql', NOW())")
	if _, err := p.migrate(t, func(o *storemigrate.Options) { o.Reset = true }); err == nil || !strings.Contains(err.Error(), "不认识") {
		t.Fatalf("源库有未知迁移时应拒绝,得到 %v", err)
	}
	p.my.Exec("DELETE FROM schema_migrations WHERE version = '9999_future.sql'")

	// 源里有 PG 存不下的 NUL 字节:拒绝,并指出哪张表哪一行。
	if _, err := p.my.Exec("UPDATE tbl_user SET name = CONCAT(name, CHAR(0)) WHERE email = 'linwei@aegisdb.io'"); err != nil {
		t.Fatal(err)
	}
	_, err = p.migrate(t, func(o *storemigrate.Options) { o.Reset = true })
	if err == nil || !strings.Contains(err.Error(), "NUL") || !strings.Contains(err.Error(), "tbl_user") {
		t.Fatalf("含 NUL 时应拒绝并指出表,得到 %v", err)
	}
}

// 搬迁期间源库被写了(网关没停干净):核对不通过。
func TestMigrateStore_DetectsConcurrentWrites(t *testing.T) {
	p := newStorePair(t)
	// 在拷贝开始前后插一行:用 Log 回调里的"开始拷贝"作为时机。
	wrote := false
	_, err := p.migrate(t, func(o *storemigrate.Options) {
		inner := o.Log
		o.Log = func(f string, a ...any) {
			inner(f, a...)
			if !wrote && strings.HasPrefix(f, "核对") {
				wrote = true
				if _, err := p.my.Exec("INSERT INTO tbl_notification(user_id, type, title, created_at) VALUES (1, 'x', 'y', NOW())"); err != nil {
					t.Errorf("插入: %v", err)
				}
			}
		}
	})
	if err == nil || !strings.Contains(err.Error(), "没停干净") {
		t.Fatalf("源库在搬迁中被写,应核对失败,得到 %v", err)
	}

}

// 大表吞吐:AEGISDB_TEST_STORE_BIG=<审计行数>(如 2000000)时,往源库审计表灌一条合法的链再搬,
// 记下用时,用来估生产停机窗口。平时不跑。
func TestMigrateStore_Throughput(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("AEGISDB_TEST_STORE_BIG"))
	if n <= 0 {
		t.Skip("设 AEGISDB_TEST_STORE_BIG=<行数> 才跑")
	}
	p := newStorePair(t)
	crypto.SetSecretKey("test-secret")
	var prev string
	if err := p.my.QueryRow("SELECT hash FROM tbl_audit_log ORDER BY id DESC LIMIT 1").Scan(&prev); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.Local)
	const batch = 1000
	_, curGen := service.AuditGenRange()
	cmds := []string{
		"SELECT o.id, o.status, o.amount FROM orders o JOIN users u ON u.id = o.user_id WHERE o.created_at > '2025-01-01' AND u.region = 'hk' ORDER BY o.id DESC LIMIT 200",
		"UPDATE orders SET status = 'shipped', updated_at = NOW() WHERE id IN (1001, 1002, 1003)",
		"ALTER TABLE orders ADD INDEX idx_created (created_at), ALGORITHM=INPLACE, LOCK=NONE",
		"登录成功",
	}
	for i := 0; i < n; i += batch {
		var sb strings.Builder
		sb.WriteString("INSERT INTO tbl_audit_log (occurred_at, actor_id, actor_name, connection_id, instance, env, tier_code, db_name, command, risk, result, approval_no, operator, prev_hash, hash, event_type, data_action, client_ip, entry_source) VALUES ")
		args := make([]any, 0, batch*19)
		for j := i; j < i+batch && j < n; j++ {
			a := &model.AuditLog{
				OccurredAt: base.Add(time.Duration(j) * 3 * time.Second).Add(time.Duration(j%1000) * time.Millisecond),
				ActorID: int64(j%50 + 1), ActorName: fmt.Sprintf("用户%02d", j%50), ConnectionID: int64(j%7 + 1),
				Instance: "orders-prod", Env: "prod-hk", TierCode: "prod", Database: "orders",
				Command: cmds[j%len(cmds)] + fmt.Sprintf(" /* %d */", j), Risk: "low", Result: "executed",
				EventType: "exec", DataAction: "read", ClientIP: "10.1.2.3", Source: "terminal", PrevHash: prev,
			}
			a.Hash = crypto.ChainHash(prev, service.AuditPayloadGen(a, curGen))
			prev = a.Hash
			if j > i {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
			args = append(args, a.OccurredAt, a.ActorID, a.ActorName, a.ConnectionID, a.Instance, a.Env, a.TierCode, a.Database,
				a.Command, a.Risk, a.Result, a.ApprovalNo, a.Operator, a.PrevHash, a.Hash, a.EventType, a.DataAction, a.ClientIP, a.Source)
		}
		if _, err := p.my.Exec(sb.String(), args...); err != nil {
			t.Fatalf("灌数据: %v", err)
		}
	}
	t.Logf("灌入 %d 行审计,用时 %s", n, time.Since(start))
	rep, err := p.migrate(t, func(o *storemigrate.Options) { o.Workers = 4 })
	if err != nil {
		t.Fatalf("migrate-store: %v", err)
	}
	t.Logf("搬迁总用时 %s(含源链校验、拷贝、核对、目标链校验)", rep.Took)
}
