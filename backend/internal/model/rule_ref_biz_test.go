package model

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 业务查询拒绝原因的中文有两份:服务端的规范串(RenderBizReason,进 msg 与审计)和前端词条
// (zh.json5 的 bizText,中文界面显示的那句)。两份必须一字不差 —— 同一次拒绝,界面上一句、审计里
// 另一句,追查时没人说得清哪句是真的。这里读前端的真实词条逐条比对;仓库里没有前端(只构建
// 后端)时跳过。
func TestBizReasonMatchesFrontendZh(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "locales", "zh.json5"))
	if err != nil {
		t.Skip("找不到前端词条文件,跳过:", err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	block := regexp.MustCompile(`(?s)\n\s*bizText: \{\n(.*?)\n\s*\},`).FindSubmatch(raw)
	if block == nil {
		t.Fatal("zh.json5 里没有 bizText")
	}
	entry := regexp.MustCompile(`^\s*(\w+): (?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)"),\s*$`)
	placeholder := regexp.MustCompile(`\{(\w+)\}`)
	unescape := strings.NewReplacer(`\\`, `\`, `\'`, `'`, `\"`, `"`)

	seen := 0
	inZh := map[string]bool{}
	defer func() {
		for _, code := range BizReasonCodes {
			if !inZh[code] {
				t.Errorf("%s:前端 zh.json5 的 bizText 里没有这个词条", code)
			}
			if RenderBizReason(NewRuleRef(code, "n", "1", "err", "x")) == "" {
				t.Errorf("%s:RenderBizReason 没有规范中文串", code)
			}
		}
	}()
	for _, line := range strings.Split(string(block[1]), "\n") {
		m := entry.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		code, tmpl := m[1], unescape.Replace(m[2]+m[3])
		inZh[code] = true
		var kv []string
		want := placeholder.ReplaceAllStringFunc(tmpl, func(p string) string {
			name := p[1 : len(p)-1]
			kv = append(kv, name, "<"+name+">")
			return "<" + name + ">"
		})
		got := RenderBizReason(NewRuleRef(code, kv...))
		if got != want {
			t.Errorf("%s:\n服务端 %q\n前端   %q", code, got, want)
		}
		seen++
	}
	if seen < 20 {
		t.Errorf("只比对了 %d 条,词条解析可能出错", seen)
	}
}
