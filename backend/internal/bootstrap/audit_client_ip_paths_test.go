package bootstrap

// 审计行的来源 IP：**每一条认证路径**都要记下来。
//
// 这一组是补票的。IP 这一列(迁移 0041)加上去时只钉了 REST 与登录两条路,而网关有三条
// 认证路径,各自从不同的地方认出"这是谁"：
//
//	REST      JWTAuth 中间件认人
//	WS 终端   握手带不了 Authorization,handler 自己 Parse + GetUserByID
//	开放接口  APIClientAuth 认凭据,再 GetUserByID 取服务账号
//
// 后两条各自从库里取了一份**干净的** user,而来源地址是挂在 user 对象上带进 service 的
// (见 model.User.ClientIP),于是它们写下的审计行那一列全是空 —— 偏偏 WS 终端是跑 SQL
// 用得最多的那条路,生产上的表现就是"登录记了 IP、执行没记"。
//
// 这种漏法不会报错:空在这一列是合法值(0041 之前的老行就是空),没有任何地方会提示。
// 所以这里按**路径**逐条钉,而不是按功能 —— 新加一条认证路径时,这组用例是唯一会因为
// 忘了走 middleware.BindRequestUser 而变红的东西。

import (
	"encoding/json"
	"net/http"
	"testing"
)

// REST 与 WS 终端两条路,写下的审计行都要带来源 IP。
func TestAuditClientIP_RESTAndWebSocketPathsBothRecordIt(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	dev := app.connIDByEnv(admin, "dev")

	// ---- REST(JWTAuth 认人)----
	const restSQL = "SELECT 'via-rest'"
	eq(t, app.do(http.MethodPost, "/api/v1/terminal/exec", admin, map[string]any{
		"connectionId": dev, "sql": restSQL, "reason": "REST 路径",
	}).Code, 0, "REST 执行")

	// ---- WS 终端(握手绕开 JWTAuth,handler 自己认人)----
	const wsSQL = "SELECT 'via-ws'"
	conn := app.dialWS(admin)
	typ := wsExecType(t, conn, dev, wsSQL)
	conn.Close()
	if typ != "output" {
		t.Fatalf("WS 执行返回 %q,期望 output", typ)
	}

	items := app.auditQ(admin, nil).Items
	for _, want := range []string{"login", restSQL, wsSQL} {
		row := rowWithCommand(items, want)
		if row == nil {
			t.Fatalf("审计里没有 %q 这一行", want)
		}
		if row.ClientIP == "" {
			t.Errorf("%q 这一行没有记下来源 IP —— 这条认证路径漏了 BindRequestUser", want)
		}
	}
}

// 开放接口这一路同样要记。外部系统的出口地址正是事后追查最想要的那一格,
// 而对接文档里也是这么承诺的。
func TestAuditClientIP_OpenAPIPathRecordsIt(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@aegisdb.io", "aegisdb123")
	pid := app.createPipeline(admin, "IP 用例流程", "", []map[string]any{
		{"name": "规范审查", "type": "review", "config": `{"failOn":"error"}`},
		{"name": "执行变更", "type": "execute"}, // 流程必须恰好一个执行阶段(工单 02 第 12 条)
	})
	token := app.issueClientPiped(admin, "审计IP用例", "zhangwei@aegisdb.io", nil, pid)

	r := app.openDo(http.MethodPost, "/api/v1/open/releases", token, map[string]any{
		"title": "审计 IP 用例", "externalRef": "AUDIT-IP-1",
		"instance": "sandbox-dev",
		"sql": "CREATE TABLE tbl_ip_probe (\n id BIGINT NOT NULL AUTO_INCREMENT COMMENT 'id',\n" +
			" PRIMARY KEY (id)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='probe';",
		"reason": "开放接口路径",
	})
	if r.Code != 0 {
		t.Fatalf("开放接口建单: code=%d msg=%s", r.Code, r.Msg)
	}
	var created struct {
		RelNo string `json:"relNo"`
	}
	if err := json.Unmarshal(r.Data, &created); err != nil {
		t.Fatalf("解码: %v", err)
	}

	// 建单当场就写了一行审计(service 里那条 intercept 行),actor 是绑定的服务账号,
	// 单号就是这张升级单的号。**只按单号认**:夹具里还躺着一批没有 IP 的历史行(它们
	// 扮演 0041 之前写下的行),匹配写松一点就会先撞上那些,然后报一个假的失败。
	var found bool
	for _, it := range app.auditQ(admin, nil).Items {
		if it.ApprovalNo != created.RelNo {
			continue
		}
		found = true
		if it.ClientIP == "" {
			t.Errorf("开放接口写下的审计行(%q)没有来源 IP —— APIClientAuth 漏了 BindRequestUser",
				clipStr(it.Command, 60))
		}
	}
	if !found {
		t.Fatalf("没找到建单 %s 写下的审计行", created.RelNo)
	}
}
