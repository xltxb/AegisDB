package service

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	_ "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"velagateway/internal/model"
	"velagateway/internal/repository"
)

// 2026-11 生产:元数据同步每一轮都 tables_dropped=0、tables_added=0,而 cols_dropped 与
// cols_added 相等且很大(conn 53:kept 451 / dropped 727 / added 727)—— 表结构没变,
// 一半以上的列每轮被删了重插。
//
// 原因:MetaColumn.Nullable 带 GORM 标签 default:true。GORM 建行时把零值(false)换成
// 默认值,于是每个 NOT NULL 列都以 nullable=true 落库,下一轮新读出来的 false 永远对不上。
// 被重写的正好是 NOT NULL 列,缓存里的"可空"也一直是错的。
//
// 这里走完整的 syncOneConnection 连跑两次:第二次每一列都应当原地不动(行 id 不变)。

type metaStore struct {
	name string
	db   *gorm.DB
}

func openMetaStores(t *testing.T) []metaStore {
	t.Helper()
	gcfg := &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)}
	var out []metaStore
	lite, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "store.db")), gcfg)
	if err != nil {
		t.Fatal(err)
	}
	if s, e := lite.DB(); e == nil {
		t.Cleanup(func() { s.Close() })
	}
	out = append(out, metaStore{"sqlite", lite})

	// 真存储库:与生产同一种驱动,GORM 对它们生成的 INSERT 才是生产上那一条。
	if base := os.Getenv("VELA_TEST_STORE_MYSQL"); base != "" { // root:pw@tcp(127.0.0.1:3407)/
		name := fmt.Sprintf("vt_metaresync_%d", time.Now().UnixNano()%1_000_000)
		admin, err := sql.Open("mysql", base)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.Exec("DROP DATABASE IF EXISTS " + name); admin.Close() })
		db, err := gorm.Open(mysql.Open(base+name+"?charset=utf8mb4&parseTime=true&loc=Local"), gcfg)
		if err != nil {
			t.Fatal(err)
		}
		if s, e := db.DB(); e == nil {
			t.Cleanup(func() { s.Close() })
		}
		out = append(out, metaStore{"mysql", db})
	}
	if base := os.Getenv("VELA_TEST_STORE_PG"); base != "" { // host=… port=… user=… password=… sslmode=disable
		name := fmt.Sprintf("vt_metaresync_%d", time.Now().UnixNano()%1_000_000)
		admin, err := gorm.Open(postgres.Open(base+" dbname=postgres"), gcfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
			t.Fatal(err)
		}
		db, err := gorm.Open(postgres.Open(base+" dbname="+name), gcfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if s, e := db.DB(); e == nil {
				s.Close()
			}
			admin.Exec("DROP DATABASE IF EXISTS " + name)
			if s, e := admin.DB(); e == nil {
				s.Close()
			}
		})
		out = append(out, metaStore{"pg", db})
	}
	for _, st := range out {
		if err := st.db.AutoMigrate(&model.MetaTable{}, &model.MetaColumn{}, &model.MetaSync{}); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
	}
	return out
}

// colRows 一台实例缓存里的全部列,按 库.表.列。
func colRows(t *testing.T, db *gorm.DB, connID int64) map[string]model.MetaColumn {
	t.Helper()
	var cs []model.MetaColumn
	if err := db.Where("connection_id = ?", connID).Find(&cs).Error; err != nil {
		t.Fatal(err)
	}
	out := make(map[string]model.MetaColumn, len(cs))
	for _, c := range cs {
		out[c.DBName+"."+c.TableName_+"."+c.Name] = c
	}
	return out
}

// rewritten 第二次同步后 id 变了(被删了重插)或消失了的列;db 非空时只看那个库 ——
// 真实例上别的测试包会并发建删库,它们的变化不归这里管。
func rewritten(before, after map[string]model.MetaColumn, db string) []string {
	var out []string
	for k, b := range before {
		if db != "" && b.DBName != db {
			continue
		}
		if a, ok := after[k]; !ok || a.ID != b.ID {
			out = append(out, k)
		}
	}
	return out
}

func TestMetaSync_UnchangedColumnsKept_SQLiteRemote(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.db")
	rdb, err := sql.Open("sqlite", remote)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rdb.Exec(`CREATE TABLE orders (
		id INTEGER NOT NULL PRIMARY KEY,
		sn VARCHAR(32) NOT NULL DEFAULT '',
		amount INTEGER NOT NULL DEFAULT 0,
		note TEXT)`); err != nil {
		t.Fatal(err)
	}
	rdb.Close()
	conn := &model.Connection{ID: 7, Name: "lite", Engine: "SQLite", Database: remote}

	for _, st := range openMetaStores(t) {
		t.Run(st.name, func(t *testing.T) {
			s := &Services{Repo: repository.New(st.db)}
			if err := s.syncOneConnection(conn); err != nil {
				t.Fatal(err)
			}
			first := colRows(t, st.db, conn.ID)
			if len(first) != 4 {
				t.Fatalf("应缓存 4 列,实际 %d", len(first))
			}
			for k, want := range map[string]bool{"orders.sn": false, "orders.amount": false, "orders.note": true} {
				var got *model.MetaColumn
				for _, c := range first {
					if c.TableName_+"."+c.Name == k {
						c := c
						got = &c
					}
				}
				if got == nil || got.Nullable != want {
					t.Errorf("%s 的可空应为 %v,缓存里是 %+v", k, want, got)
				}
			}
			if err := s.syncOneConnection(conn); err != nil {
				t.Fatal(err)
			}
			if re := rewritten(first, colRows(t, st.db, conn.ID), ""); len(re) > 0 {
				t.Errorf("表结构没变,第二次同步却重写了 %d 列: %v", len(re), re)
			}
		})
	}
}

