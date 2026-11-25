package bootstrap

// 按从库复制延迟限流(.scratch/osc-product 工单 06)。13307 那台不是从库:把它配成"从库",
// 读出来就是"复制未在运行"—— 正好走"延迟未知就停下等"这条路。

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"velagateway/internal/model"
)

// pointReplicaAtNonReplica 把连接的从库配成测试 MySQL 本身(它不是从库)。
func (a *testApp) pointReplicaAtNonReplica(connID int64) {
	a.t.Helper()
	cfg, err := mysql.ParseDSN(os.Getenv("VELA_OSC_TEST_DSN"))
	if err != nil {
		a.t.Fatal(err)
	}
	host, port, _ := strings.Cut(cfg.Addr, ":")
	p, _ := strconv.Atoi(port)
	if err := a.repo.DB().Model(&model.Connection{}).Where("id = ?", connID).
		Updates(map[string]any{"replica_host": host, "replica_port": p}).Error; err != nil {
		a.t.Fatal(err)
	}
}

func (a *testApp) oscWarningCodes(connID int64) []string {
	a.t.Helper()
	conn, _ := a.repo.GetConnection(connID)
	d := a.svc.OSCRoute(context.Background(), conn, "ALTER TABLE big ADD INDEX idx_a (a)")
	_, ws, err := a.svc.OSCPreflight(context.Background(), conn, d.Schema, d.Table, d.Parsed.Change)
	if err != nil {
		a.t.Fatal(err)
	}
	var codes []string
	for _, w := range ws {
		codes = append(codes, w.Code)
	}
	return codes
}

func hasCode(codes []string, c string) bool {
	for _, x := range codes {
		if x == c {
			return true
		}
	}
	return false
}

func TestOSCReplica_PreflightSaysWhetherLagIsGuarded(t *testing.T) {
	app := newTestApp(t)
	prod, _ := app.oscSetup(50)
	admin := app.login("linwei@vela.io", "vela123")

	if c := app.oscWarningCodes(prod); !hasCode(c, "replica_none") {
		t.Errorf("没配从库应提示不按延迟限流: %v", c)
	}
	app.pointReplicaAtNonReplica(prod)
	if c := app.oscWarningCodes(prod); !hasCode(c, "replica_not_running") {
		t.Errorf("从库复制没在运行应提示: %v", c)
	}
	eq(t, app.do(http.MethodPut, "/api/v1/settings", admin, map[string]any{"osc.maxReplicaLagSec": 0}).Code, 0, "关掉延迟限流")
	for _, c := range app.oscWarningCodes(prod) {
		if strings.HasPrefix(c, "replica_") {
			t.Errorf("阈值为 0 时不该有从库相关提示: %v", c)
		}
	}
}

// 延迟未知:迁移停在限流里不拷贝,原因落库给面板;中止照常可用。
func TestOSCReplica_UnknownLagHoldsTheJob(t *testing.T) {
	app := newTestApp(t)
	prod, raw := app.oscSetup(50)
	app.pointReplicaAtNonReplica(prod)
	j := app.newOSCJob(prod, "ALTER TABLE big ADD INDEX idx_a (a)")
	if err := app.svc.StartOSCJob(j.ID, nil); err != nil {
		t.Fatal(err)
	}
	held := app.waitOSC(j.ID, func(x *model.OSCJob) bool { return x.Throttled && x.ThrottleReason != "" })
	if !strings.Contains(held.ThrottleReason, "从库复制未在运行") || held.RowsCopied != 0 {
		t.Fatalf("应停在限流里、说明原因、没有拷贝: %+v", held)
	}
	if err := app.svc.AbortOSCJob(j.ID); err != nil {
		t.Fatal(err)
	}
	if f := app.waitOSC(j.ID, oscFinished); f.Status != model.OSCAborted {
		t.Fatalf("应可中止: %+v", f)
	}
	if oscHasTable(t, raw, "_big_gho") {
		t.Error("中止后影子表应已清理")
	}
}
