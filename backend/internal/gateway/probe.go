package gateway

// 收单检查的第二步:字段是否存在,以及**数据会不会违反即将加上的约束**。
//
// ## 为什么这里必须读数据
//
// "字段已存在"看结构就能答,"表里有 NULL 所以这条 SET NOT NULL 会失败"不能 —— 那是
// 数据的事实,元数据缓存里没有。所以这一层会对目标表跑只读探测。
//
// ## 护栏
//
// 它跑在生产库上,所以每一条都受约束:
//
//	只对有风险的形状跑   加非空列、改成非空、加唯一约束。其余一概不跑。
//	用 EXISTS 不用 COUNT  命中一行就停,不数完 —— 要回答的是"会不会",不是"有多少"。
//	10 秒超时            超时如实报"未能核对",不报通过。
//	标识符白名单         名字里有引号/空格/分号一律不探,而不是想办法转义。
//
// 最后一条是安全边界:表名列名来自用户提交的 SQL,拼进探测语句就是一条注入面。这里
// 不做"转义得足够好",而是**不合规就不探** —— 少一次检查是可以接受的,拼出一条能跑的
// 注入语句不是。

import (
	"context"
	"fmt"
	"strings"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
	"aegisdb/pkg/sqlutil"
)

// safeIdent 判断一个标识符能不能安全地拼进探测语句。
//
// 只允许字母、数字、下划线、$ 和句点(限定名)。中文表名会被挡在外面 —— 那是**有意**
// 放弃的一小部分覆盖率:这里换来的是"拼不出注入"这件事不依赖任何转义逻辑。
func safeIdent(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || c == '$' || c == '.' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// quoteIdent 按引擎给标识符加引号。限定名按点拆开,每段各自加引号。
//
// 调用前必须先过 safeIdent —— 这里不做任何转义,它假设名字里没有引号字符。
func quoteIdent(engine, name string) string {
	q, qq := `"`, `"`
	if engineFamily(engine) == familyMySQL {
		q, qq = "`", "`"
	}
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = q + p + qq
	}
	return strings.Join(parts, ".")
}

// ProbeSupported 说明这个引擎能不能跑数据探测。
//
// Mongo 不是 SQL;其余三个家族的探测语句是同一套标准 SQL。
func ProbeSupported(engine string) bool {
	switch engineFamily(engine) {
	case familyMySQL, familyPostgres, familyOracle:
		return true
	}
	return false
}

// ObjSourceLiveTag 是字段与数据结论的来源标签。
//
// 它是常量而不是参数:字段与探测**一律实时**,没有"缓存优先"那一档(字段比建表变得
// 频繁得多,过期缓存在这里更容易误报)。写死是为了让这件事在代码里也只有一种可能。
const ObjSourceLiveTag = "live"

// ErrProbeUnsafeName 说明名字里有不能安全拼进探测语句的字符,这次没探。
var ErrProbeUnsafeName = apimsg.Errorf("表名或列名含有无法安全拼接的字符,未做数据探测")

// LiveColumnsOf 实时取这几张表的列名。
//
// **不走缓存**:字段比建表变得频繁得多,一份昨天的缓存会把昨天刚加的列报成"不存在",
// 而那句话会让人去改一个本来正确的脚本。
//
// 返回的外层键与 AddTableKey 同一套约定(裸名 + 限定名,全小写),内层是列名集合
// (同样小写)。取不到的表不出现在结果里 —— 调用方据此说"没能核对",而不是说"没有列"。
func LiveColumnsOf(conn *model.Connection, tables []sqlutil.Ref) (map[string]map[string]bool, error) {
	if !RealExecSupported(conn) {
		return nil, apimsg.Errorf("实例未配置真实执行凭据,无法核对字段")
	}
	names := make([]string, 0, len(tables))
	seen := map[string]bool{}
	for _, t := range tables {
		n := strings.ToLower(strings.TrimSpace(t.Name))
		if n == "" || seen[n] || !safeIdent(t.Name) {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	if len(names) == 0 {
		return map[string]map[string]bool{}, nil
	}
	query, args := liveColumnQuery(conn, names)
	if query == "" {
		return nil, apimsg.Errorf("引擎 %q 暂不支持字段核对", conn.Engine)
	}
	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), precheckTimeout)
	defer cancel()

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var schema, table, col string
		if err := rows.Scan(&schema, &table, &col); err != nil {
			return nil, err
		}
		addColumnKey(out, schema, table, col)
	}
	return out, rows.Err()
}

func addColumnKey(out map[string]map[string]bool, schema, table, col string) {
	t := strings.ToLower(strings.TrimSpace(table))
	c := strings.ToLower(strings.TrimSpace(col))
	if t == "" || c == "" {
		return
	}
	keys := []string{t}
	if s := strings.ToLower(strings.TrimSpace(schema)); s != "" {
		keys = append(keys, s+"."+t)
	}
	for _, k := range keys {
		if out[k] == nil {
			out[k] = map[string]bool{}
		}
		out[k][c] = true
	}
}

