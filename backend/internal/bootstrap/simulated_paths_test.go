package bootstrap

// 模拟实现必须被登记。
//
// 这条规则是被一次线上误判换来的:「批量巡检」把 88 台实例全报成「可连」,而其中有的
// 连 dial 都超时 —— 因为 Executor.Test 从仓库第一笔提交起就是一个
// `sleep(120ms); return true` 的桩,**从不拨号**,并且**没有记在任何待办里**。
// 它躺了将近两个月,直到有人拿它去判断一台生产实例能不能连。
//
// 桩本身不是罪 —— 演示数据集需要它。罪在于**没人知道它是桩**。所以规矩是:
//
//   凡是产出合成数据而不是去问目标库的路径,代码里带 `SIMULATED-PATH: <id>`,
//   docs/simulated-paths.md 里有对应的一行。
//
// 这条测试比对两边。加了模拟实现却不登记 —— 当场失败;清单里留着早已删掉的条目 ——
// 同样失败(一份过期的清单会让人以为某处还是假的,或者反过来)。
//
// 它扫的是**前后端两边的源码**:合成数据不只在 Go 里,终端那张结果表格是前端编的。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// markerRe 抓代码里的标记。id 用小写字母、数字和短横 —— 与文档表格里那一列一致。
var markerRe = regexp.MustCompile(`SIMULATED-PATH:\s*([a-z0-9-]+)`)

// docRowRe 抓文档表格里**仍在用**的那一列 id:`| exec-run |`。
//
// 刻意不匹配划掉的写法(`| ~~conn-test~~ |`):那是「已修」表,它的 id 本来就该在代码里
// 找不到 —— 匹配上会让它被报成"清单过期"。
//
// id 至少含一个字母,于是表格的分隔行(`|---|`)不会被当成 id。
var docRowRe = regexp.MustCompile("(?m)^\\|\\s*`?([a-z0-9-]*[a-z][a-z0-9-]*)`?\\s*\\|")

// docHeaderCells 是表头那一格,不是 id。
var docHeaderCells = map[string]bool{"id": true}

// scanRoots 是要扫的目录,相对于仓库根。前端也在里面 —— 见文件注释。
var scanRoots = []string{
	filepath.Join("backend", "internal"),
	filepath.Join("backend", "pkg"),
	filepath.Join("backend", "cmd"),
	filepath.Join("frontend", "src"),
}

var scanExts = map[string]bool{".go": true, ".ts": true, ".vue": true}

func repoRoot(t *testing.T) string {
	t.Helper()
	// 从 backend/internal/bootstrap 上溯两级到 backend,再一级到仓库根。
	return filepath.Join("..", "..", "..")
}

func markersInCode(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	found := map[string]string{} // id → 第一次出现的文件
	for _, sub := range scanRoots {
		dir := filepath.Join(root, sub)
		err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil // 目录缺失不算失败:前端可能没被检出
			}
			if !scanExts[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			// 测试文件本身会引用这些 id(比如这一条),不算声明。
			if strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, ".spec.ts") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			for _, m := range markerRe.FindAllStringSubmatch(string(b), -1) {
				if _, seen := found[m[1]]; !seen {
					found[m[1]] = p
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return found
}

func idsInDoc(t *testing.T) map[string]bool {
	t.Helper()
	p := filepath.Join(repoRoot(t), "docs", "simulated-paths.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("模拟实现清单读不到(%s): %v", p, err)
	}
	out := map[string]bool{}
	for _, m := range docRowRe.FindAllStringSubmatch(string(b), -1) {
		if docHeaderCells[m[1]] {
			continue
		}
		out[m[1]] = true
	}
	return out
}

func TestSimulatedPathsAreDocumented(t *testing.T) {
	code := markersInCode(t)
	doc := idsInDoc(t)

	// 正则失配的话两边都会是空的,然后这条测试永远绿着 —— 那比没有更糟。
	if len(code) == 0 {
		t.Fatal("一个 SIMULATED-PATH 标记都没扫到,这条守卫等于没生效")
	}
	if len(doc) == 0 {
		t.Fatal("清单里一个 id 都没解析到,这条守卫等于没生效")
	}

	missing := []string{}
	for id, file := range code {
		if !doc[id] {
			missing = append(missing, id+"("+file+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("这些模拟实现没有登记在 docs/simulated-paths.md 里:\n    %s\n"+
			"    模拟实现本身不是罪 —— 演示数据集需要它;罪在于没人知道它是桩。\n"+
			"    请在那份清单的表格里加一行:它假的是什么、用户能不能分辨。",
			strings.Join(missing, "\n    "))
	}

	stale := []string{}
	for id := range doc {
		if _, ok := code[id]; !ok {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("清单里这些 id 在代码里已经找不到了:%s\n"+
			"    要么是改真了(那就挪到「已修」那张表,并划掉 id),要么是 id 拼错了。\n"+
			"    一份过期的清单会让人以为某处还是假的,或者反过来 —— 两种都会误导。",
			strings.Join(stale, ", "))
	}
}

// 已修的那些要留在文档里、但**不能**还留在代码里 —— 否则"已修"是假的。
func TestFixedSimulatedPathIsGoneFromCode(t *testing.T) {
	if file, still := markersInCode(t)["conn-test"]; still {
		t.Errorf("conn-test 已经在清单里标为「已修」,但代码里还带着标记:%s", file)
	}
}
