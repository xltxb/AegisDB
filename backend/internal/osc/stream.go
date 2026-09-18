package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
)

// Position 是 binlog 里的一个点:从这里开始往后订阅。
type Position struct {
	File string
	Pos  uint32
}

// CurrentPosition 读实例此刻的 binlog 位点。
//
// 迁移开始时要先记下它,再开始拷贝 —— 顺序反了会漏事件:先拷贝后记位点的话,
// 拷贝期间发生的写入既不在存量里(拷的是更早的快照),也不在订阅里(位点在它们之后)。
func CurrentPosition(ctx context.Context, db *sql.DB) (Position, error) {
	// MySQL 8.4 起 SHOW MASTER STATUS 被 SHOW BINARY LOG STATUS 取代,老版本只认前者。
	for _, q := range []string{"SHOW BINARY LOG STATUS", "SHOW MASTER STATUS"} {
		p, err := scanPosition(ctx, db, q)
		if err == nil {
			return p, nil
		}
	}
	return Position{}, fmt.Errorf("读 binlog 位点失败:两种语法都不被接受(实例可能没开 binlog)")
}

func scanPosition(ctx context.Context, db *sql.DB, query string) (Position, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return Position{}, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return Position{}, err
	}
	if !rows.Next() {
		return Position{}, fmt.Errorf("没有返回位点行")
	}
	// 各版本的列数不同(有无 Executed_Gtid_Set 等),按列名取前两列,别按位置数。
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return Position{}, err
	}
	var p Position
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "file":
			p.File = asString(vals[i])
		case "position":
			n, _ := strconv.ParseUint(asString(vals[i]), 10, 32)
			p.Pos = uint32(n)
		}
	}
	if p.File == "" {
		return Position{}, fmt.Errorf("位点里没有 File 列")
	}
	return p, nil
}

func asString(v any) string {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// StreamConfig 订阅一张表的行事件所需的一切。
type StreamConfig struct {
	DSN string
	// ServerID 必须在这个复制拓扑里唯一 —— 撞号会把另一个订阅者踢下线,
	// 包括真正的从库。每次迁移用一个自己的号。
	ServerID uint32
	Schema   string
	Table    string // 被观察的**原表**,不是影子表
	Position Position
}

// Stream 从给定位点订阅,把目标表的行事件交给 onEvent,直到 ctx 结束。
//
// 只交目标表的事件。别的表混进来就会被重放写进影子表 —— 那是把无关的数据搬进
// 一张即将顶替生产表的表里,而且不会有任何报错。
//
// 列名不在行事件里:binlog 的 ROWS_EVENT 只带值,表结构来自它前面的 TABLE_MAP_EVENT,
// 而那里也只有列的类型不带名字。所以名字从 information_schema 查一次,按序号对上 ——
// 这也是为什么迁移期间不能有人去改原表结构。
func Stream(ctx context.Context, cfg StreamConfig, onEvent func(RowEvent) error) error {
	host, port, user, pass, err := parseDSN(cfg.DSN)
	if err != nil {
		return err
	}

	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return fmt.Errorf("打开连接以读取表结构: %w", err)
	}
	defer db.Close()

	cols, err := columnNames(ctx, db, cfg.Schema, cfg.Table)
	if err != nil {
		return err
	}
	keys, err := keyColumns(ctx, db, cfg.Schema, cfg.Table)
	if err != nil {
		return err
	}

	syncer := replication.NewBinlogSyncer(replication.BinlogSyncerConfig{
		ServerID: cfg.ServerID,
		Flavor:   "mysql",
		Host:     host,
		Port:     port,
		User:     user,
		Password: pass,
	})
	defer syncer.Close()

	streamer, err := syncer.StartSync(mysql.Position{Name: cfg.Position.File, Pos: cfg.Position.Pos})
	if err != nil {
		return fmt.Errorf("开始订阅 binlog: %w", err)
	}

	for {
		ev, err := streamer.GetEvent(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // 调用方叫停,不是故障
			}
			return fmt.Errorf("读 binlog 事件: %w", err)
		}
		re, ok := ev.Event.(*replication.RowsEvent)
		if !ok {
			continue
		}
		// 按大小写不敏感比对。`lower_case_table_names` 决定了实例怎么对待标识符:
		// macOS 默认 2(存原样、比较时不分大小写),Windows 是 1(存小写),Linux 常见
		// 0(敏感)。binlog 里给的名字随之不同 —— 用敏感比较就会在前两种实例上一条
		// 事件都收不到,而订阅本身没有任何报错,看起来只是"没有写入"。
		if !strings.EqualFold(string(re.Table.Schema), cfg.Schema) ||
			!strings.EqualFold(string(re.Table.Table), cfg.Table) {
			continue // 别的表,丢掉
		}
		kind := kindOf(ev.Header.EventType)
		if kind == "" {
			continue
		}
		for _, one := range splitRows(kind, re.Rows) {
			out := RowEvent{
				Kind: kind, Schema: cfg.Schema, Table: cfg.Table,
				Columns: cols, KeyColumns: keys,
				Before: one.before, After: one.after,
			}
			if err := onEvent(out); err != nil {
				return err
			}
		}
	}
}

