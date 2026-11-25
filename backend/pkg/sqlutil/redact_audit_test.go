package sqlutil

// M-7(白盒审计):脱敏正则的三处遗漏。
//
// 这些语句会原样进不可变的审计哈希链、webhook 与飞书卡片。**审计链一旦写进去就
// 改不掉了** —— 漏一条口令,它就永久留在那里。

import (
	"strings"
	"testing"
)

func TestRedact_ReplicationPasswords(t *testing.T) {
	// \b 在 `_password` 前不成立(下划线和字母都是词字符),原规则认不出这一类。
	for _, sql := range []string{
		`CHANGE MASTER TO MASTER_HOST='h', MASTER_PASSWORD='s3cr3t'`,
		`CHANGE REPLICATION SOURCE TO SOURCE_PASSWORD='s3cr3t'`,
		`CHANGE MASTER TO master_password = 's3cr3t'`,
	} {
		if got := RedactSecrets(sql); strings.Contains(got, "s3cr3t") {
			t.Errorf("复制口令明文留在审计里: %q → %q", sql, got)
		}
	}
}

func TestRedact_ConninfoPassword(t *testing.T) {
	// 口令没有自己的引号,它在外层引号里面 —— 所有"找引号包着的值"的规则都够不着。
	sql := `CREATE SUBSCRIPTION s CONNECTION 'host=db1 user=rep password=s3cr3t dbname=app' PUBLICATION p`
	got := RedactSecrets(sql)
	if strings.Contains(got, "s3cr3t") {
		t.Errorf("conninfo 里的口令明文留在审计里: %q", got)
	}
	// 同一串里的非机密部分要留着 —— 审计要看得出这是连到哪台机器。
	if !strings.Contains(got, "host=db1") {
		t.Errorf("不该把整串都抹掉,审计需要看到连的是谁: %q", got)
	}
}

func TestRedact_ObjectStorageKeys(t *testing.T) {
	for _, sql := range []string{
		`CREATE FOREIGN TABLE t (...) SERVER obs OPTIONS (ACCESS_KEY 'AK123', SECRET_ACCESS_KEY 'SK456')`,
		`COPY t FROM 's3://b/f' CREDENTIALS 'aws_access_key_id=AK123;aws_secret_access_key=SK456'`,
	} {
		got := RedactSecrets(sql)
		if strings.Contains(got, "SK456") {
			t.Errorf("对象存储密钥明文留在审计里: %q → %q", sql, got)
		}
	}
}

// 幂等:好几条路径会对已经脱敏过的文本再脱敏一次,不能越脱越乱。
func TestRedact_StaysIdempotent(t *testing.T) {
	for _, sql := range []string{
		`CREATE USER u IDENTIFIED BY 'p'`,
		`CHANGE MASTER TO MASTER_PASSWORD='p'`,
		`CREATE SUBSCRIPTION s CONNECTION 'host=h password=p'`,
		`SET PASSWORD FOR 'u'@'%' = 'p'`,
	} {
		once := RedactSecrets(sql)
		twice := RedactSecrets(once)
		if once != twice {
			t.Errorf("不幂等: %q\n  一次: %q\n  两次: %q", sql, once, twice)
		}
	}
}

// 原有行为不能回归 —— 修补丁不能把已经守住的东西弄坏。
func TestRedact_ExistingRulesStillHold(t *testing.T) {
	cases := []string{
		`CREATE USER u IDENTIFIED BY 's3cr3t'`,
		`ALTER USER u IDENTIFIED WITH 'mysql_native_password' BY 's3cr3t'`,
		`SET PASSWORD FOR 'u'@'%' = 's3cr3t'`,
		`CREATE ROLE r WITH ENCRYPTED PASSWORD 's3cr3t'`,
		`ALTER USER u IDENTIFIED BY 's3cr3t' REPLACE 'old1'`,
	}
	for _, sql := range cases {
		got := RedactSecrets(sql)
		if strings.Contains(got, "s3cr3t") || strings.Contains(got, "old1") {
			t.Errorf("既有脱敏规则回归了: %q → %q", sql, got)
		}
	}
}
