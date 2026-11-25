package model

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// 审批描述(apText)与发布预检(preText)的中文同样有两份:服务端的规范串(进审批单 / 发布单)
// 和前端 zh.json5 的词条(中文界面显示的那句)。两份必须一字不差,理由与 bizText 相同 ——
// 见 TestBizReasonMatchesFrontendZh。{inner} 是嵌套 ref 渲染出的片段,这里用一个固定的子 ref 代入。
func TestApTextMatchesFrontendZh(t *testing.T) {
	checkTextBlock(t, "apText", ApTextCodes, RenderApText, RuleRef{Code: WinOnceUnset})
}

func TestPrecheckMatchesFrontendZh(t *testing.T) {
	checkTextBlock(t, "preText", PrecheckCodes, RenderPrecheck, RuleRef{Code: PreOpRename})
}

// 发布阶段日志(rlLog):raw / oscItem 只是把参数原样放出来,没参数时为空是对的。
func TestRelLogMatchesFrontendZh(t *testing.T) {
	checkTextBlock(t, "rlLog", RelLogCodes, RenderRelLog, RuleRef{Code: "oscPh_copying"}, RelLogRaw, RelLogOSCItem)
}

func checkTextBlock(t *testing.T, ns string, codes []string, render func(*RuleRef) string, child RuleRef, emptyOK ...string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "locales", "zh.json5"))
	if err != nil {
		t.Skip("找不到前端词条文件,跳过:", err)
	}
	src := strings.ReplaceAll(string(raw), "\r\n", "\n")
	block := regexp.MustCompile(`(?s)\n\s*` + ns + `: \{\n(.*?)\n\s*\},`).FindStringSubmatch(src)
	if block == nil {
		t.Fatalf("zh.json5 里没有 %s", ns)
	}
	entry := regexp.MustCompile(`^\s*(\w+): (?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)"),\s*$`)
	placeholder := regexp.MustCompile(`\{(\w+)\}`)
	unescape := strings.NewReplacer(`\\`, `\`, `\'`, `'`, `\"`, `"`)
	// 消息语法的特殊字符在词条里写成 {'@'} 这种字面量插值;比对前还原成字符本身。
	literal := regexp.MustCompile(`\{'([^']*)'\}`)

	inZh := map[string]bool{}
	for _, line := range strings.Split(block[1], "\n") {
		m := entry.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		code, tmpl := m[1], literal.ReplaceAllString(unescape.Replace(m[2]+m[3]), "$1")
		inZh[code] = true
		ref := &RuleRef{Code: code}
		var kv []string
		want := placeholder.ReplaceAllStringFunc(tmpl, func(p string) string {
			name := p[1 : len(p)-1]
			if name == "inner" {
				ref.Parts = []RuleRef{child}
				return render(&child)
			}
			kv = append(kv, name, "<"+name+">")
			return "<" + name + ">"
		})
		ref.Args = NewRuleRef(code, kv...).Args
		if got := render(ref); got != want {
			t.Errorf("%s.%s:\n服务端 %q\n前端   %q", ns, code, got, want)
		}
	}
	for _, code := range codes {
		if !inZh[code] {
			t.Errorf("%s:前端 zh.json5 的 %s 里没有这个词条", code, ns)
		}
		if render(&RuleRef{Code: code, Parts: []RuleRef{child}}) == "" && code != PreSyntaxError && !slices.Contains(emptyOK, code) {
			t.Errorf("%s:没有规范中文串", code)
		}
	}
	if len(inZh) < len(codes) {
		t.Errorf("%s 只解析出 %d 条,少于 %d 个 code,词条解析可能出错", ns, len(inZh), len(codes))
	}
}
