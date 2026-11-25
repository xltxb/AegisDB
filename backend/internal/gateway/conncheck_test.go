package gateway

// 拨号错误的分类。
//
// 这组用例钉的是**认错的方向**:
//
//   把授权错误认成认证失败 → 一台连得上的实例被自动巡检永久跳过(最坏的错法);
//   把认证失败认成连不上   → 多拨几次,回到没有这层防护时的样子(可以接受的错法)。
//
// 所以正例(该认出的认证失败)和反例(不该认出的授权/网络错误)都要钉。

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	"github.com/sijms/go-ora/v2/network"

	"velagateway/internal/model"
)

func TestClassifyDialError_TypedAuthCodesAreAuth(t *testing.T) {
	cases := []error{
		&mysql.MySQLError{Number: 1045, Message: "Access denied for user 'gw'@'10.1.2.3'"},
		&mysql.MySQLError{Number: 1044, Message: "Access denied for user to database 'x'"},
		&mysql.MySQLError{Number: 1698, Message: "Access denied (auth plugin)"},
		&pq.Error{Code: "28P01", Message: "password authentication failed for user \"gw\""},
		&pq.Error{Code: "28000", Message: "no pg_hba.conf entry for host"},
		&network.OracleError{ErrCode: 1017, ErrMsg: "invalid username/password; logon denied"},
		&network.OracleError{ErrCode: 28000, ErrMsg: "the account is locked"},
		// 被包装过也要认得出 —— errors.As 会穿透。
		fmt.Errorf("dial: %w", &mysql.MySQLError{Number: 1045, Message: "Access denied"}),
	}
	for _, err := range cases {
		if got := classifyDialError(err); got != CheckAuthFailed {
			t.Errorf("%v:应当是 auth,实际 %q", err, got)
		}
	}
}

// PostgreSQL 的 permission denied 是**登录成功之后**的授权错误(42501),重试不会锁账号。
// 把它当认证失败,是审查时抓到的那条误判:一台连得上的实例会被自动巡检永久跳过。
func TestClassifyDialError_AuthorizationIsNotAuthentication(t *testing.T) {
	cases := []error{
		&pq.Error{Code: "42501", Message: "permission denied for database orders"},
		errors.New("pq: permission denied for database orders"),
		errors.New("permission denied for schema public"),
	}
	for _, err := range cases {
		if got := classifyDialError(err); got == CheckAuthFailed {
			t.Errorf("%v:授权错误不该被当成认证失败", err)
		}
	}
}

func TestClassifyDialError_NetworkAndUnknownAreUnreachable(t *testing.T) {
	cases := []error{
		errors.New("dial tcp 10.0.0.2:3306: i/o timeout"),
		errors.New("dial tcp: connect: connection refused"),
		&mysql.MySQLError{Number: 2003, Message: "Can't connect to MySQL server"},
		&pq.Error{Code: "3D000", Message: "database \"x\" does not exist"},
		&network.OracleError{ErrCode: 12514, ErrMsg: "listener does not currently know of service"},
		errors.New("Oracle 未指定目标库:请在「数据库名」填写服务名"),
	}
	for _, err := range cases {
		if got := classifyDialError(err); got != CheckUnreachable {
			t.Errorf("%v:应当是 unreachable,实际 %q", err, got)
		}
	}
}

// 没配凭据是独立的一档:它不是远端的状况。
func TestCheck_NoCredentialsIsItsOwnKind(t *testing.T) {
	// 有地址、没用户名 —— 就是表单里允许建出来的那种。
	r := Check(&model.Connection{Engine: "MySQL", Host: "10.0.0.1", Port: 3306})
	if r.Kind != CheckNoCredentials {
		t.Fatalf("应当是 nocred,实际 %q", r.Kind)
	}
	if r.OK() {
		t.Error("没凭据不算通")
	}
}
