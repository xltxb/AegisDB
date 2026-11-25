package sqlparse

import (
	"reflect"
	"sync"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver" // 解析器必须有一个字面量驱动,这个不带 TiDB 的类型系统
)

// parser.Parser 不是并发安全的,而且内部缓存不小,所以放池子里复用。panic 时不放回,由 Parse 兜底。
var tidbParsers = sync.Pool{New: func() any { return parser.New() }}

func parseTiDB(sql string) ([]Statement, error) {
	p := tidbParsers.Get().(*parser.Parser)
	nodes, _, err := p.Parse(sql, "", "")
	tidbParsers.Put(p)
	if err != nil {
		return nil, err
	}
	out := make([]Statement, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, tidbStatement(n))
	}
	return out, nil
}

func tidbStatement(n ast.StmtNode) Statement {
	if e, ok := n.(*ast.ExplainStmt); ok && e.Stmt != nil {
		st := tidbStatement(e.Stmt)
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
	case *ast.SetOprStmt:
		verb = "SELECT" // UNION / INTERSECT / EXCEPT
	case *ast.InsertStmt:
		if s.IsReplace {
			verb = "REPLACE"
		}
	case *ast.UpdateStmt:
		hasWhere = s.Where != nil
	case *ast.DeleteStmt:
		hasWhere = s.Where != nil
	case *ast.AlterTableStmt:
		verb = "ALTER"
	}
	w := &tidbWalker{}
	n.Accept(w)
	kind := kindOf(verb)
	return Statement{
		Kind:       kind,
		Verb:       verb,
		Tables:     w.tables.list,
		HasWhere:   hasWhere,
		ReadOnly:   kind == KindRead || (kind == KindSelect && !w.writes),
		WritesFile: w.file,
	}
}

// tidbWalker 收集表名,顺带看子查询里有没有 INTO 或锁。
type tidbWalker struct {
	tables tableSet
	writes bool
	file   bool
}

func (w *tidbWalker) Enter(n ast.Node) (ast.Node, bool) {
	switch s := n.(type) {
	case *ast.TableName:
		w.tables.add(s.Schema.O, s.Name.O)
	case *ast.SelectStmt:
		if s.SelectIntoOpt != nil {
			w.file = true
		}
		if s.SelectIntoOpt != nil || (s.LockInfo != nil && s.LockInfo.LockType != ast.SelectLockNone) {
			w.writes = true
		}
	}
	return n, false
}

func (w *tidbWalker) Leave(n ast.Node) (ast.Node, bool) { return n, true }
