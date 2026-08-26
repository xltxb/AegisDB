package service

import (
	"testing"

	"velagateway/internal/model"
)

// applyTargetDatabase 是所有"这次请求打到哪个库"的唯一入口。
//
// 它存在的理由是一次线上故障:Oracle 连接配置的 g04_h01 是 SERVICE NAME,而
// 对象浏览器展开的 G04 是 OWNER。旧代码在十来个地方各自写 conn.Database =
// database,于是属主被写进了服务名,监听器收到 SERVICE_NAME=G04 并回 TNS-12514。
// 规则收敛到一处,才不会下次再有人新写一处漏掉。
func TestApplyTargetDatabase(t *testing.T) {
	t.Run("Oracle:属主不得覆盖服务名", func(t *testing.T) {
		conn := &model.Connection{Engine: "Oracle 19c", Database: "g04_h01"}
		applyTargetDatabase(conn, "G04") // 对象树展开属主时回传的值
		if conn.Database != "g04_h01" {
			t.Errorf("service name 被改成了 %q —— 这正是 TNS-12514 的成因", conn.Database)
		}
	})

	t.Run("Oracle:即便传的是别的服务名也不改", func(t *testing.T) {
		// 目标库的选择框对 Oracle 没有意义(Oracle 靠限定属主而不是切库),
		// 所以这里一律以连接上配置的为准。
		conn := &model.Connection{Engine: "Oracle", Database: "g04_h01"}
		applyTargetDatabase(conn, "ORCLPDB1")
		if conn.Database != "g04_h01" {
			t.Errorf("got %q, want the configured service name", conn.Database)
		}
	})

	t.Run("MySQL:目标库正常切换", func(t *testing.T) {
		conn := &model.Connection{Engine: "MySQL 8.0", Database: "app"}
		applyTargetDatabase(conn, "orders")
		if conn.Database != "orders" {
			t.Errorf("got %q, want orders", conn.Database)
		}
	})

	t.Run("空值是不操作,不是清空", func(t *testing.T) {
		conn := &model.Connection{Engine: "MySQL 8.0", Database: "app"}
		applyTargetDatabase(conn, "   ")
		if conn.Database != "app" {
			t.Errorf("got %q, want the connection's own database untouched", conn.Database)
		}
	})

	t.Run("PostgreSQL:目标库正常切换", func(t *testing.T) {
		conn := &model.Connection{Engine: "PostgreSQL 15", Database: "postgres"}
		applyTargetDatabase(conn, "analytics")
		if conn.Database != "analytics" {
			t.Errorf("got %q, want analytics", conn.Database)
		}
	})
}
