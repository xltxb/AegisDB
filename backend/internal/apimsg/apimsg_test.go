package apimsg

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"aegisdb/internal/apimsg/scan"
	"aegisdb/internal/model"
)

// 文案表必须与源码一致:新加了一句中文报错却没跑 go generate,英文界面就会漏翻这一句。
// 同时不许有绕过文案表的写法(中文 fmt.Errorf、拼出来的 resp.Fail msg)。
func TestCatalogMatchesSource(t *testing.T) {
	found, violations, err := scan.Dir("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Errorf("绕过文案表:%s", v)
	}
	want := scan.Unique(found)
	if len(want) != len(catalog) {
		t.Errorf("源码里有 %d 句,文案表 %d 句 —— 请运行 go generate ./internal/apimsg", len(want), len(catalog))
	}
	for i := 0; i < len(want) && i < len(catalog); i++ {
		if want[i].Zh != catalog[i].Zh || want[i].Fmt != catalog[i].Fmt {
			t.Fatalf("文案表过期(第 %d 句:源码 %q,表里 %q)—— 请运行 go generate ./internal/apimsg", i, want[i].Zh, catalog[i].Zh)
		}
	}
}

// 中文逐字锁定:文案表里的每一句中文都记在 testdata/zh_baseline.txt。改了任何一句中文报错,
// 这里会失败 —— 那是有意为之:中文用户看到的话变了,要有人确认,并同时改英文译文。
// 确认后更新基线:go test ./internal/apimsg -run TestChineseBaseline -update-baseline
func TestChineseBaseline(t *testing.T) {
	path := filepath.Join("testdata", "zh_baseline.txt")
	var got []string
	for _, e := range catalog {
		got = append(got, strconv.Quote(e.Zh))
	}
	if os.Getenv("APIMSG_UPDATE_BASELINE") == "1" {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	base := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			base[l] = true
		}
	}
	cur := map[string]bool{}
	for _, g := range got {
		cur[g] = true
		if !base[g] {
			t.Errorf("新的或改过的中文报错(确认后更新基线,见注释):%s", g)
		}
	}
	for b := range base {
		if !cur[b] {
			t.Errorf("这句中文报错不见了或被改写(确认后更新基线,见注释):%s", b)
		}
	}
}

// sampleArgs 给格式串的每个动词造一个参数。
func sampleArgs(t *testing.T, format string) []any {
	verbs, ok := ParseVerbs(format)
	if !ok {
		t.Fatalf("格式串无法解析:%q", format)
	}
	args := make([]any, len(verbs))
	for i, v := range verbs {
		switch v[len(v)-1] {
		case 'd':
			args[i] = 40 + i
		case 'f', 'g':
			args[i] = 1.5 + float64(i)
		case 'w':
			args[i] = fmt.Errorf("驱动原文<%d>", i)
		default:
			args[i] = fmt.Sprintf("参数%d", i)
		}
	}
	return args
}

var literalRe = regexp.MustCompile(`\{'([^']*)'\}`)
var placeholderRe = regexp.MustCompile(`\{(\d+)\}`)

// renderTemplate 模拟前端:把词条里的 {n} 换成参数、{'x'} 还原成字符。
func renderTemplate(tmpl string, args map[string]string) string {
	s := placeholderRe.ReplaceAllStringFunc(tmpl, func(p string) string {
		return "\x00" + p[1:len(p)-1] + "\x00"
	})
	s = literalRe.ReplaceAllString(s, "$1")
	return regexp.MustCompile("\x00(\\d+)\x00").ReplaceAllStringFunc(s, func(p string) string {
		return args[strings.Trim(p, "\x00")]
	})
}

