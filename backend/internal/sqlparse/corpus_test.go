package sqlparse_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"aegisdb/internal/gateway"
	"aegisdb/internal/sqlparse"
)

// 语料库文件的格式:每条语句前一行 `-- @case <名字>`,其余都是语句原文(可以多行)。
const caseMark = "-- @case "

type corpusCase struct{ name, sql string }

func loadCorpus(t *testing.T, file string) []corpusCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var out []corpusCase
	for _, chunk := range strings.Split("\n"+strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"+caseMark)[1:] {
		name, sql, _ := strings.Cut(chunk, "\n")
		out = append(out, corpusCase{strings.TrimSpace(name), strings.TrimSpace(sql)})
	}
	return out
}

// 每种方言跑哪些文件。收集来的 MySQL 用例同样喂给 TiDB:网关上 TiDB 走的就是 MySQL 系的判定。
var corpora = []struct {
	dialect, engine string
	files           []string
}{
	{sqlparse.DialectMySQL, "MySQL", []string{"curated_mysql.sql", "harvested_mysql.sql"}},
	{sqlparse.DialectTiDB, "TiDB", []string{"curated_tidb.sql", "harvested_mysql.sql"}},
	{sqlparse.DialectOracle, "Oracle", []string{"curated_oracle.sql", "harvested_oracle.sql"}},
}

// diff 是解析器和现有判定的一处不同。
type diff struct{ kind, detail, name, sql string }

// compare 把一条分好句的语句同时交给两边。没有现有判定的栏位不比。
func compare(dialect, engine, sql string, r sqlparse.Result) []diff {
	switch {
	case r.Status != sqlparse.StatusOK:
		return []diff{{kind: "解析失败", detail: r.Status.String()}}
	case len(r.Statements) == 0:
		return []diff{{kind: "解析出 0 条"}}
	case len(r.Statements) > 1:
		return []diff{{kind: "解析出多条"}}
	}
	st := r.Statements[0]
	var out []diff
	if v := gateway.ParseVerb(sql); v != st.Verb && !sameMetadataRead(v, st) {
		out = append(out, diff{kind: "动词不同", detail: "现有 " + v + " / 解析器 " + st.Verb})
	}
	read := gateway.IsRead(sql)
	if read != st.ReadOnly {
		if st.ReadOnly {
			out = append(out, diff{kind: "解析器认为只读,现有认为不是", detail: st.Verb})
		} else {
			out = append(out, diff{kind: "现有认为只读,解析器认为不是", detail: st.Verb})
		}
	}
	noWhere := (st.Kind == sqlparse.KindUpdate || st.Kind == sqlparse.KindDelete) && !st.HasWhere && !st.PlanOnly
	if existing := gateway.NoWhereFor(engine, sql); existing != noWhere {
		if noWhere {
			out = append(out, diff{kind: "解析器认为无 WHERE,现有认为有", detail: st.Verb})
		} else {
			out = append(out, diff{kind: "现有认为无 WHERE,解析器认为有", detail: st.Verb})
		}
	}
	if existing := gateway.WritesServerFile(sql); existing != st.WritesFile {
		if st.WritesFile {
			out = append(out, diff{kind: "解析器认为写文件,现有认为不写", detail: st.Verb})
		} else {
			out = append(out, diff{kind: "现有认为写文件,解析器认为不写", detail: st.Verb})
		}
	}
	return out
}

// DESC 在两个 MySQL 系解析器里都是 SHOW COLUMNS 的别名,不算差异。
func sameMetadataRead(existing string, st sqlparse.Statement) bool {
	return st.Kind == sqlparse.KindRead && (existing == "DESC" || existing == "DESCRIBE")
}

type dialectReport struct {
	dialect          string
	cases, stmts, ok int
	curatedStmts     int
	curatedOK        int
	diffs            []diff
}

func runCorpus(t *testing.T) []dialectReport {
	t.Helper()
	var reports []dialectReport
	for _, c := range corpora {
		rep := dialectReport{dialect: c.dialect}
		for _, f := range c.files {
			for _, cs := range loadCorpus(t, f) {
				rep.cases++
				for _, s := range gateway.SplitForJudge(c.engine, cs.sql) {
					rep.stmts++
					r := sqlparse.Parse(c.dialect, s)
					curated := strings.HasPrefix(f, "curated_")
					if curated {
						rep.curatedStmts++
					}
					if r.Status == sqlparse.StatusOK {
						rep.ok++
						if curated {
							rep.curatedOK++
						}
					}
					for _, d := range compare(c.dialect, c.engine, s, r) {
						d.name, d.sql = cs.name, s
						rep.diffs = append(rep.diffs, d)
					}
				}
			}
		}
		reports = append(reports, rep)
	}
	return reports
}

