package gateway

// psql 的反斜杠命令,在网关这边翻译成等价的 catalog 查询。
//
// 和 Oracle 的 SHOW / DESC 是同一类问题(见 oracle_sqlplus.go):`\dt` 是 **psql 自己
// 的命令**,由客户端解释成一条 pg_catalog 查询再发出去,服务端从来没见过反斜杠。
// 网关走驱动,原样发过去就是一个语法错误。
//
// 这一组比 Oracle 那组更值得修:`\dt`、`\d 表` 是 PostgreSQL 用户的肌肉记忆,而
// DWS 与 PolarDB-PG 都是 PG 系,敲的人比 SHOW USER 多。
//
// 同样的规矩:**翻译在判定之后**。判定看到的永远是用户输入的原文,只有交给驱动的
// 那一份被改写 —— 否则字典里写着某个词的规则就再也匹配不到东西。
//
// 覆盖面刻意窄:只翻译**只读的自省命令**。`\c`(切库)、`\i`(读本地文件)、`\copy`
// (客户端读写文件)、`\x`/`\timing`(psql 自己的显示状态)都不翻译 —— 它们要的东西
// 在网关这边根本不存在,硬造一个答案等于让人以为自己在跟一个有状态的 psql 会话说话。

import (
	"regexp"
	"strings"
)

// 反斜杠命令 + 可选的名字/模式。名字会拼进字符串字面量,所以这条正则是一道
// **安全边界**,不只是解析规则 —— 放宽它之前先想清楚。
// 允许 % 与 _ 是因为 psql 的这些命令本来就收模式。
var psqlMetaRe = regexp.MustCompile(`(?is)^\s*\\([a-zA-Z?]+)(?:\s+([A-Za-z0-9_$%.]+))?\s*;?\s*$`)

// 不带系统对象的可见 schema 过滤,几条查询共用。
const psqlUserSchemas = `n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'`

// PostgresPsqlMeta rewrites a psql backslash command into equivalent SQL.
//
// 第二个返回值是"翻译了没有"。没翻译就原样下发,让 PostgreSQL 自己报错。
func PostgresPsqlMeta(sql string) (string, bool) {
	s := strings.TrimSpace(sql)
	if !strings.HasPrefix(s, `\`) {
		return sql, false // 绝大多数语句在这里就走了
	}
	m := psqlMetaRe.FindStringSubmatch(s)
	if m == nil {
		return sql, false // 形状怪的一律不猜
	}
	cmd, arg := m[1], quoteLit(m[2])

	// relkind 决定 \dt \dv \di \ds 各自看哪一类对象 —— 它们只差这一个字母。
	relkind := map[string]string{"dt": "r','p", "dv": "v','m", "di": "i", "ds": "S"}
	if k, ok := relkind[strings.ToLower(cmd)]; ok {
		q := `SELECT n.nspname AS schema, c.relname AS name, pg_catalog.pg_get_userbyid(c.relowner) AS owner` +
			` FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace` +
			` WHERE c.relkind IN ('` + k + `') AND ` + psqlUserSchemas
		if arg != "" {
			q += ` AND c.relname LIKE '` + arg + `'`
		}
		return q + ` ORDER BY 1, 2`, true
	}

	switch strings.ToLower(cmd) {
	case "l", "list":
		return `SELECT datname AS name, pg_catalog.pg_get_userbyid(datdba) AS owner, pg_catalog.pg_encoding_to_char(encoding) AS encoding` +
			` FROM pg_catalog.pg_database WHERE datistemplate = false ORDER BY 1`, true
	case "dn":
		return `SELECT n.nspname AS name, pg_catalog.pg_get_userbyid(n.nspowner) AS owner` +
			` FROM pg_catalog.pg_namespace n WHERE ` + psqlUserSchemas + ` ORDER BY 1`, true
	case "du", "dg":
		return `SELECT rolname AS role, rolsuper AS is_super, rolcreatedb AS can_create_db,` +
			` rolcanlogin AS can_login FROM pg_catalog.pg_roles WHERE rolname NOT LIKE 'pg\_%' ORDER BY 1`, true
	case "d":
		if arg == "" {
			// 光一个 \d 在 psql 里是"列出所有关系"。
			return `SELECT n.nspname AS schema, c.relname AS name, c.relkind AS kind` +
				` FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace` +
				` WHERE c.relkind IN ('r','p','v','m','S') AND ` + psqlUserSchemas + ` ORDER BY 1, 2`, true
		}
		return psqlDescribe(arg), true
	}
	// \x \timing \c \i \copy \e \q … —— 见文件头的说明,不翻译。
	return sql, false
}

// psqlDescribe is \d <表>:列出这张表的列。
//
// 名字可能带 schema 前缀(app.t_user)。用 information_schema 而不是 pg_attribute,
// 因为前者的列名在各 PG 系发行版(DWS、PolarDB-PG)之间更稳。
func psqlDescribe(name string) string {
	schema, table := "", name
	if i := strings.LastIndex(name, "."); i >= 0 {
		schema, table = name[:i], name[i+1:]
	}
	q := `SELECT column_name, data_type, character_maximum_length AS max_len, is_nullable, column_default` +
		` FROM information_schema.columns WHERE table_name = '` + table + `'`
	if schema != "" {
		q += ` AND table_schema = '` + schema + `'`
	}
	return q + ` ORDER BY ordinal_position`
}
