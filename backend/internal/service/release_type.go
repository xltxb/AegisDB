package service

// 升级单变更类型(dml / ddl)的判定与校验。
//
// 规则只有三条,全部在提交时执行(发布内容一经提交就不可变 —— 内联 SQL 在
// 行里,脚本按 SHA-256 校验 —— 所以不需要执行时复判):
//   1. 两类语句不得同单;
//   2. 声明的类型必须与内容一致;
//   3. 未声明时由内容推断,落到单据上。

import (
	"strings"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
)

// 分类口径。SELECT 归 DML:MySQL 官方文档即把 SELECT 列为 DML,且变更单带
// SELECT 自查是常态。GRANT/REVOKE 归 DDL:权限是结构不是数据。事务/会话控制
// 是中性的 —— 迁移脚本几乎总带着 BEGIN/COMMIT,它们不说明这张单是哪种变更。
var (
	dmlVerbs = map[string]bool{
		"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
		"MERGE": true, "REPLACE": true, "CALL": true, "EXPLAIN": true,
	}
	ddlVerbs = map[string]bool{
		"CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true,
		"RENAME": true, "COMMENT": true, "GRANT": true, "REVOKE": true,
	}
	neutralVerbs = map[string]bool{
		"BEGIN": true, "START": true, "COMMIT": true, "ROLLBACK": true,
		"SET": true, "USE": true, "SAVEPOINT": true,
	}
)

// releaseChangeType validates a release body against its declared type and
// returns the effective type. declared is "", "dml" or "ddl"; anything else is
// refused rather than ignored — a typo that silently meant "推断" would let a
// caller believe they constrained something they didn't.
func releaseChangeType(declared string, stmts []string) (string, error) {
	declared = strings.ToLower(strings.TrimSpace(declared))
	if declared != "" && declared != model.ChangeDML && declared != model.ChangeDDL {
		return "", apimsg.Errorf("变更类型只能是 dml 或 ddl,收到 %q", declared)
	}
	// One classifying pass. The FIRST statement of each kind is remembered so a
	// refusal can point at lines, not at a concept.
	firstOf := map[string]int{} // kind → 1-based statement index
	for i, one := range stmts {
		verb, kind := changeKindOf(one)
		switch {
		case kind == "" && neutralVerbs[verb]:
			continue // transaction/session control belongs to either kind
		case kind == "":
			return "", apimsg.Errorf("第 %d 条语句的动词 %s 无法归类为 DML/DDL,不能进入升级单", i+1, verb)
		}
		if _, seen := firstOf[kind]; !seen {
			firstOf[kind] = i + 1
		}
		if declared != "" && kind != declared {
			return "", apimsg.Errorf("变更类型为 %s 的升级单不允许 %s 语句:第 %d 条(%s)。请拆成两张单,或改正变更类型",
				strings.ToUpper(declared), strings.ToUpper(kind), i+1, verb)
		}
	}
	if len(firstOf) == 2 {
		return "", apimsg.Errorf("升级单不允许同时包含 DML(第 %d 条)和 DDL(第 %d 条)语句,请拆成两张单",
			firstOf[model.ChangeDML], firstOf[model.ChangeDDL])
	}
	if declared != "" {
		return declared, nil
	}
	for kind := range firstOf {
		return kind, nil
	}
	// Only neutral statements — a "change" that changes nothing. Refusing here
	// beats a ticket that sails through approval and executes a COMMIT.
	return "", apimsg.Errorf("变更内容中没有 DML/DDL 语句,无法确定变更类型")
}

// changeKindOf 返回语句的动词与它归属的变更类型(dml / ddl);中性或认不出的返回空类型。
func changeKindOf(stmt string) (verb, kind string) {
	verb = strings.ToUpper(gateway.ParseVerb(stmt))
	switch {
	case dmlVerbs[verb]:
		return verb, model.ChangeDML
	case ddlVerbs[verb]:
		return verb, model.ChangeDDL
	}
	return verb, ""
}

// changeTypeConflict 是规范审查里的那一半 releaseChangeType:只报"建单时一定会被拒"的两种
// 冲突 —— 声明的类型与内容不符、DML 与 DDL 同单,措辞与建单时相同。
//
// 2026-11-12:外部平台先调 /open/sql-review 预检,不过就不建单。DDL 单里夹着 UPDATE,预检只报了
// ALGORITHM,DML/DDL 混单要等建单才说 —— 而预检一失败就走不到建单,用户以为没有这道检查。
// 认不出的动词、全是中性语句不在这里报:审查不是建单,那些脚本也可能本就不打算进升级单。
// 返回冲突(nil = 没有)、它落在第几条语句(1 起),以及参数本身的错误。
func changeTypeConflict(declared string, stmts []string) (conflict error, stmt int, err error) {
	declared = strings.ToLower(strings.TrimSpace(declared))
	if declared != "" && declared != model.ChangeDML && declared != model.ChangeDDL {
		return nil, 0, apimsg.Errorf("变更类型只能是 dml 或 ddl,收到 %q", declared)
	}
	firstOf := map[string]int{}
	for i, one := range stmts {
		verb, kind := changeKindOf(one)
		if kind == "" {
			continue
		}
		if declared != "" && kind != declared {
			return apimsg.Errorf("变更类型为 %s 的升级单不允许 %s 语句:第 %d 条(%s)。请拆成两张单,或改正变更类型",
				strings.ToUpper(declared), strings.ToUpper(kind), i+1, verb), i + 1, nil
		}
		if _, seen := firstOf[kind]; !seen {
			firstOf[kind] = i + 1
		}
	}
	if len(firstOf) == 2 {
		return apimsg.Errorf("升级单不允许同时包含 DML(第 %d 条)和 DDL(第 %d 条)语句,请拆成两张单",
			firstOf[model.ChangeDML], firstOf[model.ChangeDDL]), max(firstOf[model.ChangeDML], firstOf[model.ChangeDDL]), nil
	}
	return nil, 0, nil
}
