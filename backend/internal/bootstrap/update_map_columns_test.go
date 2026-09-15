package bootstrap

// map 形式的 Updates 里,键就是列名 —— 而它绕过模型上的 column 标注。
//
// 这次把 `sql` / `rows` / `key` / `database` 四个保留字列名换掉(迁移 0039..0044)时,
// 模型上的 `gorm:"column:…"` 一改就全对了 —— 除了三处:
//
//	Updates(map[string]any{… "rows": n …})
//
// GORM 拿 map 的键**直接当列名**,不看模型。所以这三处仍然写着旧名字,而且不会有任何
// 东西提醒:编译通过,vet 干净。它们是被端到端测试抓出来的(异步任务卡在 running、
// 导出变成 failed),而那已经是运气 —— 一个没有被测到的字段会一路安静地写不进去。
//
// 这条闸从**源码**这一侧看:任何传给 Updates/Update* 的 map 字面量,它的键必须是某个
// 模型真的声明过的列名。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// modelColumn 把一个模型字段折算成它真正的列名,第二个返回值 false 表示这个字段
// 根本不入库(gorm:"-" 或非导出字段)。两条闸都按同一套规则读列名 —— 分成两份就
// 意味着它们迟早对同一个字段给出两个答案。
func modelColumn(fieldName, tag string) (string, bool) {
	if fieldName == "" || !ast.IsExported(fieldName) {
		return "", false
	}
	gtag := reflect.StructTag(tag).Get("gorm")
	if strings.TrimSpace(gtag) == "-" {
		return "", false
	}
	col := gormColumnName(fieldName)
	if j := strings.Index(gtag, "column:"); j >= 0 {
		rest := gtag[j+len("column:"):]
		if k := strings.IndexAny(rest, ";"); k >= 0 {
			rest = rest[:k]
		}
		col = rest
	}
	return col, true
}

// modelStruct 是 internal/model 里一个真正对应到表的结构体。
type modelStruct struct {
	name   string
	fields []struct{ name, tag string }
}

// tableModels 列出 internal/model 里**每一个**落库的模型 —— 判据是它有 TableName()。
//
// 从前这份清单是 db.go 里的 allModels,也就是 AutoMigrate 拿去建表的那一份。
// AutoMigrate 整条路已经拆掉(schema 只由 migrations/*.sql 定义),那份清单随之消失,
// 而这两条闸还需要知道「有哪些模型」。
//
// 这里不把那份清单抄进测试文件:一份手抄的清单会漂,而漏掉的那个模型就正好是这两条闸
// 管不到的那个 —— 新加一张表的人不会想到还要回来登记一次。改成从源码里数:凡是声明了
// TableName() 的结构体就是一张表,这个判据跟着代码自己长。
func tableModels(t *testing.T) []modelStruct {
	t.Helper()
	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, "../model", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse ../model: %v", err)
	}

	structs := map[string]*ast.StructType{}
	hasTableName := map[string]bool{}
	for _, p := range pkg {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if !ok {
							continue
						}
						if st, ok := ts.Type.(*ast.StructType); ok {
							structs[ts.Name.Name] = st
						}
					}
				case *ast.FuncDecl:
					if d.Name.Name != "TableName" || d.Recv == nil || len(d.Recv.List) != 1 {
						continue
					}
					rt := d.Recv.List[0].Type
					if star, ok := rt.(*ast.StarExpr); ok {
						rt = star.X
					}
					if id, ok := rt.(*ast.Ident); ok {
						hasTableName[id.Name] = true
					}
				}
			}
		}
	}

	out := []modelStruct{}
	for name := range hasTableName {
		st, ok := structs[name]
		if !ok {
			t.Fatalf("%s 有 TableName() 却找不到它的结构体声明 —— 这份解析漏了东西", name)
		}
		m := modelStruct{name: name}
		for _, f := range st.Fields.List {
			tag := ""
			if f.Tag != nil {
				if unq, err := strconv.Unquote(f.Tag.Value); err == nil {
					tag = unq
				}
			}
			// 嵌入字段的 Names 是**空的**(`gorm.Model` 这种),于是下面的循环一次都不转,
			// 它带进来的每一个列都从这两条闸眼前消失 —— 而消失的方式是静默的。
			// internal/model 今天零嵌入,所以这里从来没被触发过;真正的问题是「哪天有人
			// 加了一个」,那时该响的是这一句,而不是一条永远绿着的守卫。
			if len(f.Names) == 0 {
				t.Fatalf("%s 里有一个嵌入字段(匿名字段)。这两条闸按 f.Names 遍历结构体字段,\n"+
					"    而嵌入字段的 Names 是空的 —— 它带进来的列会被**静默跳过**,\n"+
					"    保留字检查和 Updates 列名检查都管不到它们。\n"+
					"    要么把字段展开写,要么先教会 tableModels 递归展开嵌入结构体。",
					name)
			}
			for _, n := range f.Names {
				m.fields = append(m.fields, struct{ name, tag string }{n.Name, tag})
			}
		}
		out = append(out, m)
	}
	// 空清单会让下面两条闸**静默地**变成永远通过 —— 而那正是它们要防的那种失效。
	if len(out) == 0 {
		t.Fatal("在 ../model 里没数出任何模型,这两条闸等于没跑")
	}
	return out
}

