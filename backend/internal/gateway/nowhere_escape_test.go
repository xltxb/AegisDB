package gateway

import "testing"

// 反斜杠转义:同一串字节,在两族引擎里是两条不同的语句。
//
//	UPDATE t SET a='x\' WHERE 1=1 --'
//
// MySQL 默认 sql_mode 下 `\'` 是**转义的引号**,字符串一直延伸到最后那个引号 ——
// 整条语句是 `UPDATE t SET a = <一长串字面量>`,**没有 WHERE**,一次整表更新。
// PostgreSQL / Oracle / SQLite 的标准字符串里反斜杠不转义,字符串在第二个引号处
// 就结束了,后面的 `WHERE 1=1` 是真的 WHERE 子句。
//
// 判定层原先两边都按"反斜杠不是转义"读,于是在 MySQL 上把字面量里的 `WHERE 1=1`
// 当成了结构:无 WHERE 拦截整层被跳过,严格模式的 high 降成了 mid,而审批人看到的
// 规则文案写着"能力矩阵 · 需审批" —— 他以为自己在批一条带条件的更新。
const backslashEscapedNoWhere = `UPDATE t SET a='x\' WHERE 1=1 --'`

func TestUnscopedMutation_BackslashEscapeCannotFakeAWhere(t *testing.T) {
	// 空引擎 = 还没解析出引擎的调用方,按最严的那种解释判。
	for _, engine := range []string{"mysql", "tidb", "mariadb", "polardb", "MySQL 8.0", ""} {
		if !DialectFor(engine).UnscopedMutation(backslashEscapedNoWhere) {
			t.Errorf("engine=%q:整表更新被 `\\'` 里的假 WHERE 骗过了 —— 无 WHERE 拦截整层失效", engine)
		}
	}
	if !NoWhere(backslashEscapedNoWhere) {
		t.Error("NoWhere 没有引擎信息时应当取更严的那种解释")
	}
}

func TestUnscopedMutation_StandardStringsKeepTheRealWhere(t *testing.T) {
	// 这几族引擎里那条语句真的带 WHERE,判成无 WHERE 就是误报 —— 会把一条普通的
	// 条件更新升成 high,让人开始怀疑网关判错了库。
	for _, engine := range []string{"postgres", "postgresql", "dws", "gaussdb", "oracle", "sqlite"} {
		if DialectFor(engine).UnscopedMutation(backslashEscapedNoWhere) {
			t.Errorf("engine=%q:标准字符串里反斜杠不转义,这条语句是带 WHERE 的", engine)
		}
	}
}

// 反斜杠的引入不能动摇原本判得对的那些。
func TestUnscopedMutation_UnchangedForOrdinaryStatements(t *testing.T) {
	for _, c := range []struct {
		sql  string
		want bool
		why  string
	}{
		{`UPDATE t SET a='x'`, true, "裸的整表更新"},
		{`UPDATE users SET note='where'`, true, "WHERE 只出现在字面量里,不是子句"},
		{`UPDATE t SET a='x' WHERE id=1`, false, "有真 WHERE"},
		{`DELETE FROM t`, true, "整表删除"},
		{`DELETE FROM t WHERE id=1`, false, "有真 WHERE"},
		{`UPDATE t SET a='it''s' WHERE id=1`, false, "双写引号是标准转义,WHERE 在字面量外"},
		{`SELECT * FROM t`, false, "不是 DML"},
	} {
		for _, engine := range []string{"mysql", "postgres", "oracle", "sqlite", ""} {
			if got := DialectFor(engine).UnscopedMutation(c.sql); got != c.want {
				t.Errorf("engine=%q %q:期望 %v(%s),实际 %v", engine, c.sql, c.want, c.why, got)
			}
		}
	}
}
