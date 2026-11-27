package service

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// 回调带回登录页的失败原因,前端逐项翻成人话;两边对不上时,新原因会被前端当成"换取失败"(#186)。
func TestOIDCErrReasonsMatchFrontend(t *testing.T) {
	raw, err := os.ReadFile("../../../frontend/src/lib/ssoLogin.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`SSO_ERRORS = \[([^\]]*)\]`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("ssoLogin.ts 里找不到 SSO_ERRORS")
	}
	var front []string
	for _, s := range strings.Split(m[1], ",") {
		front = append(front, strings.Trim(strings.TrimSpace(s), `'"`))
	}
	if !slices.Equal(front, OIDCErrReasons) {
		t.Fatalf("前端 SSO_ERRORS = %v,后端 OIDCErrReasons = %v", front, OIDCErrReasons)
	}
}
