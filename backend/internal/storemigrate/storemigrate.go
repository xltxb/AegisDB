// Package storemigrate 把网关自己的存储库(账号、工单、审计、元数据……)从 MySQL 整体搬到
// PostgreSQL。它是一次**停机**搬迁:调用前网关必须已经停掉,搬迁期间源库不能有写入。
//
// 步骤:
//
//  1. 预检 —— 源库的迁移账本必须与本版本内置的 MySQL 迁移完全一致(不多不少);目标 PG
//     版本 >= 13 且库是空的(--reset 时只清空上次没搬完留下的存储库表);源审计链先验一遍。
//  2. 建表 —— 只跑 migrations/pg 的建表迁移,不跑参考数据回填:源库里已经有了。
//  3. 拷贝 —— 每张表一条 SELECT 流式读出、PG COPY 写入,原样带 id;多张表并发。
//  4. 收尾 —— 自增序列对齐到 max(id),ANALYZE。
//  5. 核对 —— 每张表行数相等、内容摘要相等(逐列逐值,和行序无关);审计链在 PG 上重算,
//     结论必须与源库一致;源库各表行数与开始拷贝时相同(期间没有写入)。
//
// 时间:MySQL 的 DATETIME 不带时区,读出来按进程的 time.Local 解释成时刻,写进 PG 的
// timestamptz。所以本程序的 time.Local 必须与写下这些数据的网关一致(命令行 --tz)——
// 审计哈希里的时间带着时区偏移,时区一错,第 1 步对源库的链校验就过不去,会在动手前就停下。
package storemigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"aegisdb/internal/repository"
	"aegisdb/internal/service"
	"aegisdb/migrations"
)

// Options 是一次搬迁的参数。
type Options struct {
	MySQLDSN    string // 源:网关当前在用的 MySQL 存储库 DSN(go-sql-driver 格式)
	PostgresDSN string // 目标:新建好的空 PG 库(libpq 键值串或 URL)
	Workers     int    // 并发拷贝的表数,<=0 取 4
	Reset       bool   // 目标里已有上次没搬完的存储库表时,先清空它们
	Estimate    bool   // 只预检、数行数,不写目标
	// AcceptBrokenChain 源库审计链本身验不过时仍然搬。默认拒绝:时区(--tz)填错时,内容摘要
	// 两边照样相等(都按同一个错的时区换算),能拦住它的只有审计链。
	AcceptBrokenChain bool
	// ApplySchema 建表用的函数(sqlmigrate.Run);由调用方注入,避免本包依赖 bootstrap。
	ApplySchema func(db *gorm.DB, src fs.FS) error
	Log         func(format string, args ...any)
}

// TableResult 是一张表的结果。
type TableResult struct {
	Name       string
	SourceRows int64
	TargetRows int64
	SourceSum  string
	TargetSum  string
	Took       time.Duration
}

// Report 是整次搬迁的结果。
type Report struct {
	Tables      []TableResult
	SourceChain *service.AuditChainReport
	TargetChain *service.AuditChainReport
	Took        time.Duration
}

