package sqlparse

import (
	"reflect"

	"github.com/bytebase/omni/mysql/ast"
	"github.com/bytebase/omni/mysql/parser"
)

func parseMySQL(sql string) ([]Statement, error) {
	list, err := parser.Parse(sql)
	if err != nil {
		return nil, err
	}
	out := make([]Statement, 0, list.Len())
	for _, n := range list.Items {
		out = append(out, mysqlStatement(n))
	}
	return out, nil
}

func mysqlStatement(n ast.Node) Statement {
	if e, ok := n.(*ast.ExplainStmt); ok && e.Stmt != nil {
		st := mysqlStatement(e.Stmt)
		st.PlanOnly = !e.Analyze
		st.ReadOnly = st.PlanOnly || st.ReadOnly
		st.WritesFile = st.WritesFile && !st.PlanOnly
		return st
	}
	verb := verbFromType(reflect.TypeOf(n).Elem().Name())
	var hasWhere bool
	switch s := n.(type) {
	case *ast.SelectStmt:
		hasWhere = s.Where != nil
	case *ast.InsertStmt:
		if s.IsReplace {
			verb = "REPLACE"
		}
	case *ast.UpdateStmt:
		hasWhere = s.Where != nil
	case *ast.DeleteStmt:
		hasWhere = s.Where != nil
	}
	var tables tableSet
	writes, file := false, false
	ast.Inspect(n, func(c ast.Node) bool {
		switch s := c.(type) {
		case *ast.TableRef:
			tables.add(s.Schema, s.Name)
		case *ast.SelectStmt:
			if s.Into != nil || s.ForUpdate != nil {
				writes = true
			}
			if s.Into != nil && (s.Into.Outfile != "" || s.Into.Dumpfile != "") {
				file = true
			}
		}
		return true
	})
	kind := kindOf(verb)
	return Statement{
		Kind:       kind,
		Verb:       verb,
		Tables:     tables.list,
		HasWhere:   hasWhere,
		ReadOnly:   kind == KindRead || (kind == KindSelect && !writes),
		WritesFile: file,
	}
}
