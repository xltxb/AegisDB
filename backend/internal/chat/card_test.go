package chat

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// 两种语言的词条必须一一对应:缺一条,那种语言的卡片上就会露出一个键名。
func TestTr_TablesMatch(t *testing.T) {
	if len(texts["zh"]) == 0 {
		t.Fatal("文案表是空的")
	}
	verbs := regexp.MustCompile(`%[a-z]`)
	for key, zh := range texts["zh"] {
		en, ok := texts["en"][key]
		if !ok || en == "" {
			t.Errorf("en 缺 %q", key)
			continue
		}
		if !reflect.DeepEqual(verbs.FindAllString(zh, -1), verbs.FindAllString(en, -1)) {
			t.Errorf("%q 两种语言的占位符不同:%q / %q", key, zh, en)
		}
		if regexp.MustCompile(`\p{Han}`).MatchString(en) {
			t.Errorf("%q 的英文里有汉字:%q", key, en)
		}
	}
	for key := range texts["en"] {
		if _, ok := texts["zh"][key]; !ok {
			t.Errorf("zh 缺 %q", key)
		}
	}
}

func TestTr(t *testing.T) {
	for _, c := range []struct{ lang, key, want string }{
		{"zh", "btn.approve", "通过"},
		{"en", "btn.approve", "Approve"},
		{"", "btn.approve", "通过"},   // 没设语言 = 默认 zh
		{"fr", "btn.approve", "通过"}, // 不认识的语言
		{"en", "no.such.key", "no.such.key"},
	} {
		if got := Tr(c.lang, c.key); got != c.want {
			t.Errorf("Tr(%q, %q) = %q,应为 %q", c.lang, c.key, got, c.want)
		}
	}
	if got := Tr("zh", "outcome.approved", "王芳", "11-25 19:40"); got != "已由 王芳 通过 · 11-25 19:40" {
		t.Errorf("带参数:%q", got)
	}
	// 参数里的 % 是数据,不是格式串。
	if got := Tr("en", "outcome.rejected", "100%d", "x"); got != "Rejected by 100%d · x" {
		t.Errorf("参数里带 %%:%q", got)
	}
}

func TestChannelName(t *testing.T) {
	for ch, want := range map[Channel]string{Lark: "Lark", Telegram: "Telegram", "other": "other"} {
		if got := ch.Name(); got != want {
			t.Errorf("%q.Name() = %q", ch, got)
		}
	}
}

func TestErrGoneSurvivesWrapping(t *testing.T) {
	if !errors.Is(fmt.Errorf("lark 230031: %w", ErrGone), ErrGone) {
		t.Error("渠道包一层原因之后应仍认得出 ErrGone")
	}
}

// 本包与各渠道的实现不认识业务:不引用 service / repository / model,换渠道不动业务,业务变了不动渠道。
func TestPackageStaysBusinessFree(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") {
			continue
		}
		p, err := parser.ParseFile(token.NewFileSet(), f.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, im := range p.Imports {
			for _, bad := range []string{"internal/service", "internal/repository", "internal/model"} {
				if strings.Contains(im.Path.Value, bad) {
					t.Errorf("%s 引用了 %s", f.Name(), im.Path.Value)
				}
			}
		}
	}
	if checked < 3 {
		t.Fatalf("只查了 %d 个文件", checked)
	}
}
