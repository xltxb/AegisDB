package repository

import (
	"testing"

	"velagateway/internal/testsupport"
)

// 连接名是操作者在实例树里识别数据库实例的主要方式,而且这一列没有任何应用层查重
// (建连接/改连接都只 TrimSpace,不折大小写,也没有 GetConnectionByName 之类的预检
// 查),唯一性完全靠 idx_connection_name 这条 DB 索引兜底。MySQL 的 utf8mb4_unicode_ci
// 下 "PROD-DB" 和 "prod-db" 曾经互斥;PG 下如果索引仍是裸列,两者能并存,操作者就会
// 在树里看到两个"看起来同名"的连接——选错实例执行 SQL,是这个网关要防的核心风险。
func TestConnectionNameUniqueIndexIsCaseInsensitive(t *testing.T) {
	db := testsupport.NewDB(t)

	if err := db.Exec(`INSERT INTO tbl_connection (name, engine, host, port, env, policy)
	                   VALUES ('PROD-DB', 'mysql', '127.0.0.1', 3306, 'prod', 'strict')`).Error; err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	err := db.Exec(`INSERT INTO tbl_connection (name, engine, host, port, env, policy)
	                VALUES ('prod-db', 'mysql', '127.0.0.1', 3307, 'prod', 'strict')`).Error
	if err == nil {
		t.Fatal("插入 name 仅大小写不同的第二个连接本应被唯一约束拒绝,却成功了")
	}
}

// 项目名同理,后果比连接名轻(组织混乱而非误操作执行),但同一条裸列唯一索引模式。
func TestProjectNameUniqueIndexIsCaseInsensitive(t *testing.T) {
	db := testsupport.NewDB(t)

	if err := db.Exec(`INSERT INTO tbl_project (name) VALUES ('Foo')`).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}

	err := db.Exec(`INSERT INTO tbl_project (name) VALUES ('foo')`).Error
	if err == nil {
		t.Fatal("插入 name 仅大小写不同的第二个项目本应被唯一约束拒绝,却成功了")
	}
}
