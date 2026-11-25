package osc

// 任意 ALTER 走在线变更(工单 08),对着真 MySQL + 真 binlog。
//
// 对照的办法:两张一模一样的表 t 与 ref,并发写入在同一个事务里同时写两张。t 走在线变更,ref 等
// 写入停了之后执行原生的同一条 ALTER —— 两张表的结构与每一行的每一列都要逐字节相同。原生 ALTER
// 的结果就是"正确答案",不必为每种变更手写期望值。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const alterDDL = `CREATE TABLE %s (
	id BIGINT NOT NULL,
	a  INT NOT NULL,
	b  VARCHAR(64) CHARACTER SET utf8mb3 NULL,
	c  DECIMAL(10,2) NOT NULL DEFAULT 0,
	d  DATETIME(3) NULL,
	e  TEXT NULL,
	u  INT NULL,
	PRIMARY KEY (id),
	KEY ia (a)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb3`

func seedAlterTables(t *testing.T, db *sql.DB, n int, tables ...string) {
	t.Helper()
	for _, tb := range tables {
		mustExec(t, db, fmt.Sprintf(alterDDL, tb))
		var vals []string
		for i := 1; i <= n; i++ {
			u := fmt.Sprint(i * 10)
			if i%50 == 0 {
				u = "NULL"
			}
			vals = append(vals, fmt.Sprintf("(%d, %d, '名字-%d', %d.25, '2026-10-02 08:00:%02d.123', 'text %d', %s)", i, i%100, i, i, i%60, i, u))
			if len(vals) == 500 || i == n {
				mustExec(t, db, "INSERT INTO "+tb+" VALUES "+strings.Join(vals, ","))
				vals = nil
			}
		}
	}
}

// alterWriters 不停地在同一个事务里对 t 与 ref 做同样的插入 / 更新 / 删除。只碰 id、a、b、u 四列 ——
// 这几列在所有用例的变更前后都还在,写入语句在切换前后都有效。
func alterWriters(ctx context.Context, db *sql.DB, n int, dupU bool) (stop func() error) {
	done := make(chan struct{})
	errs := make(chan error, 2)
	var nextID int64 = 1_000_000
	for w := 0; w < 2; w++ {
		go func(w int) {
			errs <- func() error {
				r := rand.New(rand.NewSource(int64(w) + 7))
				for i := 0; ; i++ {
					select {
					case <-done:
						return nil
					default:
					}
					tx, err := db.BeginTx(ctx, nil)
					if err != nil {
						return err
					}
					var q string
					var args []any
					switch r.Intn(3) {
					case 0:
						id := atomic.AddInt64(&nextID, 1)
						u := id * 10
						if dupU {
							u = 10 // 与种子里 id=1 那一行撞上
						}
						q, args = "INSERT INTO %s (id, a, b, u) VALUES (?, ?, ?, ?)", []any{id, r.Intn(1000), fmt.Sprintf("写入-%d", id), u}
					case 1:
						q, args = "UPDATE %s SET a = a + 1, b = ? WHERE id = ?", []any{fmt.Sprintf("改-%d", i), 1 + r.Intn(n)}
					default:
						q, args = "DELETE FROM %s WHERE id = ?", []any{1 + r.Intn(n)}
					}
					for _, tb := range []string{"t", "ref"} {
						if _, err := tx.ExecContext(ctx, fmt.Sprintf(q, tb), args...); err != nil {
							tx.Rollback()
							return err
						}
					}
					if err := tx.Commit(); err != nil {
						return err
					}
					time.Sleep(time.Millisecond)
				}
			}()
		}(w)
	}
	return func() error {
		close(done)
		var first error
		for w := 0; w < 2; w++ {
			if err := <-errs; err != nil && first == nil {
				first = err
			}
		}
		return first
	}
}

// dumpTable 把整张表按 id 排序读成一行一串(每列十六进制),ignore 里的列跳过。
func dumpTable(t *testing.T, db *sql.DB, table string, ignore ...string) []string {
	t.Helper()
	rows, err := db.Query("SELECT * FROM " + table + " ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	skip := map[int]bool{}
	for i, c := range cols {
		for _, ig := range ignore {
			if strings.EqualFold(c, ig) {
				skip[i] = true
			}
		}
	}
	var out []string
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var parts []string
		for i, v := range vals {
			switch {
			case skip[i]:
			case v == nil:
				parts = append(parts, "NULL")
			default:
				parts = append(parts, fmt.Sprintf("%x", []byte(v)))
			}
		}
		out = append(out, strings.Join(parts, "|"))
	}
	return out
}

func alterChange(t *testing.T, spec string) Change {
	t.Helper()
	p, ok := ParseDDL("ALTER TABLE t " + spec)
	if !ok {
		t.Fatalf("%q 认不出", spec)
	}
	p.Resolve(8, 0, 0) // 按最老的 8.0 归类:加 / 删列也走在线变更,便于测
	if !p.Supported {
		t.Fatalf("%q 应走在线变更: %+v", spec, p)
	}
	return p.Change
}

func TestRealBinlog_AlterMatchesNativeDDL(t *testing.T) {
	cases := []struct {
		name, spec string
		ignore     []string // 原生与在线变更取值本就不同的列(自增新列按拷贝顺序编号)
	}{
		{"加唯一索引", "ADD UNIQUE INDEX uq_u (u)", nil},
		{"改列类型 INT→BIGINT", "MODIFY a BIGINT NOT NULL", nil},
		{"加长 VARCHAR 跨过 255 字节", "MODIFY b VARCHAR(300) CHARACTER SET utf8mb3 NULL", nil},
		{"改列名与类型", "CHANGE e remark MEDIUMTEXT NULL", nil},
		{"转字符集", "CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", nil},
		{"换主键(保留同列唯一键)", "DROP PRIMARY KEY, ADD PRIMARY KEY (id, a), ADD UNIQUE KEY uk_id (id)", nil},
		{"加自增列", "ADD COLUMN seq BIGINT NOT NULL AUTO_INCREMENT, ADD UNIQUE KEY uk_seq (seq)", []string{"seq"}},
		{"加存储生成列", "ADD COLUMN dbl INT AS (a * 2) STORED", nil},
		{"加全文索引", "ADD FULLTEXT INDEX ft_e (e)", nil},
		{"重建表", "ENGINE=InnoDB", nil},
		{"改 ROW_FORMAT", "ROW_FORMAT=COMPACT", nil},
		{"加 / 删列并建索引", "ADD COLUMN x INT NOT NULL DEFAULT 5, ADD INDEX ix (x, a), DROP COLUMN c", nil},
		{"改 NULL 为 NOT NULL", "MODIFY b VARCHAR(64) CHARACTER SET utf8mb3 NOT NULL DEFAULT ''", nil}, // b 种子与写入都非空
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, schema := mysqlDB(t)
			db.SetMaxOpenConns(16)
			ctx := context.Background()
			const n = 3000
			seedAlterTables(t, db, n, "t", "ref")
			stop := alterWriters(ctx, db, n, false)
			m := New(db, realBinlog(t, db, schema, "t"), schema, "t", alterChange(t, c.spec),
				Config{ChunkSize: 200, CutOverRetryWait: 100 * time.Millisecond})
			err := m.Run(ctx)
			if werr := stop(); werr != nil {
				t.Fatalf("写入方出错: %v", werr)
			}
			if err != nil {
				t.Fatalf("在线变更失败: %v\n状态: %+v", err, m.Status())
			}
			mustExec(t, db, "ALTER TABLE ref "+c.spec)
			ts, _ := ghostShape(ctx, db, schema, "t")
			rs, _ := ghostShape(ctx, db, schema, "ref")
			if ts != rs {
				t.Errorf("结构与原生 ALTER 不同:\n在线变更:\n%s\n原生:\n%s", ts, rs)
			}
			got, want := dumpTable(t, db, "t", c.ignore...), dumpTable(t, db, "ref", c.ignore...)
			if len(got) != len(want) {
				explainRowDiff(t, db, m)
				t.Fatalf("行数不同:在线变更 %d,原生 %d", len(got), len(want))
			}
			bad := 0
			for i := range got {
				if got[i] != want[i] {
					if bad++; bad <= 3 {
						t.Errorf("第 %d 行不同:\n在线变更 %s\n原生     %s", i, got[i], want[i])
					}
				}
			}
			if bad > 0 {
				t.Fatalf("共 %d 行不同", bad)
			}
			st := m.Status()
			t.Logf("拷贝 %d 行、重放 %d 行、切换 %d 次", st.RowsCopied, st.RowsReplayed, st.CutOverAttempts)
		})
	}
}

// 做不到就要失败,而且原表不动、影子表清掉 —— 不能"做完了但少了几行 / 值变了"。
func TestRealBinlog_AlterFailsLoudlyInsteadOfLosingData(t *testing.T) {
	type outcome struct {
		code string // 期望的 BlockedError 代码;空 = 期望运行中失败
		msg  string // 期望错误里含的话
	}
	cases := []struct {
		name, spec string
		prep       string // 迁移前对 t 做的手脚
		dupWrites  bool   // 迁移期间写入制造重复
		want       outcome
	}{
		{"已有重复值加唯一", "ADD UNIQUE INDEX uq_a (a)", "", false, outcome{code: "unique_duplicates"}},
		{"迁移中途写出重复", "ADD UNIQUE INDEX uq_u (u)", "", true, outcome{msg: "唯一键"}},
		{"类型收窄越界", "MODIFY id BIGINT NOT NULL, MODIFY c DECIMAL(4,2) NOT NULL DEFAULT 0", "", false, outcome{msg: "警告"}},
		{"NOT NULL 遇到 NULL", "MODIFY u INT NOT NULL DEFAULT 0", "", false, outcome{msg: "警告"}},
		{"删了定位键", "DROP PRIMARY KEY", "", false, outcome{code: "no_shared_key"}},
		{"新加非空列没默认值", "ADD COLUMN z INT NOT NULL", "", false, outcome{code: "new_column_no_default"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, schema := mysqlDB(t)
			db.SetMaxOpenConns(16)
			ctx := context.Background()
			const n = 3000
			seedAlterTables(t, db, n, "t", "ref")
			before := dumpTable(t, db, "t")
			m := New(db, realBinlog(t, db, schema, "t"), schema, "t", alterChange(t, c.spec),
				Config{ChunkSize: 50, CutOverRetryWait: 100 * time.Millisecond, CutOverRetries: 2, CatchUpTimeout: 5 * time.Second})
			var stop func() error
			done := make(chan error, 1)
			go func() { done <- m.Run(ctx) }()
			if c.dupWrites { // 查重过了、开始拷贝之后才写出重复:要由拷贝 / 重放当场发现
				for m.Status().Phase != PhaseCopying && len(done) == 0 {
					time.Sleep(5 * time.Millisecond)
				}
				stop = alterWriters(ctx, db, n, true)
			}
			err := <-done
			if stop != nil {
				_ = stop() // 制造重复的写入方自己也会撞上 ref 的数据之外的约束,不管它
			}
			if err == nil {
				t.Fatal("应当失败")
			}
			var be *BlockedError
			switch {
			case c.want.code != "":
				if !errors.As(err, &be) || be.Blockers[0].Code != c.want.code {
					t.Fatalf("应被前置检查以 %s 拦下: %v", c.want.code, err)
				}
			case !strings.Contains(err.Error(), c.want.msg):
				t.Fatalf("错误里应提到「%s」: %v", c.want.msg, err)
			}
			var gho int
			db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = '_t_gho'", schema).Scan(&gho)
			if gho != 0 {
				t.Error("失败后影子表应已清理")
			}
			if !c.dupWrites {
				if after := dumpTable(t, db, "t"); strings.Join(after, "\n") != strings.Join(before, "\n") {
					t.Error("失败后原表不应有任何变化")
				}
			}
			t.Logf("如期失败: %v", err)
		})
	}
}

// 生成工单前的探测:在空表上真跑一遍,语句错、删了定位键、新非空列没默认值,都要提前说出来。
func TestMySQL_ProbeAlterCatchesProblemsBeforeTheTicket(t *testing.T) {
	db, schema := mysqlDB(t)
	ctx := context.Background()
	seedAlterTables(t, db, 10, "t")
	cases := []struct{ spec, code string }{
		{"MODIFY nosuch INT", "alter_failed"},
		{"DROP PRIMARY KEY", "no_shared_key"},
		{"ADD COLUMN z INT NOT NULL", "new_column_no_default"},
		{"MODIFY a BIGINT NOT NULL", ""},
	}
	for _, c := range cases {
		bs, err := ProbeAlter(ctx, db, schema, "t", alterChange(t, c.spec))
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		got := ""
		if len(bs) > 0 {
			got = bs[0].Code
		}
		if got != c.code {
			t.Errorf("%q: 应为 %q,实际 %q %+v", c.spec, c.code, got, bs)
		}
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME LIKE ?", schema, probePrefix+"%").Scan(&n)
	if n != 0 {
		t.Error("探测表用完应删掉")
	}

	// L-OSC3(2026-10 安全审计):从前探测表是固定名 _<表>_ocp,建之前先 DROP IF EXISTS ——
	// 库里恰好有一张同名的业务表就被删了。现在用随机名,同名的表不受影响。
	if _, err := db.Exec("CREATE TABLE _t_ocp (keep_me INT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeAlter(ctx, db, schema, "t", alterChange(t, "ADD COLUMN y INT")); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = '_t_ocp'", schema).Scan(&n)
	if n != 1 {
		t.Error("与旧探测表同名的业务表不应被删")
	}
}

var _ = sync.Mutex{}

// explainRowDiff 在行数对不上时说清楚差在哪:哪些 id 只在一边,它们在切换下来的旧表 _t_del 里
// 在不在、长什么样 —— 在旧表里 = 切换前就在原表,却没被拷贝 / 重放进影子表;不在 = 切换之后写的。
func explainRowDiff(t *testing.T, db *sql.DB, m *Migration) {
	t.Helper()
	ids := func(q string) map[int64]bool {
		out := map[int64]bool{}
		rows, err := db.Query(q)
		if err != nil {
			t.Logf("诊断查询失败: %v", err)
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			out[id] = true
		}
		return out
	}
	tIDs, refIDs, delIDs := ids("SELECT id FROM t"), ids("SELECT id FROM ref"), ids("SELECT id FROM _t_del")
	for id := range refIDs {
		if !tIDs[id] {
			t.Logf("缺行 id=%d:ref 有、在线变更结果没有;切换下来的旧表里 %v", id, delIDs[id])
		}
	}
	for id := range tIDs {
		if !refIDs[id] {
			t.Logf("多行 id=%d:在线变更结果有、ref 没有;旧表里 %v", id, delIDs[id])
		}
	}
	t.Logf("状态: %+v", m.Status())
}
