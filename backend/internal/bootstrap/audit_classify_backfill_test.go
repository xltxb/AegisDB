package bootstrap

// 历史审计行的分类回填:**算得出来的补上,算不出来的留空**。
//
// 加完 0039 / 0040 之后,界面上绝大多数行两列都是短横 —— 一张只对最近几条成立的列,几乎
// 回答不了任何事后问题。但回填有一条线不能过:不猜。在一张哈希链审计表上尤其不能破。
//
// 所以这里钉的是那条线画在哪儿:
//
//   增删改查   补 —— 它是 command 的纯函数,而 command 存着、还进了哈希。今天算的和当时
//              算的是同一个答案。而且要用**同一个分类器**:另写一份的话,历史行和新行会按
//              两套规则归类,摆在同一列里谁也看不出来。
//   事件类型   只补登录 —— 它的命令文本就是 "login" 且 connection_id 为 0,没有第二条路
//              写得出这个组合。intercept / approve / exec 区分不开,留空。

import (
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// oldAuditRow 插一条"迁移之前写下的"审计行:两列分类都是空的。
func oldAuditRow(t *testing.T, db *gorm.DB, command string, connID int64, risk, result string) int64 {
	t.Helper()
	a := &model.AuditLog{
		OccurredAt: time.Now().Add(-72 * time.Hour),
		ActorID:    1, ActorName: "Lin Wei", ConnectionID: connID,
		Command: command, Risk: risk, Result: result,
		// 两列留空 —— 这正是 0039 / 0040 之前那些行的样子。
		EventType: "", DataAction: "",
		PrevHash: "old-" + command + itoa(connID) + risk + result, Hash: "old-hash",
	}
	if err := db.Create(a).Error; err != nil {
		t.Fatalf("插历史行: %v", err)
	}
	return a.ID
}

func auditRowByID(t *testing.T, db *gorm.DB, id int64) model.AuditLog {
	t.Helper()
	var got model.AuditLog
	if err := db.First(&got, id).Error; err != nil {
		t.Fatalf("读回 %d: %v", id, err)
	}
	return got
}

func TestAuditBackfill_FillsWhatIsComputableAndLeavesTheRest(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	// 五条历史行,覆盖"算得出来"与"算不出来"两边。
	login := oldAuditRow(t, db, "login", 0, model.RiskLow, model.ResultExecuted)
	loginFail := oldAuditRow(t, db, "login failed", 0, model.RiskLow, model.ResultRejected)
	drop := oldAuditRow(t, db, "DROP TABLE orders_2024_q3;", 2, model.RiskHigh, model.ResultPending)
	// 多语句:按第一条算会是"查",正确答案是"删"。回填必须走同一个分类器才会得到删。
	script := oldAuditRow(t, db, "select 1; drop table t_tmp;", 2, model.RiskMid, model.ResultExecuted)
	// 一条配置变更:不是数据操作,而且它的事件类型当年就没记 —— 两边的结论各不相同。
	cfg := oldAuditRow(t, db, "admin.role.capabilities role=dba_l2 value={}", 0, model.RiskMid, model.ResultExecuted)

	if err := backfillAuditClassification(db); err != nil {
		t.Fatalf("回填: %v", err)
	}

	// ---- 增删改查:五条都补上了,而且答案对 ----
	for _, c := range []struct {
		id   int64
		want string
		what string
	}{
		{login, "other", "登录不是数据操作"},
		{loginFail, "other", "登录失败同上"},
		{drop, "delete", "DROP 是删"},
		{script, "delete", "多语句取最重的一条 —— 按第一条算会错成「查」"},
		{cfg, "other", "配置变更不是对数据的操作"},
	} {
		if got := auditRowByID(t, db, c.id).DataAction; got != c.want {
			t.Errorf("%s:应回填成 %q,实际 %q", c.what, c.want, got)
		}
	}

	// ---- 事件类型:只有登录那两条 ----
	if got := auditRowByID(t, db, login).EventType; got != model.AuditEventLogin {
		t.Errorf("登录行的事件类型应补成 login,实际 %q", got)
	}
	if got := auditRowByID(t, db, loginFail).EventType; got != model.AuditEventLogin {
		t.Errorf("登录失败行同上,实际 %q", got)
	}
	for _, c := range []struct {
		id   int64
		what string
	}{
		{drop, "被拦下来的命令"},
		{script, "执行过的脚本"},
		{cfg, "配置变更"},
	} {
		if got := auditRowByID(t, db, c.id).EventType; got != "" {
			t.Errorf("%s的事件类型算不出来,应留空而不是猜一个,实际 %q", c.what, got)
		}
	}
}

// 幂等:再跑一遍不改变任何东西,也不会把已经补好的值覆盖掉。
func TestAuditBackfill_IsIdempotent(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	id := oldAuditRow(t, db, "DROP TABLE orders;", 2, model.RiskHigh, model.ResultPending)
	if err := backfillAuditClassification(db); err != nil {
		t.Fatalf("第一遍: %v", err)
	}
	first := auditRowByID(t, db, id)

	// 中间手工改掉一条**已经有值**的行,再跑一遍:回填只碰空的,不该动它。
	// (这一条同时说明回填不会拿今天的规则去覆盖当年记下的结论。)
	if err := db.Model(&model.AuditLog{}).Where("id = ?", id).
		Update("data_action", "read").Error; err != nil {
		t.Fatalf("改: %v", err)
	}
	if err := backfillAuditClassification(db); err != nil {
		t.Fatalf("第二遍: %v", err)
	}
	if got := auditRowByID(t, db, id).DataAction; got != "read" {
		t.Errorf("回填只该补空的那些 —— 已有值的行被改成了 %q(第一遍算出来的是 %q)", got, first.DataAction)
	}
}

// 它必须挂在**真实的 schema 路径**上,而不是只有测试直接调得到。
//
// 两条路:生产走 SQL 迁移(Migrate),dev/测试走 AutoMigrate。漏掉任何一条,那条路上的库
// 就永远带着一屏短横,而且没有任何地方会报错 —— 所以这里把 AutoMigrate 那条跑一遍,
// 并对源码检查另一条确实也挂了。
func TestAuditBackfill_HangsOffTheSchemaPaths(t *testing.T) {
	app := newTestApp(t)
	db := app.repo.DB()

	id := oldAuditRow(t, db, "TRUNCATE TABLE orders;", 2, model.RiskHigh, model.ResultExecuted)
	if got := auditRowByID(t, db, id).DataAction; got != "" {
		t.Fatalf("前置:这一行应当是空的,实际 %q", got)
	}
	// 跑当前存储库驱动真正的 schema 路径:SQLite 是 AutoMigrate,PG 是 SQL 迁移(Migrate)。
	schemaPath := autoMigrate
	if app.cfg.Database.Driver == "postgres" {
		schemaPath = func(db *gorm.DB) error { return Migrate(app.cfg, db) }
	}
	if err := schemaPath(db); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	if got := auditRowByID(t, db, id).DataAction; got != "delete" {
		t.Errorf("AutoMigrate 那条路上没跑回填 —— 这一行仍是 %q", got)
	}

	// SQL 迁移那条路跑不起来(要一个真的 MySQL),所以在源码上确认它也挂了。
	src, err := os.ReadFile("migrate.go")
	if err != nil {
		t.Fatalf("读 migrate.go: %v", err)
	}
	if n := strings.Count(string(src), "backfillAuditClassification("); n < 2 {
		t.Errorf("migrate.go 里只出现了 %d 次 backfillAuditClassification —— "+
			"两条 schema 路径(Migrate 与 autoMigrate)都要挂,漏掉的那条上的库会永远带着一屏短横", n)
	}
}
