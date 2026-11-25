package gateway

import "testing"

// SELECT … INTO OUTFILE / DUMPFILE 在数据库服务器上写文件 —— 落一个 webshell 只要一条
// "查询"。它的动词是 SELECT,按动词判就是一次读,只读角色也放行。导出和流水线早就拒
// 了它,终端走的判定却没有,于是同一条语句换个入口就过去了。
//
// 拦在判定核心里,所有入口一起生效。识别要看两种读法:MySQL 的 `--` 后面必须跟空白
// 才是注释,`1--1` 是减负一,剥注释时却会连同后面的 INTO OUTFILE 一起剥掉。
func TestEvaluate_SelectIntoOutfileIsDenied(t *testing.T) {
	e := NewRiskEngine(&fakeStore{})
	for _, sql := range []string{
		"SELECT * FROM t INTO OUTFILE '/tmp/x'",
		"select '<?php ?>' into dumpfile '/var/www/x.php'",
		"SELECT 1--1 INTO OUTFILE '/tmp/p'",
		"SELECT 1 INTO/**/OUTFILE '/tmp/p'",
		"SELECT 1 /*!50000 INTO OUTFILE '/tmp/p' */",
	} {
		if v := e.EvaluateFor([]int64{1}, "MySQL 8.0", "dev", sql); v.Action != ActionDeny {
			t.Errorf("EvaluateFor(%q) = %s; 写服务器文件必须拒绝", sql, v.Action)
		}
	}
	// 字面量里出现这几个词只是数据。
	if v := e.EvaluateFor([]int64{1}, "MySQL 8.0", "dev", "SELECT 'into outfile' AS s FROM t"); v.Action != ActionAllow {
		t.Errorf("字面量不该触发:%s", v.Action)
	}
}
