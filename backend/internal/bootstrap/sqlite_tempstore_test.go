package bootstrap

import (
	"path/filepath"
	"testing"
)

// issue #92:临时目录不可写时(只读容器、受限沙箱),批量 INSERT ... RETURNING 要落临时文件,
// SQLite 报 "unable to open database file (14)",元数据同步整个失败。临时表放内存就不依赖它。
func TestOpenDB_SQLiteKeepsTempStoreInMemory(t *testing.T) {
	for _, path := range []string{"plain.db", "query.db?_pragma=synchronous(1)"} {
		cfg := &Config{}
		cfg.Database.Driver = "sqlite"
		cfg.Database.SQLitePath = filepath.Join(t.TempDir(), path)
		db, err := OpenDB(cfg)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		var got int
		err = sqlDB.QueryRow("PRAGMA temp_store").Scan(&got)
		sqlDB.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got != 2 { // 0 默认(文件) · 1 文件 · 2 内存
			t.Errorf("%s: temp_store = %d,应为 2(MEMORY)", path, got)
		}
	}
}
