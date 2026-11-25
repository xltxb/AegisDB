package chat

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
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
// 连子目录一起查,本仓库里只许引用 chat 自己。
func TestPackageStaysBusinessFree(t *testing.T) {
	checked, dirs := 0, map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		p, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		dirs[filepath.Dir(path)] = true
		for _, im := range p.Imports {
			to := strings.Trim(im.Path.Value, `"`)
			if strings.HasPrefix(to, "aegisdb/") && to != "aegisdb/internal/chat" && !strings.HasPrefix(to, "aegisdb/internal/chat/") {
				t.Errorf("%s 引用了 %s", path, to)
			}
			// Telegram 只用标准库:少一个依赖,也就少一处会把带令牌的地址写进日志的代码。
			if host, _, _ := strings.Cut(to, "/"); filepath.Dir(path) == "telegram" && strings.Contains(host, ".") {
				t.Errorf("%s 引用了第三方库 %s", path, to)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 3 || !dirs["lark"] || !dirs["telegram"] {
		t.Fatalf("只查了 %d 个文件,目录 %v", checked, dirs)
	}
}