// Run 执行搬迁。返回 error 即未完成(目标库可能留下部分数据,修好原因后用 Reset 重跑)。
func Run(ctx context.Context, o Options) (*Report, error) {
	start := time.Now()
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.ApplySchema == nil && !o.Estimate {
		return nil, errors.New("ApplySchema 未设置")
	}

	// ---- 源 ----
	myCfg, err := mysqldrv.ParseDSN(o.MySQLDSN)
	if err != nil {
		return nil, fmt.Errorf("源 MySQL DSN 解析失败: %w", err)
	}
	myCfg.ParseTime = true
	myCfg.Loc = time.Local // 与网关读这些时间时的口径一致,见包注释
	if myCfg.Params == nil {
		myCfg.Params = map[string]string{}
	}
	myCfg.Params["charset"] = "utf8mb4"
	src, err := sql.Open("mysql", myCfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("连不上源 MySQL: %w", err)
	}
	if moved, err := markerExists(ctx, src); err != nil {
		return nil, err
	} else if moved && !o.Estimate {
		return nil, fmt.Errorf("源库里已有搬迁标记表 %s:这个库已经搬过一次。确认要重搬(例如回退后再来一次)时,先删掉这张表", MovedMarkerTable)
	}
	if err := checkSourceLedger(ctx, src); err != nil {
		return nil, err
	}
	srcTables, err := mysqlTables(ctx, src)
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	var total int64
	for _, t := range srcTables {
		n, err := countRows(ctx, src, "SELECT COUNT(*) FROM `"+t+"`")
		if err != nil {
			return nil, err
		}
		counts[t] = n
		total += n
	}
	rep := &Report{}
	o.Log("源库 %d 张表,共 %d 行(时区 %s)", len(srcTables), total, time.Local)

	srcGorm, err := gorm.Open(mysql.New(mysql.Config{Conn: src}), quietGorm())
	if err != nil {
		return nil, err
	}
	o.Log("校验源库审计链……")
	if rep.SourceChain, err = service.NewForOps(repository.New(srcGorm)).VerifyAuditChain(0); err != nil {
		return nil, fmt.Errorf("源审计链校验出错: %w", err)
	}
	o.Log("  %s", rep.SourceChain.String())
	if o.Estimate {
		for _, t := range srcTables {
			rep.Tables = append(rep.Tables, TableResult{Name: t, SourceRows: counts[t]})
		}
		rep.Took = time.Since(start)
		return rep, nil
	}
	if !rep.SourceChain.OK && !o.AcceptBrokenChain {
		return rep, fmt.Errorf("源库审计链在本程序里验不过(%s)。\n"+
			"如果网关界面上的链校验是通过的,几乎可以肯定是 --tz 与网关所在时区不一致 —— 时区错了搬过去所有时间都会整体偏移。\n"+
			"如果界面上本来就验不过、且已知原因,加 --accept-broken-chain 照搬(核对时要求两边断在同一行)", rep.SourceChain)
	}

	// ---- 目标 ----
	pgVer, err := targetCheck(ctx, o.PostgresDSN, o.Reset)
	if err != nil {
		return nil, err
	}
	o.Log("目标 PostgreSQL %s", pgVer)
	dst, err := gorm.Open(postgres.Open(o.PostgresDSN), quietGorm())
	if err != nil {
		return nil, fmt.Errorf("连不上目标 PG: %w", err)
	}
	if s, e := dst.DB(); e == nil {
		defer s.Close()
	}
	if err := o.ApplySchema(dst, migrations.PGFS); err != nil {
		return nil, fmt.Errorf("目标建表失败: %w", err)
	}
	pool, err := pgPool(ctx, o.PostgresDSN)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	cols, err := pgColumns(ctx, pool)
	if err != nil {
		return nil, err
	}
	if err := sameTableSet(srcTables, cols); err != nil {
		return nil, err
	}
	if o.Reset {
		if err := truncateStore(ctx, pool, srcTables); err != nil {
			return nil, err
		}
	} else if err := mustBeEmpty(ctx, pool, srcTables); err != nil {
		return nil, err
	}

	// ---- 拷贝 ----
	o.Log("开始拷贝(%d 路并发)……", o.Workers)
	// 大表先开工,整体耗时取决于最大的那张。
	order := append([]string(nil), srcTables...)
	sort.Slice(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	results := make(map[string]*TableResult, len(order))
	var mu sync.Mutex
	var firstErr error
	jobs := make(chan string)
	var wg sync.WaitGroup
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for w := 0; w < o.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				r, err := copyTable(cctx, src, pool, t, cols[t])
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = fmt.Errorf("拷贝 %s 失败: %w", t, err)
					cancel()
				}
				if err == nil {
					results[t] = r
					o.Log("  %-32s %10d 行  %s", t, r.SourceRows, r.Took.Round(time.Millisecond))
				}
				mu.Unlock()
			}
		}()
	}
	for _, t := range order {
		if cctx.Err() != nil {
			break
		}
		jobs <- t
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	// ---- 收尾 ----
	if err := resetIdentities(ctx, pool, cols); err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, "ANALYZE"); err != nil {
		return nil, fmt.Errorf("ANALYZE: %w", err)
	}

	// ---- 核对 ----
	o.Log("核对……")
	var bad []string
	for _, t := range srcTables {
		r := results[t]
		r.TargetRows, r.TargetSum, err = pgDigest(ctx, pool, t, cols[t])
		if err != nil {
			return nil, err
		}
		now, err := countRows(ctx, src, "SELECT COUNT(*) FROM `"+t+"`")
		if err != nil {
			return nil, err
		}
		switch {
		case now != counts[t]:
			bad = append(bad, fmt.Sprintf("%s:源库行数在搬迁期间变了(%d → %d),网关没停干净", t, counts[t], now))
		case r.SourceRows != r.TargetRows:
			bad = append(bad, fmt.Sprintf("%s:行数不一致 源 %d / 目标 %d", t, r.SourceRows, r.TargetRows))
		case r.SourceSum != r.TargetSum:
			bad = append(bad, fmt.Sprintf("%s:内容摘要不一致", t))
		}
		rep.Tables = append(rep.Tables, *r)
	}
	if rep.TargetChain, err = service.NewForOps(repository.New(dst)).VerifyAuditChain(0); err != nil {
		return nil, fmt.Errorf("目标审计链校验出错: %w", err)
	}
	if !sameChainVerdict(rep.SourceChain, rep.TargetChain) {
		bad = append(bad, fmt.Sprintf("审计链结论不一致:源「%s」/ 目标「%s」", rep.SourceChain, rep.TargetChain))
	}
	rep.Took = time.Since(start)
	if len(bad) == 0 {
		if err := writeMarker(ctx, src, o.PostgresDSN); err != nil {
			return rep, err
		}
	}
	if len(bad) > 0 {
		return rep, fmt.Errorf("核对没通过,不要切换:\n  %s", strings.Join(bad, "\n  "))
	}
	return rep, nil
}

