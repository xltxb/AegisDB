package bootstrap

// 生产的存储库表结构只由 migrations/*.sql 决定,而测试从前跑在按 GORM 模型建表、
// 不检查 VARCHAR 长度的存储库上 —— 两份"表结构"从来没被放在一起比过。2026-11-16 改跑真 MySQL
// 评估时当场查出两处:tbl_user.mfa_secret 仍是 VARCHAR(64),装不下静态加密后的 TOTP 种子;
// tbl_connection.env 被 0034 对齐排序规则时改回了 VARCHAR(16)(0013 才把它加宽到 32)。
//
// 这条测试把两份对齐:在真 MySQL 上跑完全部迁移,模型里声明了长度的每个字符串字段,库里的
// 列都必须存在、且不比模型窄。设 AEGISDB_TEST_STORE_MYSQL=user:pw@tcp(host:port)/ 才跑。

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestMigrations_ColumnWidthsCoverModels_RealMySQL(t *testing.T) {
	base := os.Getenv("AEGISDB_TEST_STORE_MYSQL")
	if base == "" {
		t.Skip("未设置 AEGISDB_TEST_STORE_MYSQL,跳过真实 MySQL 存储库用例")
	}
	name := fmt.Sprintf("vt_colwidth_%d", os.Getpid())
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

	cfg := &Config{}
	cfg.Database.Driver = "mysql"
	cfg.Database.MySQLDSN = base + name + "?charset=utf8mb4&parseTime=true&loc=Local"
	cfg.Gateway.StrictMode = true
	db, err := OpenDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s, e := db.DB(); e == nil {
		defer s.Close()
	}
	if err := Migrate(cfg, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	type col struct {
		typ string
		max sql.NullInt64
	}
	cols := map[string]col{} // table.column
	rows, err := admin.Query(`SELECT table_name, column_name, data_type, character_maximum_length
		FROM information_schema.columns WHERE table_schema = ?`, name)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tb, cn, typ string
		var max sql.NullInt64
		if err := rows.Scan(&tb, &cn, &typ, &max); err != nil {
			t.Fatal(err)
		}
		cols[tb+"."+cn] = col{typ, max}
	}
	rows.Close()

	cache := &sync.Map{}
	checked := 0
	for _, m := range allModels {
		s, err := schema.Parse(m, cache, db.NamingStrategy)
		if err != nil {
			t.Fatalf("parse %T: %v", m, err)
		}
		for _, f := range s.Fields {
			if f.DBName == "" || f.DataType != schema.String || f.Size <= 0 {
				continue
			}
			key := s.Table + "." + f.DBName
			c, ok := cols[key]
			if !ok {
				t.Errorf("%s:模型 %s.%s 声明了这一列,迁移后的 MySQL 里没有", key, s.Name, f.Name)
				continue
			}
			checked++
			// TEXT 一族没有 VARCHAR 那样的长度上限问题(最小的 TEXT 也有 65535 字节)
			if c.typ != "varchar" && c.typ != "char" {
				continue
			}
			if c.max.Valid && c.max.Int64 < int64(f.Size) {
				t.Errorf("%s:MySQL 列是 %s(%d),比模型声明的 size:%d 窄 —— 生产写入超长值会报 1406 或被截断",
					key, c.typ, c.max.Int64, f.Size)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("只核对了 %d 列,模型解析多半出了问题", checked)
	}
	t.Logf("核对了 %d 个带长度的字符串列", checked)
}