func kindOf(t replication.EventType) RowEventKind {
	switch t {
	case replication.WRITE_ROWS_EVENTv0, replication.WRITE_ROWS_EVENTv1, replication.WRITE_ROWS_EVENTv2:
		return RowInsert
	case replication.UPDATE_ROWS_EVENTv0, replication.UPDATE_ROWS_EVENTv1, replication.UPDATE_ROWS_EVENTv2:
		return RowUpdate
	case replication.DELETE_ROWS_EVENTv0, replication.DELETE_ROWS_EVENTv1, replication.DELETE_ROWS_EVENTv2:
		return RowDelete
	}
	return ""
}

type rowPair struct{ before, after []any }

// UPDATE 的 Rows 是成对的:偶数下标是改前,奇数下标是改后。INSERT / DELETE 一行一条。
// 把这个约定收在一处,别让调用方去记它。
func splitRows(kind RowEventKind, rows [][]any) []rowPair {
	var out []rowPair
	switch kind {
	case RowUpdate:
		for i := 0; i+1 < len(rows); i += 2 {
			out = append(out, rowPair{before: rows[i], after: rows[i+1]})
		}
	case RowInsert:
		for _, r := range rows {
			out = append(out, rowPair{after: r})
		}
	case RowDelete:
		for _, r := range rows {
			out = append(out, rowPair{before: r})
		}
	}
	return out
}

// keyColumns 是用来定位一行的键 —— 主键优先,否则第一个单列唯一非空索引。
// 与 chunkKey 挑的是同一套依据,两处不一致会让重放删不掉拷贝刚写进去的行。
func keyColumns(ctx context.Context, db *sql.DB, schema, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT COLUMN_NAME FROM information_schema.STATISTICS
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME = 'PRIMARY'
		 ORDER BY SEQ_IN_INDEX`, schema, table)
	if err != nil {
		return nil, fmt.Errorf("读主键列: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}
	// 没有主键就退回分块用的那个键 —— Preflight 保证了两者至少有一个存在。
	k, err := chunkKey(ctx, db, schema, table)
	if err != nil {
		return nil, err
	}
	return []string{k}, nil
}

// parseDSN 从 go-sql-driver 的 DSN 里取出 binlog 订阅要的四样。
// 订阅走的是复制协议、不是 database/sql,所以这些必须拆出来单独给。
func parseDSN(dsn string) (host string, port uint16, user, pass string, err error) {
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return "", 0, "", "", fmt.Errorf("DSN 里没有 @:%q", dsn)
	}
	cred := dsn[:at]
	rest := dsn[at+1:]

	user = cred
	if i := strings.Index(cred, ":"); i >= 0 {
		user, pass = cred[:i], cred[i+1:]
	}

	l, r := strings.Index(rest, "("), strings.Index(rest, ")")
	if l < 0 || r < l {
		return "", 0, "", "", fmt.Errorf("DSN 里没有 tcp(host:port):%q", dsn)
	}
	addr := rest[l+1 : r]
	host = addr
	port = 3306
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
		n, convErr := strconv.ParseUint(addr[i+1:], 10, 16)
		if convErr != nil {
			return "", 0, "", "", fmt.Errorf("DSN 里的端口不是数字:%q", addr)
		}
		port = uint16(n)
	}
	return host, port, user, pass, nil
}
