package gateway

import "testing"

// MySQL 默认把 \' 当转义:SELECT 'a\'', 1 INTO OUTFILE '/tmp/x' -- ' 里 'a\'' 是一个字符串,后面的
// INTO OUTFILE 是真语法,结尾的 ' 在注释里。按标准词法读,INTO OUTFILE 却落在字符串里。
func TestWritesServerFile_BackslashHidesOutfile(t *testing.T) {
	for _, s := range []string{
		`SELECT 'a\'', 1 INTO OUTFILE '/tmp/x' -- '`,
		`SELECT 'a\'', 1 INTO DUMPFILE '/tmp/x' -- '`,
		`SELECT "a\"", 1 INTO OUTFILE '/tmp/x' -- "`,
		`SELECT 'a\'', 1 /*! INTO OUTFILE '/tmp/x' */ -- '`,
		`SELECT 'a\'', 1 INTO/**/OUTFILE '/tmp/x' -- '`,
		// 两种读法叠在一起:字面量边界靠反斜杠,INTO 与 OUTFILE 之间靠 #
		`SELECT 'a\'#b', 1 INTO#x` + "\n" + `OUTFILE '/tmp/x' -- '`,
		// `--1` 不是注释,但 StripComments 会删到行尾,所以 # 那份读法也要留一份不去注释的
		"SELECT 1--1 INTO#x\nOUTFILE '/tmp/x'",
	} {
		if !WritesServerFile(s) {
			t.Errorf("%s 应认出写文件", s)
		}
	}
}

// 原有行为:字面量里的 into outfile 仍是数据,带转义引号的普通语句不算写文件。
func TestWritesServerFile_BackslashKeepsLiteralsAsData(t *testing.T) {
	for _, s := range []string{
		`SELECT 'it\'s' FROM t`,
		`SELECT 'a\\', 'into outfile'`,
		`SELECT "a\"" FROM t`,
		`INSERT INTO t VALUES ('C:\\tmp\\outfile')`,
		`SELECT a INTO @x FROM t WHERE b = 'it\'s'`,
	} {
		if WritesServerFile(s) {
			t.Errorf("%s 不是写文件", s)
		}
	}
}

func TestEvaluate_BackslashHidesOutfile(t *testing.T) {
	e := NewRiskEngine(&fakeStore{caps: map[string]string{
		"select|prod": "allow", "write|prod": "allow", "ddl|prod": "deny", "grant|prod": "deny",
	}})
	const hidden = `SELECT 'a\'', 1 INTO OUTFILE '/tmp/x' -- '`
	for _, engine := range []string{"MySQL", "MariaDB", "TiDB"} {
		if v := e.EvaluateFor([]int64{1}, engine, "prod", hidden); v.Action != ActionDeny {
			t.Errorf("%s 上应拒绝,实际 %+v", engine, v)
		}
	}
	// 原有行为:带转义引号的普通读写照常放行
	for _, s := range []string{
		`SELECT 'it\'s' FROM t`,
		`SELECT * FROM t WHERE a = 'x\'y'`,
		`UPDATE t SET a = 'it\'s' WHERE id = 1`,
	} {
		if v := e.EvaluateFor([]int64{1}, "MySQL", "prod", s); v.Action != ActionAllow {
			t.Errorf("%s 应放行,实际 %+v", s, v)
		}
	}
}
