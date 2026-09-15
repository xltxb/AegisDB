package bootstrap

import (
	"fmt"
	"io/fs"
	"sort"

	"gorm.io/gorm"

	"velagateway/migrations"
)

// MigrationsFS 是嵌入的迁移目录。抽成函数有两个用处:让 cmd/server 不必自己
// import migrations,也让测试能换成别的 fs.FS 去伪造「有新版本待应用」的状态。
func MigrationsFS() fs.FS { return migrations.FS }

// VerifySchemaCurrent 报告这个库的 schema 是否已经追上了二进制里带的迁移。
//
// 它是 --no-migrate 的安全底座。那个开关的用意是把「迁移」与「服务」重新分开,
// 让部署流程自己决定谁来跑 —— 多副本滚动升级时,第一个重启的副本自己改共享库
// 是一件需要能关掉的事。
//
// 但「跳过迁移」不能等于「什么都不检查」。serve 不迁移、而人也忘了跑,副本就对着
// 旧表结构服务;而一台表不全的网关照样监听端口、照样让 /healthz 变绿,每个请求
// 500 —— 那正是 Migrate 被挂进 serve 路径之前的形状(见 main.go 那段注释)。开关
// 若只是一条 if,等于把刚补上的洞重新凿开。
//
// 所以语义是:不**应用**,但**验证**。待应用的版本一条都没有才放行,否则拒绝启动
// 并点名差的是哪一条 —— 「schema 不是最新」这句话本身没法让运维知道该跑什么。
func VerifySchemaCurrent(db *gorm.DB, srcFS fs.FS) error {
	entries, err := fs.Glob(srcFS, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)

	// 账本表不存在 = 这个库从来没被迁移过。空库当成「没有待应用的版本」放过去,
	// 正是这个函数要挡的那种误用。
	if !db.Migrator().HasTable(&schemaMigration{}) {
		return fmt.Errorf("这个库还没有 schema_migrations 账本 —— 它从未被迁移过。"+
			"先跑一次 `vela-gateway migrate`(共 %d 条待应用)", len(entries))
	}

	applied := map[string]bool{}
	var rows []schemaMigration
	if err := db.Find(&rows).Error; err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for _, r := range rows {
		applied[r.Version] = true
	}

	var pending []string
	for _, name := range entries {
		if !applied[name] {
			pending = append(pending, name)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("schema 落后于这个二进制,%d 条迁移待应用:%v。"+
			"要么去掉 --no-migrate,要么先在别处跑 `vela-gateway migrate`",
			len(pending), pending)
	}
	return nil
}
