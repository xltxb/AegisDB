package gateway

import (
	"regexp"
	"strings"
)

// 动词是 SELECT,不等于这条语句只读。
//
// 判定按首动词归维度:SELECT 落进 select(读)。可有一类函数**调用即生效**,写在
// SELECT 里照样踢连接、改配置、写文件、借另一条连接执行任意 SQL —— 而只读角色的
// select 维度处处是 allow。这里把它们认出来,让这条语句按 write 判,交给能力矩阵。
//
// 这是一张**已知清单**,不是证明:用户自建的函数里写了 DML,这里看不见(那要解析函数
// 体)。清单收的是各引擎内置、权限足够时一调就有副作用的那些 —— 也正是攻击者会先试
// 的那些。归到 write 而不是直接拒绝:DBA 踢一个卡死的会话是日常操作,该按他的写权限
// 放行或送审,不该被一刀切掉。

// effectfulFuncs 按名匹配(小写,不含 schema 限定;`pg_catalog.x(` 照样命中)。
var effectfulFuncs = []string{
	// PostgreSQL / GaussDB / DWS —— 会话与实例管理
	"pg_terminate_backend", "pg_cancel_backend", "pg_reload_conf", "pg_rotate_logfile",
	"pg_promote", "pg_switch_wal", "pg_switch_xlog", "pg_create_restore_point",
	"pg_create_physical_replication_slot", "pg_create_logical_replication_slot",
	"pg_drop_replication_slot", "pg_logical_emit_message", "pg_stat_reset",
	"pg_stat_reset_shared", "pg_stat_statements_reset", "set_config",
	// 序列
	"nextval", "setval",
	// 服务器文件与大对象
	"pg_read_file", "pg_read_binary_file", "pg_ls_dir", "pg_file_write", "pg_file_unlink",
	"pg_file_rename", "lo_import", "lo_export", "lo_unlink", "lo_create", "lo_from_bytea", "lo_put",
	// 借别的连接执行 / 把字符串当 SQL 执行
	"dblink", "dblink_exec", "dblink_send_query", "dblink_connect", "dblink_open",
	"query_to_xml", "query_to_xml_and_xmlschema", "cursor_to_xml",
	// MySQL
	"load_file", "sys_exec", "sys_eval",
	// SQL Server
	"openrowset", "opendatasource", "openquery", "xp_cmdshell",
}

// effectfulPackages 是 Oracle 的包:`utl_http.request(…)` 这类调用按包名认。
var effectfulPackages = []string{
	"utl_http", "utl_file", "utl_tcp", "utl_smtp", "utl_mail", "utl_inaddr",
	"dbms_ldap", "dbms_pipe", "dbms_scheduler", "dbms_job", "dbms_sql", "dbms_xmlquery",
}

var effectfulFuncRe = regexp.MustCompile(`(?i)(?:^|[^\pL\pN_$#])(` +
	strings.Join(effectfulFuncs, "|") + `)\s*\(|(?:^|[^\pL\pN_$#])(` +
	strings.Join(effectfulPackages, "|") + `)\s*\.`)

// EffectfulCall returns the name of a side-effecting built-in the statement calls
// ("" when none). String literals are blanked first — 'pg_terminate_backend(1)'
// is data — while identifier quotes are dropped, so `"dblink_exec"(…)` still counts.
//
// \' 能把调用藏进"字面量",`#` 能拆开函数名与括号(backslash.go、hash_comment.go),每种读法都看。
func EffectfulCall(sql string) string {
	for _, r := range judgeReadings(sql) {
		text := strings.NewReplacer(`"`, " ", "`", " ", "[", " ", "]", " ").
			Replace(blankStringLiterals(StripComments(r)))
		m := effectfulFuncRe.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		if m[1] != "" {
			return strings.ToUpper(m[1])
		}
		return strings.ToUpper(m[2])
	}
	return ""
}