func quietGorm() *gorm.Config {
	return &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)}
}

func sameChainVerdict(a, b *service.AuditChainReport) bool {
	return a.OK == b.OK && a.Checked == b.Checked && a.BadID == b.BadID && a.BadReason == b.BadReason &&
		a.RolloverRows == b.RolloverRows
}

// checkSourceLedger:源库必须恰好跑完本版本内置的全部 MySQL 迁移。少了说明源库比本版本
// 旧(先在源库上跑 migrate);多了说明源库比本版本新(换对应版本的程序来搬)。
func checkSourceLedger(ctx context.Context, src *sql.DB) error {
	want, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	rows, err := src.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("读源库迁移账本: %w", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return err
		}
		have[v] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var missing []string
	for _, v := range want {
		if !have[v] {
			missing = append(missing, v)
		}
		delete(have, v)
	}
	if len(missing) > 0 {
		return fmt.Errorf("源库还缺 %d 个迁移(如 %s):先用当前版本在源库上执行 migrate,再搬", len(missing), missing[0])
	}
	if len(have) > 0 {
		var extra []string
		for v := range have {
			extra = append(extra, v)
		}
		sort.Strings(extra)
		return fmt.Errorf("源库有本版本不认识的迁移(如 %s):源库比这个程序新,请用对应版本的程序搬", extra[0])
	}
	return nil
}

func mysqlTables(ctx context.Context, src *sql.DB) ([]string, error) {
	rows, err := src.QueryContext(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' AND table_name NOT IN ('schema_migrations', ?) ORDER BY table_name`, MovedMarkerTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func countRows(ctx context.Context, db *sql.DB, q string) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, q).Scan(&n)
	return n, err
}

// targetCheck 看目标 PG 版本,以及是否是一个能用的库。
func targetCheck(ctx context.Context, dsn string, reset bool) (string, error) {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return "", fmt.Errorf("连不上目标 PG: %w", err)
	}
	defer c.Close(ctx)
	var num int
	var ver string
	if err := c.QueryRow(ctx, "SELECT current_setting('server_version_num')::int, current_setting('server_version')").Scan(&num, &ver); err != nil {
		return "", err
	}
	if num < 130000 {
		return "", fmt.Errorf("目标 PG 版本 %s 太旧:需要 13 及以上(citext 须为受信扩展)", ver)
	}
	return ver, nil
}

type pgCol struct {
	Name, Type string // Type: bigint | integer | boolean | citext | timestamptz
	Identity   bool
}

func pgColumns(ctx context.Context, pool *pgxpool.Pool) (map[string][]pgCol, error) {
	rows, err := pool.Query(ctx, `SELECT c.relname, a.attname, t.typname, a.attidentity <> ''
		FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_type t ON t.oid = a.atttypid
		WHERE n.nspname = current_schema() AND c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped
			AND c.relname <> 'schema_migrations'
		ORDER BY c.relname, a.attnum`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]pgCol{}
	for rows.Next() {
		var tb string
		var c pgCol
		if err := rows.Scan(&tb, &c.Name, &c.Type, &c.Identity); err != nil {
			return nil, err
		}
		switch c.Type {
		case "int8":
			c.Type = "bigint"
		case "int4":
			c.Type = "integer"
		case "bool":
			c.Type = "boolean"
		case "citext", "timestamptz":
		default:
			return nil, fmt.Errorf("目标 %s.%s 的类型 %s 本程序不认识", tb, c.Name, c.Type)
		}
		out[tb] = append(out[tb], c)
	}
	return out, rows.Err()
}

