package repository

import (
	"testing"

	"gorm.io/gorm"

	"velagateway/internal/model"
	"velagateway/internal/testsupport"
)

// 拦截数按**审计行自己的快照**算,不按实例此刻绑在哪一层算。
//
// 双快照(issue 03)存在的全部意义就是这个:这一行说「一条命令当时被判成 high」,而只有
// 当时那一层能解释为什么。拿当前绑定去 join 有两个后果:
//
//	· 把一台实例从 prod 改绑到 dev,**历史拦截数当场变少** —— 上个月发生过的事不会因为
//	  今天改了配置就没发生
//	· 连接被删之后,它名下所有审计行**一条都不算** —— 而删掉一台实例恰恰是"那段历史更
//	  值得留着"的时候
func newAuditDB(t *testing.T) (*gorm.DB, *Repo) {
	t.Helper()
	db := testsupport.NewDB(t)
	// prod 计入待处置,dev 不计入。
	for _, tier := range []model.EnvTier{
		{Code: "prod", DisplayName: "生产", CountsInPending: true},
		{Code: "dev", DisplayName: "开发", CountsInPending: false},
	} {
		if err := db.Select("*").Create(&tier).Error; err != nil {
			t.Fatalf("seed tier: %v", err)
		}
	}
	for _, env := range []model.Environment{
		{Code: "prod", DisplayName: "生产", TierCode: "prod"},
		{Code: "dev", DisplayName: "开发", TierCode: "dev"},
	} {
		if err := db.Create(&env).Error; err != nil {
			t.Fatalf("seed env: %v", err)
		}
	}
	return db, New(db)
}

func TestCountProdInterceptions_UsesTheRowsOwnSnapshot(t *testing.T) {
	db, repo := newAuditDB(t)

	conn := model.Connection{Name: "order-cluster", Engine: "MySQL", Host: "h", Port: 3306,
		Env: "prod", Policy: "strict", DefaultRole: "dba_l2", Status: "online"}
	if err := db.Create(&conn).Error; err != nil {
		t.Fatalf("seed conn: %v", err)
	}
	// 一行 PROD 的拦截,快照写着 prod。
	if err := db.Create(&model.AuditLog{
		ConnectionID: conn.ID, Env: "prod", TierCode: "prod",
		Command: "DROP TABLE t", Risk: "high", Result: "pending", ActorName: "lin",
	}).Error; err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	n, err := repo.CountProdInterceptions()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("前置条件不成立:应当数到 1 条,实际 %d", n)
	}

	// ① 把这台实例改绑到 dev —— 历史不该因此变少。
	if err := db.Model(&model.Connection{}).Where("id = ?", conn.ID).
		Update("env", "dev").Error; err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if n, _ := repo.CountProdInterceptions(); n != 1 {
		t.Errorf("改绑之后历史拦截数变成 %d —— 上个月发生过的事不会因为今天改了配置就没发生", n)
	}

	// ② 把这台实例删掉 —— 那段历史更该留着。
	if err := db.Delete(&model.Connection{}, conn.ID).Error; err != nil {
		t.Fatalf("delete conn: %v", err)
	}
	if n, _ := repo.CountProdInterceptions(); n != 1 {
		t.Errorf("删掉实例之后它名下的审计行一条都不算了(得到 %d)—— 而删实例恰恰是那段历史"+
			"更值得留着的时候", n)
	}
}

// 快照之前的老行没有 tier_code,只能回退到当前绑定 —— 尽力而为,而不是一概不算。
func TestCountProdInterceptions_FallsBackForRowsPredatingTheSnapshot(t *testing.T) {
	db, repo := newAuditDB(t)
	conn := model.Connection{Name: "legacy", Engine: "MySQL", Host: "h", Port: 3306,
		Env: "prod", Policy: "strict", DefaultRole: "dba_l2", Status: "online"}
	if err := db.Create(&conn).Error; err != nil {
		t.Fatalf("seed conn: %v", err)
	}
	if err := db.Create(&model.AuditLog{
		ConnectionID: conn.ID, Command: "DROP TABLE t", Risk: "high",
		Result: "rejected", ActorName: "lin", // Env / TierCode 为空:拆分之前的行
	}).Error; err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	if n, _ := repo.CountProdInterceptions(); n != 1 {
		t.Errorf("没有快照的老行应当回退到当前绑定去算,实际数到 %d", n)
	}
}
