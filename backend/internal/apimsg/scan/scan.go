// Package scan 从后端源码里找出会出现在接口报错 msg 里的中文,生成 apimsg 的文案表,
// 同时找出绕过文案表的写法(漏翻的来源)。生成器与测试共用它。
package scan

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Found 是一句收录的中文。Fmt 表示它是 apimsg.Errorf 的格式串。
type Found struct {
	Zh  string
	Fmt bool
	Pos string
}

var cjk = regexp.MustCompile(`[\p{Han}]`)

// HasCJK 报告字符串里是否有汉字。
func HasCJK(s string) bool { return cjk.MatchString(s) }

// 不扫的包:bootstrap 只在启动时出错,不进接口;storemigrate 是运维子命令 migrate-store,
// 报错只打在终端上,同样不进接口。
var skipDirs = map[string]bool{"bootstrap": true, "apimsg": true, "storemigrate": true}

// 这些包里出现的中文字面量一律收录 —— 它们几乎都是给接口返回的提示(报错、常量、WS 消息)。
// 其它包只收录 errors.New 与 apimsg.Errorf。
var allLiteralDirs = map[string]bool{"handler": true, "middleware": true}

// 这些函数的参数是审计 / 日志文本,不是给界面看的提示,不收录。
var recordFuncs = map[string]bool{"passwordNote": true, "append": true, "Sprintf": true, "String": true, "Printf": true, "Println": true, "Join": true}

// 这些函数与 apimsg.Errorf 同签名、把格式串转交给它:调用处的格式串收录为 Fmt。
var fmtWrappers = map[string]bool{"invalidf": true, "grantInvalid": true, "invalidInspection": true, "msgf": true}

// Dir 扫描 internalDir(backend/internal)下的源码。
func Dir(internalDir string) (found []Found, violations []string, err error) {
	fset := token.NewFileSet()
	err = filepath.WalkDir(internalDir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, _ := filepath.Rel(internalDir, p)
		top := strings.Split(filepath.ToSlash(rel), "/")[0]
		if d.IsDir() {
			if skipDirs[top] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || filepath.Base(p) == "apidocs.go" {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		fo, vi := file(fset, f, allLiteralDirs[top])
		found = append(found, fo...)
		violations = append(violations, vi...)
		return nil
	})
	return found, violations, err
}

func lit(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	return s, err == nil
}

func callName(c *ast.CallExpr) (pkg, fn string) {
	switch f := c.Fun.(type) {
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			return id.Name, f.Sel.Name
		}
		return "", f.Sel.Name
	case *ast.Ident:
		return "", f.Name
	}
	return "", ""
}

// cjkInside 报告表达式里有没有带汉字的字符串字面量。
func cjkInside(e ast.Expr) bool {
	has := false
	ast.Inspect(e, func(n ast.Node) bool {
		if s, ok := lit2(n); ok && HasCJK(s) {
			has = true
		}
		return !has
	})
	return has
}

func lit2(n ast.Node) (string, bool) {
	if e, ok := n.(ast.Expr); ok {
		return lit(e)
	}
	return "", false
}

func file(fset *token.FileSet, f *ast.File, allLits bool) (found []Found, violations []string) {
	pos := func(n ast.Node) string {
		p := fset.Position(n.Pos())
		return fmt.Sprintf("%s:%d", filepath.ToSlash(p.Filename), p.Line)
	}
	handled := map[ast.Expr]bool{} // 已按调用处理过的字面量
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pkg, fn := callName(c)
		switch {
		case pkg == "errors" && fn == "New" && len(c.Args) == 1:
			if s, ok := lit(c.Args[0]); ok {
				handled[c.Args[0]] = true
				if HasCJK(s) {
					found = append(found, Found{Zh: s, Pos: pos(c)})
				}
			} else if cjkInside(c.Args[0]) {
				violations = append(violations, pos(c)+": errors.New 的参数是拼出来的中文,改用 apimsg.Errorf")
			}
		case pkg == "fmt" && fn == "Errorf" && len(c.Args) > 0:
			if cjkInside(c.Args[0]) {
				violations = append(violations, pos(c)+": 中文 fmt.Errorf,改用 apimsg.Errorf(同签名)")
			}
		case pkg == "apimsg" && (fn == "Text" || fn == "T") && len(c.Args) == 1:
			if s, ok := lit(c.Args[0]); ok {
				handled[c.Args[0]] = true
				found = append(found, Found{Zh: s, Pos: pos(c)})
			} else {
				violations = append(violations, pos(c)+": apimsg.Text 的参数必须是字面量")
			}
		case pkg == "" && fmtWrappers[fn] && len(c.Args) > 0:
			if s, ok := lit(c.Args[0]); ok {
				handled[c.Args[0]] = true
				found = append(found, Found{Zh: s, Fmt: true, Pos: pos(c)})
			} else if cjkInside(c.Args[0]) {
				violations = append(violations, pos(c)+": "+fn+" 的格式串必须是字面量")
			}
		case pkg == "apimsg" && (fn == "Errorf" || fn == "Sprintf") && len(c.Args) > 0:
			if id, ok := c.Args[0].(*ast.Ident); ok && id.Name == "format" {
				break // 包装函数(如 invalidf)把格式串转交过来,格式串在包装函数的调用处收录
			}
			s, ok := lit(c.Args[0])
			if !ok {
				violations = append(violations, pos(c)+": apimsg.Errorf 的格式串必须是字面量")
				break
			}
			handled[c.Args[0]] = true
			found = append(found, Found{Zh: s, Fmt: true, Pos: pos(c)})
		case pkg == "resp" && (fn == "Fail" || fn == "Abort" || fn == "FailData") && len(c.Args) >= 3:
			m := c.Args[2]
			if _, ok := lit(m); !ok && cjkInside(m) {
				violations = append(violations, pos(c)+": resp."+fn+" 的 msg 是拼出来的中文,改用 resp.FailErr + apimsg.Errorf")
			}
		case recordFuncs[fn] || strings.Contains(strings.ToLower(fn), "audit") || strings.HasPrefix(fn, "notify"):
			for _, a := range c.Args {
				ast.Inspect(a, func(x ast.Node) bool {
					if e, ok := x.(ast.Expr); ok {
						if _, isLit := lit(e); isLit {
							handled[e] = true
						}
					}
					return true
				})
			}
		}
		return true
	})
	if allLits {
		ast.Inspect(f, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok || handled[e] {
				return true
			}
			if s, ok := lit(e); ok && HasCJK(s) {
				found = append(found, Found{Zh: s, Pos: pos(e)})
			}
			return true
		})
	}
	return found, violations
}

// Unique 去重并排序(同一句可能出现在多处;同一句既是固定串又是格式串时按格式串收)。
func Unique(found []Found) []Found {
	by := map[string]Found{}
	for _, f := range found {
		if old, ok := by[f.Zh]; ok && old.Fmt {
			continue
		}
		by[f.Zh] = f
	}
	out := make([]Found, 0, len(by))
	for _, f := range by {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Zh < out[j].Zh })
	return out
}