// knownColumns 汇总每个模型声明过的列名(含 column: 标注与 GORM 默认命名)。
func knownColumns(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range tableModels(t) {
		for _, f := range m.fields {
			col, ok := modelColumn(f.name, f.tag)
			if !ok {
				continue
			}
			out[col] = true
		}
	}
	// GORM 自己维护的时间戳列,模型里靠字段名约定而非标注。
	for _, c := range []string{"created_at", "updated_at", "deleted_at"} {
		out[c] = true
	}
	return out
}

func TestUpdateMaps_UseRealColumnNames(t *testing.T) {
	cols := knownColumns(t)
	fset := token.NewFileSet()
	roots := []string{"../repository", "../service", "../handler"}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				return perr
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !strings.HasPrefix(sel.Sel.Name, "Update") {
					return true
				}
				for _, arg := range call.Args {
					lit, ok := arg.(*ast.CompositeLit)
					if !ok {
						continue
					}
					mt, ok := lit.Type.(*ast.MapType)
					if !ok {
						continue
					}
					if id, ok := mt.Key.(*ast.Ident); !ok || id.Name != "string" {
						continue
					}
					for _, e := range lit.Elts {
						kv, ok := e.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						k, ok := kv.Key.(*ast.BasicLit)
						if !ok || k.Kind != token.STRING {
							continue
						}
						name, _ := strconv.Unquote(k.Value)
						if !cols[name] {
							t.Errorf("%s:%d %s 的 map 里有键 %q,而没有任何模型声明过这个列名。\n"+
								"    map 形式的 Updates 拿键**直接当列名**,绕过模型上的 `gorm:\"column:…\"` —— \n"+
								"    写错了编译不会报,vet 也不会报,只有那个字段安静地写不进去。",
								fset.Position(k.Pos()).Filename, fset.Position(k.Pos()).Line, sel.Sel.Name, name)
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}

var upperRunRe = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])|([a-z\d])([A-Z])`)

// gormColumnName 复现 GORM 的默认命名(NamingStrategy.ColumnName):驼峰转蛇形,
// 连续大写当作一个词(SQL → sql,DBName → db_name)。
func gormColumnName(field string) string {
	return strings.ToLower(upperRunRe.ReplaceAllString(field, "${1}${3}_${2}${4}"))
}

// 列名不得是 PostgreSQL 保留字 —— 模型这一侧。
//
// 隔壁 TestMigrationsAvoidReservedWords 只看 .sql 文件,而且**加了双引号就放行**。于是
// 存量的 key / database / sql / rows 靠引号活了很久,而 ADR 0016 §二要的是换名字:
//
//	· 此后每一处手写 SQL 都得记得加引号。忘一次就是一句 `syntax error at or near
//	  "user"`,而它往往指向的是**下一个** token。
//	· 在 PG 上加了双引号还会把名字变成大小写敏感的,`"User"` 和 user 从此是两个东西。
//	· GORM 生成的语句自己会加引号,所以 ORM 那条路一直没事 —— 出事的永远是那几句
//	  手写的 Where / Order,以及 map 形式的 Updates(见上一条用例)。
func TestModels_NoReservedColumnNames(t *testing.T) {
	for _, m := range tableModels(t) {
		for _, f := range m.fields {
			col, ok := modelColumn(f.name, f.tag)
			if !ok {
				continue
			}
			// 复用隔壁那张**完整的** PostgreSQL 保留字表(migration_reserved_words_test.go),
			// 不另抄一份缩水版 —— 抄一份就意味着两张表会分叉,而分叉的那一半正好漏掉
			// 下一次要撞的那个词。
			if pgReserved[strings.ToUpper(col)] {
				t.Errorf("%s.%s 的列名是 %q —— PostgreSQL 保留字。\n"+
					"    加双引号能让它跑起来,但此后每一处手写 SQL 都得记得加,忘一次就是一句\n"+
					"    syntax error,而且引号还会让这个名字变成大小写敏感的。ADR 0016 §二:换个名字。\n"+
					"    改法:给字段加 gorm:\"column:<新名字>\",并配一条 RENAME COLUMN 的迁移。",
					m.name, f.name, col)
			}
		}
	}
}
