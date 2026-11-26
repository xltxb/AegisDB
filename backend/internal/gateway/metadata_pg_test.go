package gateway

// PG 家族没配库名时,要去枚举这台实例上的库,而不是拒绝。
//
// 这组用例是被一次生产日志换来的:
//
//	{"level":"WARN","msg":"metadata sync failed","conn":"dws-digiplus-prod",
//	 "err":"PostgreSQL 家族需要在连接上指定数据库才能探查元数据"}
//
// 两台 DWS 生产实例的连接没填库名,于是定时同步一条记录都收不到。而**凭据、网络、
// 默认库其实全是通的** —— RealMetadata 是先 openConn(它 ping 过)再分派的,那句拒绝
// 发生在 ping 成功之后。树在同样的情况下会列出服务器上的库(RealSchema 的 pg 分支),
// 只有同步这一条路把自己挡住了,而原来的注释还写着"与树的浏览口径一致"。
//
// 这里用一个对任何查询都报错的桩驱动,所以钉的不是"读到了什么",而是**它去做了哪件事**:
// 拿到一个不认识 pg_database 的库时,错必须来自那条枚举查询,而不是那句拒绝。
// 真机验收仍然欠着(见 issue 24)—— 这条用例挡的是"有人把 guard 加回来"。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"aegisdb/internal/model"
)

// notPGDriver 是"连得上、但不是 PostgreSQL"的桩:能建连接,任何查询都报"关系不存在"
// (就像在一个没有 pg_database / pg_class 的库上那样)。
type notPGDriver struct{}

func (notPGDriver) Open(string) (driver.Conn, error) { return notPGConn{}, nil }

type notPGConn struct{}

func (notPGConn) Prepare(q string) (driver.Stmt, error) {
	return nil, errors.New(`relation does not exist: ` + q)
}
func (notPGConn) Close() error              { return nil }
func (notPGConn) Begin() (driver.Tx, error) { return nil, errors.New("no tx") }

func init() { sql.Register("aegis-notpg", notPGDriver{}) }

func notPGHandle(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("aegis-notpg", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRealMetadataPG_NoDatabaseEnumeratesInsteadOfRefusing(t *testing.T) {
	conn := &model.Connection{Engine: "dws", Database: "", Host: "10.0.0.1", Port: 8000, Username: "u", Password: "p"}
	_, _, err := realMetadataPG(context.Background(), notPGHandle(t), conn)
	if err == nil {
		t.Fatal("拿非 PG 的库当 PG 探查不该成功")
	}
	if strings.Contains(err.Error(), "需要在连接上指定数据库") {
		t.Fatalf("那道拒绝被加回来了 —— 没配库名时应当去枚举实例上的库:%v", err)
	}
	if !strings.Contains(err.Error(), "列出实例上的库失败") {
		t.Fatalf("错误应当来自枚举那一步,实际是:%v", err)
	}
}

// 配了库名就只探那个库 —— 没配才枚举。两条路都要在,少一条就是另一种行为改变。
func TestRealMetadataPG_WithDatabaseProbesThatOneOnly(t *testing.T) {
	conn := &model.Connection{Engine: "dws", Database: "appdb"}
	_, _, err := realMetadataPG(context.Background(), notPGHandle(t), conn)
	if err == nil {
		t.Fatal("拿非 PG 的库当 PG 探查不该成功")
	}
	// 配了库名时不该去列库:错必须来自表清单那条查询(pg_class),不是 pg_database。
	if strings.Contains(err.Error(), "列出实例上的库失败") {
		t.Fatalf("配了库名还去枚举整台实例:%v", err)
	}
}

// 不完整不是失败:同步侧靠 errors.As 认出它,然后**收下已经拿到的那部分**。
// 类型一改(或改成值接收者),那个判断会静静地失配,于是一台实例只要有一个库没权限,
// 其余几十个库的结构就全被丢掉 —— 拿到了却扔掉,比没拿到更糟。
func TestMetaIncompleteError_IsRecognisedByErrorsAs(t *testing.T) {
	var err error = &MetaIncompleteError{Reason: "2 个库没能读取:a, b"}
	var target *MetaIncompleteError
	if !errors.As(err, &target) {
		t.Fatal("errors.As 认不出 MetaIncompleteError")
	}
	if !strings.Contains(target.Error(), "没能读取") {
		t.Fatalf("原因没带出来:%q", target.Error())
	}
	// 它和截断是两回事,不能互相冒充。
	if errors.Is(err, ErrMetaTruncated) {
		t.Fatal("不完整不该被当成截断")
	}
}
