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

// knownColumns 汇总每个模型声明过的列名(含 column: 标注与 GORM 默认命名)。
//
// 清单直接用 allModels —— 就是 AutoMigrate 拿去建表的那一份。另抄一份模型清单,
// 漏掉的那个模型就正好是这条闸管不到的那个。
func knownColumns(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range allModels {
		rt := reflect.TypeOf(m).Elem()
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if f.PkgPath != "" {
				continue
			}
			tag := f.Tag.Get("gorm")
			if strings.TrimSpace(tag) == "-" {
				continue
			}
			col := gormColumnName(f.Name)
			if j := strings.Index(tag, "column:"); j >= 0 {
				rest := tag[j+len("column:"):]
				if k := strings.IndexAny(rest, ";"); k >= 0 {
					rest = rest[:k]
				}
				col = rest
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
	for _, m := range allModels {
		rt := reflect.TypeOf(m).Elem()
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if f.PkgPath != "" {
				continue
			}
			tag := f.Tag.Get("gorm")
			if strings.TrimSpace(tag) == "-" {
				continue
			}
			col := gormColumnName(f.Name)
			if j := strings.Index(tag, "column:"); j >= 0 {
				rest := tag[j+len("column:"):]
				if k := strings.IndexAny(rest, ";"); k >= 0 {
					rest = rest[:k]
				}
				col = rest
			}
			// 复用隔壁那张**完整的** PostgreSQL 保留字表(migration_reserved_words_test.go),
			// 不另抄一份缩水版 —— 抄一份就意味着两张表会分叉,而分叉的那一半正好漏掉
			// 下一次要撞的那个词。
			if pgReserved[strings.ToUpper(col)] {
				t.Errorf("%s.%s 的列名是 %q —— PostgreSQL 保留字。\n"+
					"    加双引号能让它跑起来,但此后每一处手写 SQL 都得记得加,忘一次就是一句\n"+
					"    syntax error,而且引号还会让这个名字变成大小写敏感的。ADR 0016 §二:换个名字。\n"+
					"    改法:给字段加 gorm:\"column:<新名字>\",并配一条 RENAME COLUMN 的迁移。",
					rt.Name(), f.Name, col)
			}
		}
	}
}