// 真 MySQL 远端(VELA_TEST_MYSQL=host:port/user/password):一张宽表,长注释、超长注释、
// 各种默认值、NOT NULL、长 ENUM、大小写混写的列名。第二次同步应当一列不动;改了的列
// (包括注释被截断的列在注释真改了以后)要更新。
func TestMetaSync_UnchangedColumnsKept_MySQLRemote(t *testing.T) {
	v := os.Getenv("VELA_TEST_MYSQL")
	if v == "" {
		t.Skip("未设置 VELA_TEST_MYSQL")
	}
	p := strings.SplitN(v, "/", 3)
	hp := strings.SplitN(p[0], ":", 2)
	port, _ := strconv.Atoi(hp[1])
	admin, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/", p[1], p[2], p[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("zz_metaresync_%d", time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + schema + " CHARACTER SET utf8mb4"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP DATABASE " + schema)

	longComment := strings.Repeat("长注释", 300)  // 900 字,超过缓存的 500,会被截断
	midComment := strings.Repeat("备注 note ", 40) // 未截断
	enumVals := make([]string, 60)
	for i := range enumVals {
		enumVals[i] = fmt.Sprintf("'state_value_%02d'", i)
	}
	ddl := fmt.Sprintf("CREATE TABLE %s.wide (\n", schema) + strings.Join([]string{
		"id BIGINT NOT NULL AUTO_INCREMENT COMMENT '主键'",
		"OrderNo VARCHAR(64) NOT NULL DEFAULT '' COMMENT '" + midComment + "'",
		"amount DECIMAL(18,2) NOT NULL DEFAULT 0.00",
		"qty INT NULL DEFAULT NULL",
		"status ENUM(" + strings.Join(enumVals, ",") + ") NOT NULL DEFAULT 'state_value_00'",
		"remark TEXT COMMENT '" + longComment + "'",
		"flag TINYINT(1) NOT NULL DEFAULT 1",
		"price DOUBLE NOT NULL DEFAULT 1.5",
		"created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)",
		"updated_at TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP",
		"title VARCHAR(255) CHARACTER SET latin1 COLLATE latin1_bin NOT NULL DEFAULT 'x' COMMENT 'trailing space '",
		"uid CHAR(36) NULL DEFAULT ''",
		"js JSON NULL",
		"PRIMARY KEY (id)",
		"KEY idx_order (OrderNo)",
	}, ",\n") + ") COMMENT='宽表'"
	if _, err := admin.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	conn := &model.Connection{ID: 53, Name: "mysql-it", Engine: "MySQL 8.0", Host: hp[0], Port: port,
		Username: p[1], Password: p[2], Database: schema}

	pick := func(m map[string]model.MetaColumn, col string) model.MetaColumn {
		for _, c := range m {
			if c.DBName == schema && c.TableName_ == "wide" && c.Name == col {
				return c
			}
		}
		t.Fatalf("缓存里没有 %s", col)
		return model.MetaColumn{}
	}

	for _, st := range openMetaStores(t) {
		t.Run(st.name, func(t *testing.T) {
			// 每个存储库从同一张表的原样开始(上一轮子测试改过注释)。
			if _, err := admin.Exec(fmt.Sprintf("ALTER TABLE %s.wide MODIFY remark TEXT COMMENT '%s', MODIFY qty INT NULL DEFAULT NULL", schema, longComment)); err != nil {
				t.Fatal(err)
			}
			s := &Services{Repo: repository.New(st.db)}
			if err := s.syncOneConnection(conn); err != nil {
				t.Fatal(err)
			}
			first := colRows(t, st.db, conn.ID)
			if c := pick(first, "amount"); c.Nullable {
				t.Errorf("amount 是 NOT NULL,缓存里却是可空")
			}
			if c := pick(first, "qty"); !c.Nullable {
				t.Errorf("qty 可空,缓存里却是 NOT NULL")
			}
			if err := s.syncOneConnection(conn); err != nil {
				t.Fatal(err)
			}
			second := colRows(t, st.db, conn.ID)
			if re := rewritten(first, second, schema); len(re) > 0 {
				t.Errorf("表结构没变,第二次同步却重写了 %d 列: %v", len(re), re)
			}

			// 真改了的列要更新:截断注释的列改注释(前 500 字内)、可空改 NOT NULL。
			newComment := "改过了" + longComment
			if _, err := admin.Exec(fmt.Sprintf("ALTER TABLE %s.wide MODIFY remark TEXT COMMENT '%s', MODIFY qty INT NOT NULL DEFAULT 0", schema, newComment)); err != nil {
				t.Fatal(err)
			}
			if err := s.syncOneConnection(conn); err != nil {
				t.Fatal(err)
			}
			third := colRows(t, st.db, conn.ID)
			if c := pick(third, "remark"); !strings.HasPrefix(c.Comment, "改过了") {
				t.Errorf("remark 注释改了,缓存没跟上: %.20q", c.Comment)
			}
			if c := pick(third, "qty"); c.Nullable || c.ColDefault != "0" {
				t.Errorf("qty 改成 NOT NULL DEFAULT 0,缓存没跟上: %+v", c)
			}
			re := rewritten(second, third, schema)
			if len(re) != 2 {
				t.Errorf("只有 remark、qty 两列变了,实际重写 %v", re)
			}
		})
	}
}
