package bootstrap

import (
	"strings"
	"testing"
	"testing/fstest"

	"velagateway/internal/testsupport"
)

// --no-migrate 的全部安全性系于这个函数。
//
// 开关的用意是把「迁移」与「服务」重新分开,让部署流程自己决定谁来跑。但它一旦
// 只是「跳过 Migrate」,就等于把刚修好的洞重新打开:serve 不迁移、人也忘了跑,
// 副本就对着旧表结构服务 —— 那正是 Task 3 之前的形状(一台没有表的网关照样监听,
// 每个请求 500,而探活是绿的)。
//
// 所以它的语义是「不**应用**迁移,但**验证**已经是最新」:待应用的版本一条都没有
// 才放行,否则拒绝启动并把差在哪说出来。
func TestVerifySchemaCurrent_PassesWhenNothingPending(t *testing.T) {
	db := testsupport.NewDB(t)
	cfg := &Config{}
	cfg.Gateway.StrictMode = true
	if err := Migrate(cfg, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := VerifySchemaCurrent(db, MigrationsFS()); err != nil {
		t.Errorf("迁移刚跑完,应当认为 schema 是最新的,却报: %v", err)
	}
}

// 有待应用的版本时必须拒绝,而且要说出差的是哪一个 —— 「schema 不是最新」这句话
// 本身没法让运维知道该跑什么。
func TestVerifySchemaCurrent_RefusesWhenSomethingIsPending(t *testing.T) {
	db := testsupport.NewDB(t)
	cfg := &Config{}
	cfg.Gateway.StrictMode = true
	if err := Migrate(cfg, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// 伪造一个「新版本已发布但这个库还没应用」的状态。
	future := fstest.MapFS{
		"0001_init.sql":      {Data: []byte("-- 已应用")},
		"0002_something.sql": {Data: []byte("-- 尚未应用")},
	}
	err := VerifySchemaCurrent(db, future)
	if err == nil {
		t.Fatal("有一条待应用的迁移,VerifySchemaCurrent 却放行了")
	}
	if !strings.Contains(err.Error(), "0002_something.sql") {
		t.Errorf("错误信息该点名差的是哪一条,实际是: %v", err)
	}
}

// 空库 —— 连账本表都还没有。这是「装了新二进制、库却是全新的」那种误用,
// 必须拒绝,而不是把空库当成「没有待应用的版本」放过去。
func TestVerifySchemaCurrent_RefusesAnEmptyDatabase(t *testing.T) {
	db := testsupport.NewDB(t)

	err := VerifySchemaCurrent(db, MigrationsFS())
	if err == nil {
		t.Fatal("库里一条迁移都没应用过,VerifySchemaCurrent 却放行了")
	}
}
