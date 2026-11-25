package sqlparse

import (
	"reflect"
	"strings"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/bytebase/omni/oracle/parser"
)

func parseOracle(sql string) ([]Statement, error) {
	list, err := parser.Parse(sql)
	if err != nil {
		return nil, err
	}
	out := make([]Statement, 0, list.Len())
	for _, n := range list.Items {
		if raw, ok := n.(*ast.RawStmt); ok {
			n = raw.Stmt
		}
		out = append(out, oracleStatement(n))
	}
	return out, nil
}

func oracleStatement(n ast.Node) Statement {
	if e, ok := n.(*ast.ExplainPlanStmt); ok && e.Statement != nil {
		st := oracleStatement(e.Statement)
		st.PlanOnly = true
		st.ReadOnly = true
		return st
	}
	verb := verbFromType(reflect.TypeOf(n).Elem().Name())
	var (
		hasWhere bool
		tables   tableSet
	)
	addName := func(o *ast.ObjectName) {
		if o != nil {
			tables.add(o.Schema, o.Name)
		}
	}
	kind := Kind("")
	switch s := n.(type) {
	case *ast.PLSQLBlock:
		verb, kind = "BEGIN", KindPLSQL // 与事务的 BEGIN 不同:Oracle 的 BEGIN 总是块
	case *ast.AdminDDLStmt:
		verb = strings.ToUpper(s.Action) // DROP USER、CREATE TABLESPACE 这类都归到这个类型
	case *ast.SelectStmt:
		hasWhere = s.WhereClause != nil
	case *ast.InsertStmt:
		addName(s.Table)
	case *ast.UpdateStmt:
		hasWhere = s.WhereClause != nil
		addName(s.Table)
	case *ast.DeleteStmt:
		hasWhere = s.WhereClause != nil
		addName(s.Table)
	case *ast.MergeStmt:
		addName(s.Target)
	case *ast.CreateTableStmt:
		addName(s.Name)
	case *ast.AlterTableStmt:
		addName(s.Name)
	case *ast.TruncateStmt:
		addName(s.Table)
	case *ast.DropStmt:
		if s.ObjectType == ast.OBJECT_TABLE && s.Names != nil {
			for _, item := range s.Names.Items {
				if o, ok := item.(*ast.ObjectName); ok {
					addName(o)
				}
			}
		}
	}
	writes := false
	ast.Inspect(n, func(c ast.Node) bool {
		switch s := c.(type) {
		case *ast.TableRef:
			addName(s.Name)
		case *ast.SelectStmt:
			if s.Into != nil || s.IntoVars != nil || s.ForUpdate != nil {
				writes = true
			}
		}
		return true
	})
	if kind == "" {
		kind = kindOf(verb)
	}
	return Statement{
		Kind:     kind,
		Verb:     verb,
		Tables:   tables.list,
		HasWhere: hasWhere,
		ReadOnly: kind == KindRead || (kind == KindSelect && !writes),
	}
}