func sameTableSet(src []string, dst map[string][]pgCol) error {
	var onlySrc, onlyDst []string
	seen := map[string]bool{}
	for _, t := range src {
		seen[t] = true
		if dst[t] == nil {
			onlySrc = append(onlySrc, t)
		}
	}
	for t := range dst {
		if !seen[t] {
			onlyDst = append(onlyDst, t)
		}
	}
	if len(onlySrc)+len(onlyDst) > 0 {
		sort.Strings(onlyDst)
		return fmt.Errorf("两边的表对不上:源库独有 %v,目标独有 %v", onlySrc, onlyDst)
	}
	return nil
}

func mustBeEmpty(ctx context.Context, pool *pgxpool.Pool, tables []string) error {
	for _, t := range tables {
		var any bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+pgx.Identifier{t}.Sanitize()+")").Scan(&any); err != nil {
			return err
		}
		if any {
			return fmt.Errorf("目标库的 %s 里已经有数据:像是上次没搬完。确认目标就是本次要用的新库后,加 --reset 重跑", t)
		}
	}
	return nil
}

func truncateStore(ctx context.Context, pool *pgxpool.Pool, tables []string) error {
	ids := make([]string, len(tables))
	for i, t := range tables {
		ids[i] = pgx.Identifier{t}.Sanitize()
	}
	_, err := pool.Exec(ctx, "TRUNCATE "+strings.Join(ids, ", "))
	return err
}

