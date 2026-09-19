package bootstrap

import (
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

// 单次覆盖是发起人对**这一单**的明确指令,它要落库并进审计 ——
// 一次例外要说得出是谁定的。

func TestRelease_KeepsTheOSCModeTheSubmitterChose(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/releases", token, map[string]any{
		"title": "给订单表加索引", "connectionId": 1, "database": "app",
		"sql": "ALTER TABLE t_order ADD INDEX idx_memo (memo)", "oscMode": "skip",
	})
	if r.Code != resp.CodeOK {
		t.Fatalf("建单失败: code=%d msg=%q", r.Code, r.Msg)
	}

	rel := app.lastRelease(t)
	if rel.OSCMode != "skip" {
		t.Errorf("落库的 oscMode 是 %q,期望 skip", rel.OSCMode)
	}
}

func TestRelease_RejectsAnUnknownOSCMode(t *testing.T) {
	// 拼错的值必须当场被拒,而不是被当成"按策略"静默吞掉:一个以为自己选了
	// "强制直发"的人,会看着一次走了 OSC 的执行不知所以。
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodPost, "/api/v1/releases", token, map[string]any{
		"title": "x", "connectionId": 1, "database": "app",
		"sql": "ALTER TABLE t_order ADD INDEX i (c)", "oscMode": "forse",
	})

	if r.Code == resp.CodeOK {
		t.Error("拼错的 oscMode 被接受了")
	}
}
