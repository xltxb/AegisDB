package bootstrap

// 2026-11-23:发布单「人工审批」阶段建的审批单也被推到了审批魔方(飞书里出现
// "[DB_AI] prod/…" 待审批卡片)。改为单独开关 approval.external.releaseEnabled,
// 默认关:发布审批只在控制台 / 飞书卡片里批;打开后才和高危命令一样外发。
// 普通高危命令的外发不受这个开关影响。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestReleaseApproval_ExternalDispatchFollowsReleaseSwitch(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{} // external_task_id(= ApNo)→ 已外发
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in struct {
			ExternalTaskID string `json:"external_task_id"`
		}
		_ = json.Unmarshal(body, &in)
		mu.Lock()
		seen[in.ExternalTaskID] = true
		mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"code":0,"task_id":"cube-` + in.ExternalTaskID + `","status":"PENDING"}`))
	}))
	defer stub.Close()
	dispatched := func(apNo string, wait time.Duration) bool {
		deadline := time.Now().Add(wait)
		for {
			mu.Lock()
			ok := seen[apNo]
			mu.Unlock()
			if ok || time.Now().After(deadline) {
				return ok
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	app.setSettings(admin, map[string]any{
		"approval.external.enabled":         true,
		"approval.external.baseURL":         stub.URL,
		"approval.external.token":           "tok-xyz",
		"approval.external.callbackBaseURL": "https://gw.example",
	})
	stg := app.connIDByEnv(admin, "staging")
	pid := app.createPipeline(admin, "外发开关流程", "", []map[string]any{
		{"name": "人工审批", "type": "approve"},
		{"name": "执行变更", "type": "execute"},
	})
	releaseApNo := func(title string) string {
		t.Helper()
		r := app.submitRelease(admin, map[string]any{
			"title": title, "pipelineId": pid, "connectionId": stg,
			"sql": "SELECT 1;", "reason": "外发开关回归",
		})
		eq(t, r.Code, 0, "submit release "+title)
		var rel releaseView
		_ = json.Unmarshal(r.Data, &rel)
		got := app.waitRelease(admin, rel.ID, "waiting", "failed", "success")
		eq(t, got.Status, "waiting", "发布单停在审批上")
		st := stageByType(got, "approve")
		if st == nil || st.ApprovalNo == "" {
			t.Fatalf("审批阶段应带审批单号,实际 %+v", st)
		}
		return st.ApprovalNo
	}

	// 默认(开关未设):发布审批不外发,普通高危命令照常外发。
	off := releaseApNo("默认不外发")
	hi := app.submitProdHighRisk(admin)
	if !dispatched(hi.ApNo, 5*time.Second) {
		t.Fatalf("普通高危命令 %s 应照常推送审批魔方", hi.ApNo)
	}
	if dispatched(off, 500*time.Millisecond) {
		t.Fatalf("发布审批 %s 在开关关闭时不应推送审批魔方", off)
	}
	if row := app.approvalRow(admin, off); row.ExternalTaskID != "" {
		t.Fatalf("未外发的发布审批不应有 vendor task id,实际 %q", row.ExternalTaskID)
	}

	// 打开开关:发布审批也外发。
	app.setSettings(admin, map[string]any{"approval.external.releaseEnabled": true})
	on := releaseApNo("打开后外发")
	if !dispatched(on, 5*time.Second) {
		t.Fatalf("开关打开后发布审批 %s 应推送审批魔方", on)
	}
}