// pgPool 连目标库,并把 citext 注册成文本编解码(它是扩展类型,pgx 默认不认识它的 OID)。
func pgPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("目标 PG DSN 解析失败: %w", err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		var oid uint32
		if err := c.QueryRow(ctx, "SELECT 'citext'::regtype::oid").Scan(&oid); err != nil {
			return err
		}
		c.TypeMap().RegisterType(&pgtype.Type{Name: "citext", OID: oid, Codec: pgtype.TextCodec{}})
		return nil
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// rowSource 把 MySQL 的结果集接成 pgx 的 CopyFromSource,并在同一趟里算源端摘要。
type rowSource struct {
	table string
	rows  *sql.Rows
	cols  []pgCol
	dest  []any
	vals  []any
	dig   *digest
	err   error
}

func newScanDest(cols []pgCol) []any {
	d := make([]any, len(cols))
	for i, c := range cols {
		switch c.Type {
		case "bigint", "integer", "boolean":
			d[i] = new(sql.NullInt64)
		case "citext":
			d[i] = new(sql.NullString)
		case "timestamptz":
			d[i] = new(sql.NullTime)
		}
	}
	return d
}

// value 把扫描结果换成写入 PG 的值(nil 表示 NULL)。两端摘要都从这一步之后的值算。
func value(c pgCol, d any) any {
	switch v := d.(type) {
	case *sql.NullInt64:
		if !v.Valid {
			return nil
		}
		if c.Type == "boolean" {
			return v.Int64 != 0
		}
		return v.Int64
	case *sql.NullString:
		if !v.Valid {
			return nil
		}
		return v.String
	case *sql.NullTime:
		if !v.Valid {
			return nil
		}
		return v.Time
	case *sql.NullBool:
		if !v.Valid {
			return nil
		}
		return v.Bool
	}
	return nil
}

func (s *rowSource) Next() bool {
	if !s.rows.Next() {
		return false
	}
	if err := s.rows.Scan(s.dest...); err != nil {
		s.err = err
		return false
	}
	for i, c := range s.cols {
		v := value(c, s.dest[i])
		if str, ok := v.(string); ok && strings.IndexByte(str, 0) >= 0 {
			// PG 的文本里放不下 NUL 字节,MySQL 里可以。改写内容会让审计哈希对不上,所以停下来让人处理。
			first := any("?")
			if i > 0 {
				first = s.vals[0] // 第一列,各表都是主键(或其首列)
			}
			s.err = fmt.Errorf("%s.%s 有一行含 NUL 字节(%s=%v),PG 的文本存不下;请先在源库上处理这一行", s.table, c.Name, s.cols[0].Name, first)
			return false
		}
		s.vals[i] = v
	}
	s.dig.add(s.vals)
	return true
}

func (s *rowSource) Values() ([]any, error) { return s.vals, nil }

func (s *rowSource) Err() error {
	if s.err != nil {
		return s.err
	}
	return s.rows.Err()
}

func copyTable(ctx context.Context, src *sql.DB, pool *pgxpool.Pool, table string, cols []pgCol) (*TableResult, error) {
	start := time.Now()
	names := make([]string, len(cols))
	sel := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
		sel[i] = "`" + c.Name + "`"
	}
	rows, err := src.QueryContext(ctx, "SELECT "+strings.Join(sel, ", ")+" FROM `"+table+"`")
	if err != nil {
		return nil, fmt.Errorf("读源表: %w", err)
	}
	defer rows.Close()
	rs := &rowSource{table: table, rows: rows, cols: cols, dest: newScanDest(cols), vals: make([]any, len(cols)), dig: newDigest(cols)}
	n, err := pool.CopyFrom(ctx, pgx.Identifier{table}, names, rs)
	if err != nil {
		if rs.err != nil {
			return nil, rs.err
		}
		return nil, err
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	return &TableResult{Name: table, SourceRows: rs.dig.n, TargetRows: n, SourceSum: rs.dig.sum(), Took: time.Since(start)}, nil
}

// pgDigest 把目标表整张读回来算摘要。
func pgDigest(ctx context.Context, pool *pgxpool.Pool, table string, cols []pgCol) (int64, string, error) {
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = pgx.Identifier{c.Name}.Sanitize()
		if c.Type == "citext" {
			sel[i] += "::text"
		}
	}
	rows, err := pool.Query(ctx, "SELECT "+strings.Join(sel, ", ")+" FROM "+pgx.Identifier{table}.Sanitize())
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	dig := newDigest(cols)
	vals := make([]any, len(cols))
	for rows.Next() {
		raw, err := rows.Values()
		if err != nil {
			return 0, "", err
		}
		for i, c := range cols {
			v := raw[i]
			if c.Type == "integer" {
				if x, ok := v.(int32); ok {
					v = int64(x)
				}
			}
			vals[i] = v
		}
		dig.add(vals)
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	return dig.n, dig.sum(), nil
}

// digest 是一张表与行序无关的内容摘要:每行(按列序、带类型标记地)取 SHA-256,再逐位异或。
// 主键保证行不重复,所以异或不会出现两行相同而互相抵消的情况。
type digest struct {
	cols []pgCol
	acc  [sha256.Size]byte
	n    int64
	buf  []byte
}

func newDigest(cols []pgCol) *digest { return &digest{cols: cols} }

func (d *digest) add(vals []any) {
	b := d.buf[:0]
	for i, v := range vals {
		switch x := v.(type) {
		case nil:
			b = append(b, 'N')
		case int64:
			b = append(b, 'I')
			b = binary.BigEndian.AppendUint64(b, uint64(x))
		case bool:
			if x {
				b = append(b, 'T')
			} else {
				b = append(b, 'F')
			}
		case string:
			b = append(b, 'S')
			b = binary.BigEndian.AppendUint64(b, uint64(len(x)))
			b = append(b, x...)
		case time.Time:
			b = append(b, 'D')
			b = binary.BigEndian.AppendUint64(b, uint64(x.UnixMicro()))
		default:
			b = append(b, 'X')
			b = append(b, fmt.Sprintf("%s=%T:%v", d.cols[i].Name, v, v)...)
		}
	}
	d.buf = b
	h := sha256.Sum256(b)
	for i := range d.acc {
		d.acc[i] ^= h[i]
	}
	d.n++
}

func (d *digest) sum() string { return fmt.Sprintf("%x", d.acc[:8]) }

// resetIdentities 把每个自增列的序列推到 max(id),否则切过去后第一次插入就撞主键。
func resetIdentities(ctx context.Context, pool *pgxpool.Pool, cols map[string][]pgCol) error {
	for t, cs := range cols {
		for _, c := range cs {
			if !c.Identity {
				continue
			}
			ti, ci := pgx.Identifier{t}.Sanitize(), pgx.Identifier{c.Name}.Sanitize()
			q := fmt.Sprintf("SELECT setval(pg_get_serial_sequence('%s', '%s'), COALESCE((SELECT max(%s) FROM %s), 1), (SELECT max(%s) FROM %s) IS NOT NULL)",
				strings.ReplaceAll(ti, "'", "''"), c.Name, ci, ti, ci, ti)
			if _, err := pool.Exec(ctx, q); err != nil {
				return fmt.Errorf("对齐 %s.%s 的序列: %w", t, c.Name, err)
			}
		}
	}
	return nil
}