// liveColumnQuery 拼出按表名过滤的列查询。表名走**参数**,不拼进语句 ——
// 这一条与探测语句不同,它有参数位可用,就该用参数位。
func liveColumnQuery(conn *model.Connection, names []string) (string, []any) {
	args := make([]any, 0, len(names))
	for _, n := range names {
		args = append(args, n)
	}
	switch engineFamily(conn.Engine) {
	case familyMySQL:
		return `SELECT table_schema, table_name, column_name FROM information_schema.columns
		        WHERE LOWER(table_name) IN (` + placeholders(len(names), "?") + `)`, args
	case familyPostgres:
		return `SELECT table_schema, table_name, column_name FROM information_schema.columns
		        WHERE LOWER(table_name) IN (` + placeholders(len(names), "$") + `)`, args
	case familyOracle:
		return `SELECT owner, table_name, column_name FROM all_tab_columns
		        WHERE LOWER(table_name) IN (` + placeholders(len(names), ":") + `)`, args
	}
	return "", nil
}

func placeholders(n int, style string) string {
	out := make([]string, n)
	for i := range out {
		switch style {
		case "$":
			out[i] = fmt.Sprintf("$%d", i+1)
		case ":":
			out[i] = fmt.Sprintf(":%d", i+1)
		default:
			out[i] = "?"
		}
	}
	return strings.Join(out, ",")
}

// ProbeRowsExist 回答"这张表里有没有行"。加一个无默认值的非空列时,它就是成败的分界。
func ProbeRowsExist(conn *model.Connection, table sqlutil.Ref) (bool, error) {
	name := refFullName(table)
	if !safeIdent(name) {
		return false, ErrProbeUnsafeName
	}
	return probe(conn, "SELECT 1 FROM "+quoteIdent(conn.Engine, name)+" WHERE 1=1")
}

// ProbeNullsExist 回答"这一列里有没有 NULL"。改成 NOT NULL 时,有一个就会失败。
func ProbeNullsExist(conn *model.Connection, table sqlutil.Ref, column string) (bool, error) {
	name := refFullName(table)
	if !safeIdent(name) || !safeIdent(column) {
		return false, ErrProbeUnsafeName
	}
	return probe(conn, "SELECT 1 FROM "+quoteIdent(conn.Engine, name)+
		" WHERE "+quoteIdent(conn.Engine, column)+" IS NULL")
}

// ProbeDuplicatesExist 回答"这几列上有没有重复值"。加唯一约束时,有一组就会失败。
//
// NULL 不参与唯一性(标准 SQL 里 NULL 不等于 NULL),所以先排掉 —— 不排的话,两行
// 都是 NULL 会被数成一组重复,而它们其实不冲突。
func ProbeDuplicatesExist(conn *model.Connection, table sqlutil.Ref, columns []string) (bool, error) {
	name := refFullName(table)
	if !safeIdent(name) || len(columns) == 0 {
		return false, ErrProbeUnsafeName
	}
	cols := make([]string, 0, len(columns))
	notNull := make([]string, 0, len(columns))
	for _, c := range columns {
		if !safeIdent(c) {
			return false, ErrProbeUnsafeName
		}
		q := quoteIdent(conn.Engine, c)
		cols = append(cols, q)
		notNull = append(notNull, q+" IS NOT NULL")
	}
	list := strings.Join(cols, ", ")
	return probe(conn, "SELECT 1 FROM (SELECT "+list+" FROM "+quoteIdent(conn.Engine, name)+
		" WHERE "+strings.Join(notNull, " AND ")+
		" GROUP BY "+list+" HAVING COUNT(*) > 1) d WHERE 1=1")
}

// probe 跑一条只读探测:有没有至少一行。
//
// 行数限制用**驱动层**的 LIMIT 而不是拼 SQL:Oracle 用 ROWNUM、SQL Server 用 TOP,
// 拼法各不相同。这里让查询自己跑,但只读第一行就关掉 —— 数据库看到客户端不再取数,
// 会停下来。配合 10 秒超时,最坏情况是超时,而超时会如实报成"未能核对"。
func probe(conn *model.Connection, query string) (bool, error) {
	if !RealExecSupported(conn) {
		return false, apimsg.Errorf("实例未配置真实执行凭据,无法探测数据")
	}
	if !ProbeSupported(conn.Engine) {
		return false, apimsg.Errorf("引擎 %q 不支持数据探测", conn.Engine)
	}
	db, release, err := openConn(conn)
	if err != nil {
		return false, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), precheckTimeout)
	defer cancel()

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := rows.Next()
	if err := rows.Err(); err != nil {
		return false, err
	}
	return found, nil
}

func refFullName(t sqlutil.Ref) string {
	if t.Schema != "" {
		return t.Schema + "." + t.Name
	}
	return t.Name
}

