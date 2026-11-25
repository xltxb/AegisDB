package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"

	"velagateway/pkg/resp"
)

type apListView struct {
	Items []struct {
		ApNo     string `json:"apNo"`
		Status   string `json:"status"`
		Instance string `json:"instance"`
		Database string `json:"database"`
	} `json:"items"`
	Total int64 `json:"total"`
}

// decodeApList 把 /approvals 的分页信封拆成上面那个视图。
func decodeApList(t *testing.T, data []byte) apListView {
	t.Helper()
	var out apListView
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode approvals page: %v", err)
	}
	return out
}

func (a *testApp) listApprovals(token, status string) apListView {
	a.t.Helper()
	r := a.do(http.MethodGet, "/api/v1/approvals?scope=all&page=1&pageSize=200&status="+status, token, nil)
	if r.Code != 0 {
		a.t.Fatalf("list approvals status=%q: code=%d msg=%s", status, r.Code, r.Msg)
	}
	return decodeApList(a.t, r.Data)
}

// 按状态取,而不是取一页回去自己筛。
//
// 总览的"待执行"必须靠它:通过之后命令并没有跑,而**通过了的工单不会过期**(超时
// 清扫只作废 pending),所以一张等着执行的单子可以停很久,早就被新工单挤出了任何
// 一页 —— 客户端筛就会漏报,而那张卡片漏报等于没有。
func TestApprovals_FilterByStatus(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	all := app.listApprovals(token, "")
	if all.Total == 0 {
		t.Skip("这套种子里没有工单,过滤无从验证")
	}

	sum := int64(0)
	for _, st := range []string{"pending", "approved", "rejected", "expired"} {
		page := app.listApprovals(token, st)
		sum += page.Total
		for _, it := range page.Items {
			if it.Status != st {
				t.Errorf("status=%s 的结果里混进了 %s (%s)", st, it.Status, it.ApNo)
			}
		}
	}
	// 四个状态互斥且穷尽,加起来就该是不过滤时的全部。少了说明筛掉了不该筛的,
	// 多了说明某张单被算了两遍。
	if sum != all.Total {
		t.Errorf("四种状态合计 %d,不过滤时是 %d", sum, all.Total)
	}
}

// 认不出的状态要当场拒绝,而不是原样进 WHERE 查出一个空列表 —— 空列表和"确实没有"
// 长得一模一样,调用方看不出自己把参数写错了。
func TestApprovals_RejectsUnknownStatus(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")

	r := app.do(http.MethodGet, "/api/v1/approvals?status=whatever", token, nil)
	if r.Code == 0 {
		t.Fatal("拼错的 status 被当成了有效查询")
	}
	if r.Code != resp.CodeBadRequest {
		t.Errorf("应当是参数错误,得到 code=%d msg=%s", r.Code, r.Msg)
	}
}
