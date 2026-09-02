package bootstrap

// M-5(白盒审计):API 凭据能绑到哪些账号。
//
// 审计报告提的是"可绑任意人类账号(含 admin),成免登录替身"。风险是真的,但**直接
// 禁掉绑真人是过头的**:控制台的创建界面本来就把全部用户列出来供选择(服务账号加
// 🤖 排在前面,真人显示邮箱),把凭据挂在某个负责人名下是这套产品有意提供的用法。
//
// 所以只挡最锋利的那一刀:不能绑平台管理员。下面两条一起看才完整 —— 一条证明能力
// 还在,一条证明那把刀被挡住了。

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 绑普通成员照常可用 —— 修安全问题不能顺手砍掉在用的功能。
func TestAPIClient_CanStillBindAnOrdinaryMember(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	uid := app.userIDByEmail(admin, "chenhao@vela.io") // 真人,持 l2 角色

	r := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "CI 集成", "userId": uid, "scopes": []string{"release:read"},
	})
	eq(t, r.Code, 0, "绑普通成员应当照常可用")
}

// 绑平台管理员必须被拒:那等于把一把能改权限矩阵、建凭据、停用账户的钥匙,变成一个
// 不经登录、不经 MFA、只靠一串密钥的身份。
func TestAPIClient_CannotBindAPlatformAdmin(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	adminUID := app.userIDByEmail(admin, "linwei@vela.io")

	r := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "影子管理员", "userId": adminUID, "scopes": []string{"release:create"},
	})
	if r.Code == 0 {
		t.Fatal("把 API 凭据绑到平台管理员应当被拒 —— 那是一个不经登录也不经 MFA 的管理员身份")
	}
}

// 换绑走同一道闸。只在创建时挡、改绑时放过去,等于没挡。
func TestAPIClient_CannotBeRebountToAnAdmin(t *testing.T) {
	app := newTestApp(t)
	admin := app.login("linwei@vela.io", "vela123")
	ordinary := app.userIDByEmail(admin, "chenhao@vela.io")
	adminUID := app.userIDByEmail(admin, "linwei@vela.io")

	r := app.do(http.MethodPost, "/api/v1/api-clients", admin, map[string]any{
		"name": "先合法后换绑", "userId": ordinary, "scopes": []string{"release:read"},
	})
	eq(t, r.Code, 0, "先建一把合法的")
	var created struct {
		Client struct {
			ID int64 `json:"id"`
		} `json:"client"`
	}
	_ = json.Unmarshal(r.Data, &created)
	if created.Client.ID == 0 {
		t.Fatalf("拿不到凭据 id: %s", r.Data)
	}

	up := app.do(http.MethodPut, "/api/v1/api-clients/"+itoa(created.Client.ID), admin, map[string]any{
		"userId": adminUID,
	})
	if up.Code == 0 {
		t.Error("换绑到管理员应当被拒 —— 否则创建时那道闸形同虚设")
	}
}
