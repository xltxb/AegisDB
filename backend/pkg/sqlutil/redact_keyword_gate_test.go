package sqlutil

import (
	"strings"
	"testing"
	"time"
)

// redactAllRules 是加关键词预检之前的 RedactSecrets:七条规则无条件全跑。预检只许省时间,
// 不许改结果 —— 下面拿它逐条对照。
func redactAllRules(sql string) string {
	out := redactIdentifiedClauses(sql)
	out = rePasswordFn.ReplaceAllString(out, `${1}'***'${2}`)
	out = reSetPassword.ReplaceAllString(out, `${1}'***'`)
	out = rePasswordKV.ReplaceAllString(out, `${1}'***'`)
	out = reReplicaPassword.ReplaceAllString(out, `${1}'***'`)
	out = reAccessKey.ReplaceAllString(out, `${1}'***'`)
	out = reConninfoPassword.ReplaceAllString(out, `${1}***`)
	return out
}

func TestRedactKeywordGateChangesNothing(t *testing.T) {
	cases := []string{
		"",
		"SELECT 1",
		"INSERT INTO t (id, name) VALUES (1, 'a');",
		"CREATE USER 'app'@'%' IDENTIFIED BY 'S3cret';",
		"create user app identified by Pw_123 replace Old_456",
		"ALTER USER 'a'@'%' IDENTIFIED WITH 'mysql_native_password' BY 'pw';",
		"SET PASSWORD FOR 'u'@'h' = 'pw';",
		"SELECT PASSWORD('pw');",
		"CREATE ROLE r WITH ENCRYPTED PASSWORD 'pw';",
		"CHANGE MASTER TO MASTER_PASSWORD='pw', MASTER_HOST='h';",
		"CHANGE REPLICATION SOURCE TO SOURCE_PASSWORD = 'pw';",
		"CREATE SUBSCRIPTION s CONNECTION 'host=h password=secret dbname=d' PUBLICATION p;",
		"COPY t FROM 'obs://b/x' CREDENTIALS 'access_key_id=AK secret_access_key=SK';",
		"CREATE FOREIGN TABLE f () SERVER s OPTIONS (ACCESS_KEY 'AK', SECRET_ACCESS_KEY 'SK');",
		"UPDATE t_user SET password = 'x' WHERE id = 1;",
		"INSERT INTO t_account (name, password) VALUES ('a', 'plain');",
		// 大小写折叠:K(开尔文符号)与 ſ(长 s)在 (?i) 下等同 k / s,预检不能因此漏跑。
		"CREATE TABLE t (secret_Key varchar(8)); SELECT 1 FROM x WHERE access_Key = 'AK';",
		"ALTER ROLE r PAſſWORD 'pw';",
		"alter role r paſſword = 'pw'",
		"x IDENTIFIED BY 'a' y PASSWORD 'b' z ACCESS_KEY='c' master_password='d'",
	}
	for _, sql := range cases {
		if got, want := RedactSecrets(sql), redactAllRules(sql); got != want {
			t.Errorf("预检改变了结果\n  in: %q\n got: %q\nwant: %q", sql, got, want)
		}
	}
}

// 不含任何口令关键词的大脚本(大批量 INSERT —— 发布单最常见的那种)不该再按 MB 计时。
func TestRedactSkipsRulesWhenNoKeyword(t *testing.T) {
	line := "INSERT INTO t_account_config (account_info_id, account_type, account_name, site_id) VALUES (1001, 'viber', 'some_account_name_here', 77);\n"
	sql := strings.Repeat(line, 4*1024*1024/len(line)) // ≈4MB
	start := time.Now()
	out := RedactSecrets(sql)
	if out != sql {
		t.Fatal("没有关键词的文本不该被改写")
	}
	// 全跑七条约 0.46 秒/MB(≈1.8 秒);预检后只剩一次 ToLower + 三次 Contains。
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("4MB 无关键词脚本脱敏用了 %v", d)
	}
}