// 语料库本身要能跑通;想看明细就设 AEGISDB_SQLPARSE_REPORT=<文件> 重新生成报告。
func TestCorpusReport(t *testing.T) {
	reports := runCorpus(t)
	for _, r := range reports {
		if r.stmts == 0 {
			t.Errorf("%s: 语料库是空的", r.dialect)
		}
		t.Logf("%s: 用例 %d,语句 %d,解析成功 %d,差异 %d", r.dialect, r.cases, r.stmts, r.ok, len(r.diffs))
	}
	path := os.Getenv("AEGISDB_SQLPARSE_REPORT")
	if path == "" {
		return
	}
	var b strings.Builder
	b.WriteString("# 解析器语料库差异报告\n\n自动生成,勿手改。重新生成:\n\n")
	b.WriteString("    AEGISDB_SQLPARSE_REPORT=<文件> go test ./internal/sqlparse -run TestCorpusReport\n\n")
	b.WriteString("收集来的用例没有分引擎,也含故意写坏的片段,所以总通过率偏低;手写样例的通过率更接近真实语句。\n\n")
	b.WriteString("| 方言 | 用例 | 语句 | 解析成功 | 总通过率 | 手写样例通过 | 差异 |\n|---|---|---|---|---|---|---|\n")
	for _, r := range reports {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %.1f%% | %d/%d | %d |\n", r.dialect, r.cases, r.stmts, r.ok, 100*float64(r.ok)/float64(r.stmts), r.curatedOK, r.curatedStmts, len(r.diffs))
	}
	for _, r := range reports {
		fmt.Fprintf(&b, "\n## %s\n", r.dialect)
		groups := map[string][]diff{}
		for _, d := range r.diffs {
			groups[d.kind] = append(groups[d.kind], d)
		}
		kinds := make([]string, 0, len(groups))
		for k := range groups {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			ds := groups[k]
			sort.SliceStable(ds, func(i, j int) bool { return ds[i].sql < ds[j].sql })
			fmt.Fprintf(&b, "\n### %s(%d)\n\n", k, len(ds))
			seen := map[string]bool{}
			for _, d := range ds {
				key := d.detail + d.sql
				if seen[key] {
					continue
				}
				seen[key] = true
				s := strings.Join(strings.Fields(d.sql), " ")
				if rs := []rune(s); len(rs) > 140 {
					s = string(rs[:140]) + "…"
				}
				detail := ""
				if d.detail != "" {
					detail = "(" + d.detail + ")"
				}
				fmt.Fprintf(&b, "- %s`%s`\n", detail, strings.ReplaceAll(s, "`", "'"))
			}
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 绕过样例:这些语句上,**解析器单独看可能比现有判定松**。每条都钉住两边各自的结论,升级解析器或改判定
// 时结论变了会在这里失败。这份表是"解析器只能叠加"的证据,见 ADR 0019。
func TestPinnedBypassSamples(t *testing.T) {
	type expect struct {
		status   sqlparse.Status
		stmts    int
		readOnly bool // 解析器看原文的结论;只有 stmts==1 时有意义
	}
	cases := []struct {
		name, engine, dialect, sql string
		parser                     expect
	}{
		// V-01:解析器把注释体当注释,读出 0 条;MariaDB 会执行。
		{"V-01 M 注释", "MariaDB", sqlparse.DialectMySQL, "/*M! DROP TABLE t */", expect{sqlparse.StatusOK, 0, false}},
		{"V-01 M 注释带版本", "MariaDB", sqlparse.DialectMySQL, "/*M!100100 DROP TABLE t */", expect{sqlparse.StatusOK, 0, false}},
		// SELECT 后面的注释体同样被忽略:解析器说只读,MariaDB 上是写文件。
		{"V-01 M 注释藏 OUTFILE", "MariaDB", sqlparse.DialectMySQL, "SELECT 1 /*M! INTO OUTFILE '/tmp/a' */", expect{sqlparse.StatusOK, 1, true}},
		// V-02:解析器不认 SET STATEMENT,解析失败。
		{"V-02 SET STATEMENT", "MariaDB", sqlparse.DialectMySQL, "SET STATEMENT a=1 FOR DROP TABLE t", expect{sqlparse.StatusError, 0, false}},
		// V-04:`#` 把 INTO OUTFILE 藏到下一行。解析器和服务器读法相同,这条它能帮上忙。
		{"V-04 # 注释", "MySQL", sqlparse.DialectMySQL, "SELECT 1 # c\nINTO OUTFILE '/tmp/p'", expect{sqlparse.StatusOK, 1, false}},
		{"V-04 # 注释 TiDB", "TiDB", sqlparse.DialectTiDB, "SELECT 1 # c\nINTO OUTFILE '/tmp/p'", expect{sqlparse.StatusOK, 1, false}},
		// --1 在 MySQL 上是减负一,解析器读法正确。
		{"--1", "MySQL", sqlparse.DialectMySQL, "SELECT 1--1 INTO OUTFILE '/tmp/p'", expect{sqlparse.StatusOK, 1, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := sqlparse.Parse(c.dialect, c.sql)
			if r.Status != c.parser.status || len(r.Statements) != c.parser.stmts {
				t.Fatalf("解析器结论变了: status=%s n=%d err=%s", r.Status, len(r.Statements), r.Err)
			}
			if c.parser.stmts == 1 && r.Statements[0].ReadOnly != c.parser.readOnly {
				t.Fatalf("解析器结论变了: %+v", r.Statements[0])
			}
			// 不管解析器怎么读,现有判定都要拦得住:要么不算只读,要么被写文件那道闸拦下。
			if gateway.ReadOnlyStatements(c.engine, c.sql) && !gateway.WritesServerFile(c.sql) {
				t.Errorf("现有判定拦不住: %q", c.sql)
			}
		})
	}
}

// 先分句再解析,V-01 的注释体就进了解析器:判定文本里注释标记已经去掉。接入调用方时必须保持这个顺序。
func TestSplitBeforeParseSeesMariaDBCommentBody(t *testing.T) {
	parts := gateway.SplitForJudge("MariaDB", "/*M! DROP TABLE t */")
	if len(parts) != 1 {
		t.Fatalf("分句结果 %q", parts)
	}
	r := sqlparse.Parse(sqlparse.DialectMySQL, parts[0])
	if r.Status != sqlparse.StatusOK || len(r.Statements) != 1 || r.Statements[0].Verb != "DROP" {
		t.Fatalf("%+v", r)
	}
}

// 收集现有测试里的 SQL,写进 testdata。只在要刷新语料时手动跑:
//
//	AEGISDB_SQLPARSE_HARVEST=1 go test ./internal/sqlparse -run TestHarvestCorpus
func TestHarvestCorpus(t *testing.T) {
	if os.Getenv("AEGISDB_SQLPARSE_HARVEST") == "" {
		t.Skip("设 AEGISDB_SQLPARSE_HARVEST=1 才刷新语料")
	}
	sqlLike := regexp.MustCompile(`(?is)^\s*(/\*.*?\*/\s*|--[^\n]*\n\s*|#[^\n]*\n\s*)*\(?\s*(SELECT|INSERT|UPDATE|DELETE|REPLACE|MERGE|CREATE|ALTER|DROP|TRUNCATE|RENAME|GRANT|REVOKE|SHOW|DESC|DESCRIBE|EXPLAIN|WITH|CALL|SET|USE|BEGIN|DECLARE|LOAD|HANDLER|DO)\s`)
	skipFile := regexp.MustCompile(`(?i)mongo|dws|postgres|_pg|sqlite`)
	oracleFile := regexp.MustCompile(`(?i)oracle|plsql|sqlplus`)
	found := map[string]map[string]string{"mysql": {}, "oracle": {}}
	fset := token.NewFileSet()
	for _, dir := range []string{"../gateway", "../review", "../osc", "../../pkg/sqlutil"} {
		files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if skipFile.MatchString(filepath.Base(path)) {
				continue
			}
			target := "mysql"
			if oracleFile.MatchString(filepath.Base(path)) {
				target = "oracle"
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || len(s) > 4096 || strings.Contains(s, "%") || !sqlLike.MatchString(s) {
					return true
				}
				s = strings.TrimSpace(s)
				if strings.Contains(s, caseMark) {
					return true
				}
				if _, dup := found[target][s]; !dup {
					found[target][s] = fmt.Sprintf("%s/%s", filepath.Base(dir), filepath.Base(path))
				}
				return true
			})
		}
	}
	for target, m := range found {
		keys := make([]string, 0, len(m))
		for s := range m {
			keys = append(keys, s)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("-- 由 TestHarvestCorpus 从现有测试收集,勿手改。\n")
		for _, s := range keys {
			fmt.Fprintf(&b, "%s%s\n%s\n", caseMark, m[s], s)
		}
		if err := os.WriteFile(filepath.Join("testdata", "harvested_"+target+".sql"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d 条", target, len(keys))
	}
}
