package service

// 升级单变更类型(dml / ddl)的判定与校验。
//
// 规则只有三条,全部在提交时执行(发布内容一经提交就不可变 —— 内联 SQL 在
// 行里,脚本按 SHA-256 校验 —— 所以不需要执行时复判):
//   1. 两类语句不得同单;
//   2. 声明的类型必须与内容一致;
//   3. 未声明时由内容推断,落到单据上。

import (
	"fmt"
	"strings"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
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
		return "", fmt.Errorf("变更类型只能是 dml 或 ddl,收到 %q", declared)
	}
	// One classifying pass. The FIRST statement of each kind is remembered so a
	// refusal can point at lines, not at a concept.
	firstOf := map[string]int{} // kind → 1-based statement index
	for i, one := range stmts {
		verb := strings.ToUpper(gateway.ParseVerb(one))
		var kind string
		switch {
		case dmlVerbs[verb]:
			kind = model.ChangeDML
		case ddlVerbs[verb]:
			kind = model.ChangeDDL
		case neutralVerbs[verb]:
			continue // transaction/session control belongs to either kind
		default:
			return "", fmt.Errorf("第 %d 条语句的动词 %s 无法归类为 DML/DDL,不能进入升级单", i+1, verb)
		}
		if _, seen := firstOf[kind]; !seen {
			firstOf[kind] = i + 1
		}
		if declared != "" && kind != declared {
			return "", fmt.Errorf("变更类型为 %s 的升级单不允许 %s 语句:第 %d 条(%s)。请拆成两张单,或改正变更类型",
				strings.ToUpper(declared), strings.ToUpper(kind), i+1, verb)
		}
	}
	if len(firstOf) == 2 {
		return "", fmt.Errorf("升级单不允许同时包含 DML(第 %d 条)和 DDL(第 %d 条)语句,请拆成两张单",
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
	return "", fmt.Errorf("变更内容中没有 DML/DDL 语句,无法确定变更类型")
}
