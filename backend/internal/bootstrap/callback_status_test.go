package bootstrap

// 回调被拒时,厂商要**看得出**被拒了。
//
// 「一律 HTTP 200,业务码在信封里」是对**我们自己的前端**定的约定 —— 它读 code 字段。
// 而飞书回调的消费者是外部系统,它按 HTTP 状态码判断成败:一律 200 意味着一次被拒的
// 回调在对面看起来像成功 —— 不重试、不告警,而那次审批就这么丢了。
//
// 代码里那句注释自己承认了这件事:「The vendor only sees HTTP 200, so this line is how
// you spot a silently-rejected callback」—— 日志是唯一能发现它的地方,而没有人会去盯
// 一个"成功了"的回调的日志。
//
// 这一条直接决定 #9 的取舍站不站得住:那次把「未派发的单」改成拒绝,理由是「厂商重试
// 即可」。厂商收到 200 是不会重试的。
//
// 信封照旧带业务码(前端与调试都在读它),变的只是外层状态码。

import (
	"net/http"
	"testing"
)

func TestExternalCallback_RejectionsCarryARealHTTPStatus(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"approval.external.enabled":        true,
		"approval.external.callbackSecret": "s3cr3t",
	})
	ap := app.submitProdHighRisk(token)
	app.markExternallyDispatched(ap.ApNo, "vt-"+ap.ApNo)

	for _, c := range []struct {
		name       string
		secret     string
		body       map[string]any
		wantStatus int
	}{
		{"密钥不对", "wrong",
			map[string]any{"external_task_id": ap.ApNo, "task_id": "vt-" + ap.ApNo, "approved": true},
			http.StatusForbidden},
		{"没带密钥", "",
			map[string]any{"external_task_id": ap.ApNo, "task_id": "vt-" + ap.ApNo, "approved": true},
			http.StatusForbidden},
		{"单据不存在", "s3cr3t",
			map[string]any{"external_task_id": "AP-doesnotexist", "task_id": "vt-x", "approved": true},
			http.StatusNotFound},
		{"厂商任务号对不上", "s3cr3t",
			map[string]any{"external_task_id": ap.ApNo, "task_id": "another-task", "approved": true,
				"approver": []string{"herbert@tbu.net"}},
			http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, status := app.postLarkCallback(c.secret, c.body)
			if status != c.wantStatus {
				t.Errorf("回调被拒却回了 HTTP %d(期望 %d)—— 厂商看不出失败,不会重试,"+
					"那次审批就丢了", status, c.wantStatus)
			}
			if env.Code == 0 {
				t.Error("信封里的业务码仍然要说明是哪一种失败")
			}
		})
	}
}

// 成功那一条不变:200 + code 0。
func TestExternalCallback_SuccessStaysTwoHundred(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	app.setSettings(token, map[string]any{
		"approval.external.enabled":        true,
		"approval.external.callbackSecret": "s3cr3t",
	})
	ap := app.submitProdHighRisk(token)
	app.markExternallyDispatched(ap.ApNo, "vt-"+ap.ApNo)

	env, status := app.postLarkCallback("s3cr3t", map[string]any{
		"external_task_id": ap.ApNo, "task_id": "vt-" + ap.ApNo, "approved": true,
		"approver": []string{"herbert@tbu.net"},
	})
	eq(t, status, http.StatusOK, "成功的回调仍然是 200")
	eq(t, env.Code, 0, "业务码 0")
}