func readBlock(t *testing.T, file string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "locales", file))
	if err != nil {
		t.Skip("找不到前端词条文件,跳过:", err)
	}
	src := strings.ReplaceAll(string(raw), "\r\n", "\n")
	i := strings.Index(src, "\n  apiMsg: {\n")
	if i < 0 {
		t.Fatalf("%s 里没有 apiMsg 块", file)
	}
	out := map[string]string{}
	entry := regexp.MustCompile(`^    (m[0-9a-f]{8}): '((?:[^'\\]|\\.)*)',$`)
	unescape := strings.NewReplacer(`\\`, `\`, `\'`, `'`, `\n`, "\n", `\t`, "\t")
	for _, line := range strings.Split(src[i+13:], "\n") {
		if line == "  }," {
			break
		}
		if m := entry.FindStringSubmatch(line); m != nil {
			out[m[1]] = unescape.Replace(m[2])
		}
	}
	return out
}

// Go 生成的中文与前端 zh 词条渲染出来的必须一字不差(zh 词条只是同一句话的模板形式);
// 并且 apimsg.Errorf 与 fmt.Errorf 产出同一句中文。
func TestZhMatchesFrontendAndFmt(t *testing.T) {
	zh := readBlock(t, "zh.json5")
	if len(zh) != len(catalog) {
		t.Errorf("zh.json5 apiMsg 有 %d 条,文案表 %d 条 —— 请运行 go generate ./internal/apimsg", len(zh), len(catalog))
	}
	for _, e := range catalog {
		code := Code(e.Zh)
		tmpl, ok := zh[code]
		if !ok {
			t.Errorf("zh.json5 缺 %s(%q)", code, e.Zh)
			continue
		}
		if !e.Fmt {
			if got := renderTemplate(tmpl, nil); got != e.Zh {
				t.Errorf("%s:前端 %q,服务端 %q", code, got, e.Zh)
			}
			if r := Lookup(e.Zh); r == nil || r.Code != code {
				t.Errorf("%s:固定中文认不出来", code)
			}
			continue
		}
		args := sampleArgs(t, e.Zh)
		want := fmt.Errorf(e.Zh, args...).Error()
		err := Errorf(e.Zh, args...)
		if err.Error() != want {
			t.Errorf("%s:apimsg.Errorf %q,fmt.Errorf %q", code, err.Error(), want)
		}
		ref := RefOf(err)
		if ref == nil || ref.Code != code {
			t.Errorf("%s:没有 ref", code)
			continue
		}
		if got := renderTemplate(tmpl, ref.Args); got != want {
			t.Errorf("%s:前端渲染 %q,服务端 %q", code, got, want)
		}
	}
}

var cjk = regexp.MustCompile(`\p{Han}`)

// 中英对照:每一句都有英文、英文里没有汉字、占位符与中文一一对应。
func TestEnglishComplete(t *testing.T) {
	zh := readBlock(t, "zh.json5")
	en := readBlock(t, "en.json5")
	for code, z := range zh {
		e, ok := en[code]
		if !ok {
			t.Errorf("en.json5 缺 %s(中文:%s)", code, z)
			continue
		}
		if cjk.MatchString(renderTemplate(e, nil)) {
			t.Errorf("%s 还没翻译:%s", code, e)
		}
		zp, ep := placeholderRe.FindAllString(z, -1), placeholderRe.FindAllString(e, -1)
		sort.Strings(zp)
		sort.Strings(ep)
		if strings.Join(zp, ",") != strings.Join(ep, ",") {
			t.Errorf("%s 占位符不一致:中文 %v,英文 %v(%s)", code, zp, ep, e)
		}
	}
	for code := range en {
		if _, ok := zh[code]; !ok {
			t.Errorf("en.json5 多出 %s", code)
		}
	}
}

func TestRefOf(t *testing.T) {
	const outer = "创建失败:%s"
	const inner = "连接不存在"
	if !known[Code(outer)] || !known[Code(inner)] {
		t.Skip("样例句子不在文案表里")
	}

	// 内层是收录过的固定中文:嵌成 part,不是 raw。
	err := Errorf(outer, errors.New(inner))
	if err.Error() != "创建失败:连接不存在" {
		t.Fatalf("中文变了:%q", err.Error())
	}
	ref := RefOf(err)
	if ref == nil || len(ref.Parts) != 1 || ref.Parts[0].Code != Code(inner) || ref.Parts[0].Args[Slot] != "0" {
		t.Fatalf("内层应嵌成 part:%+v", ref)
	}

	// 内层是驱动原文:raw,原文不翻。
	ref = RefOf(Errorf(outer, errors.New("Error 1062: Duplicate entry")))
	if ref == nil || ref.Parts[0].Code != Raw || ref.Parts[0].Args["text"] != "Error 1062: Duplicate entry" {
		t.Fatalf("驱动原文应是 raw part:%+v", ref)
	}

	// %w 链照旧。
	sentinel := errors.New("sentinel")
	if !errors.Is(Errorf("参数错误: %w", sentinel), sentinel) && known[Code("参数错误: %w")] {
		t.Error("errors.Is 断了")
	}

	// 外面又套了一层没收录的话:不能只翻里层半句。
	wrapped := fmt.Errorf("outer context: %w", Errorf(outer, errors.New(inner)))
	if r := RefOf(wrapped); r != nil {
		t.Errorf("外层没收录时不应给 ref,got %+v", r)
	}

	// 没收录的固定串 / 空串。
	if Lookup("Error 1045: Access denied") != nil || Lookup("") != nil {
		t.Error("没收录的串不应有 ref")
	}
	if RefOf(nil) != nil {
		t.Error("nil 错误不应有 ref")
	}
}

// refusal 是自己拼 Error() 的错误类型(如 service.ScriptStmtError)。
type refusal struct{ err error }

func (r *refusal) Error() string          { return r.err.Error() }
func (r *refusal) MsgRef() *model.RuleRef { return RefOf(r.err) }

func TestRefOfRefer(t *testing.T) {
	const f = "创建失败:%s"
	if !known[Code(f)] {
		t.Skip("样例句子不在文案表里")
	}
	r := &refusal{err: Errorf(f, "x")}
	if got := RefOf(r); got == nil || got.Code != Code(f) {
		t.Fatalf("Refer 应交出 ref:%+v", got)
	}
}

func TestTemplateEscapes(t *testing.T) {
	got := Template(Entry{Zh: "邮箱 {a}@b|c <x> 100%% · %d 行", Fmt: true})
	want := "邮箱 {'{'}a{'}'}{'@'}b{'|'}c {'<'}x{'>'} 100{'%'} · {0} 行"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// 第二批:非报错的话(通知、任务错误、终端输出)用 Sprintf / T / M。
func TestSprintfAndM(t *testing.T) {
	const f = "已执行 %d/%d 条后失败 · 第 %d 条: %s"
	if !known[Code(f)] {
		t.Skip("样例句子不在文案表里")
	}
	inner := T("连接不存在")
	m := Sprintf(f, 2, 5, 3, inner)
	if want := fmt.Sprintf(f, 2, 5, 3, "连接不存在"); m.S != want {
		t.Fatalf("中文变了:%q,应为 %q", m.S, want)
	}
	if m.Ref == nil || m.Ref.Code != Code(f) || len(m.Ref.Parts) != 1 || m.Ref.Parts[0].Code != Code("连接不存在") || m.Ref.Parts[0].Args[Slot] != "3" {
		t.Fatalf("M 参数应嵌成 part:%+v", m.Ref)
	}

	// 没收录的格式串:只有中文,没有 ref
	if u := Sprintf("没收录的 %d", 1); u.S != "没收录的 1" || u.Ref != nil {
		t.Errorf("没收录的格式串不该有 ref:%+v", u)
	}

	// 存库:带参数的存 ref;固定句不存也能按原句补
	if r := StoredRef(m.S, RefJSON(m.Ref)); r == nil || r.Code != Code(f) {
		t.Errorf("存进去的 ref 读不回来:%+v", r)
	}
	if r := StoredRef("连接不存在", ""); r == nil || r.Code != Code("连接不存在") {
		t.Errorf("固定句应按原句补 ref:%+v", r)
	}
	if StoredRef("Error 1045", "") != nil || RefJSON(nil) != "" {
		t.Error("认不出的原文不该有 ref")
	}
	if model.StoredTextRef("连接不存在", "") == "" {
		t.Error("model 的 AfterFind 钩子应已由 apimsg 注入")
	}
}

// 收尾批:多行文本里网关自己的行(LineRef),只收带 ref 的行。
func TestLinesJSON(t *testing.T) {
	if LinesJSON(nil) != "" || LinesJSON([]M{{S: "NOTICE: x"}}) != "" {
		t.Error("没有带 ref 的行时应存空串")
	}
	js := LinesJSON([]M{T("连接不存在"), {S: "NOTICE: x"}})
	if !strings.Contains(js, `"t":"连接不存在"`) || strings.Contains(js, "NOTICE") {
		t.Errorf("LinesJSON 不对:%s", js)
	}
}
