package gateway

// 连通性探测的**类型化**结论。
//
// ## 为什么不能只回 (bool, string)
//
// 一次探测有五种结局,它们要求的下一步动作完全不同:
//
//	OK             没事
//	Unreachable    网络/地址的问题 —— 可以再试,恢复了就该知道
//	AuthFailed     凭据的问题 —— **不能**自动再试:Oracle 默认失败 10 次锁账号
//	NoCredentials  网关这边没配凭据 —— 根本没有远端可探,这不是远端的状况
//	Unsupported    连接上的引擎网关已不能驱动 —— 同样不是远端的状况
//
// 压成一个 bool 之后,调用方只能靠猜:把"没配凭据"记成"连不上"是把我们的配置问题说成
// 远端坏了;拿错误文本的子串去认"认证失败"会把 PostgreSQL 的 permission denied(登录
// 成功之后的授权错误)也认进去,于是一台连得上的实例被自动巡检永久跳过。
//
// 所以分类在**这里**做 —— 驱动错误在这一层还是带类型的(MySQL 的 Number、pq 的 Code、
// go-ora 的 ErrCode),出了这层就只剩一串文字了。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	"github.com/sijms/go-ora/v2/network"

	"aegisdb/internal/model"
)

// CheckKind 是探测结论的种类。它会原样存进 tbl_connection.last_check_kind。
type CheckKind string

const (
	CheckOK            CheckKind = "ok"
	CheckUnreachable   CheckKind = "unreachable"
	CheckAuthFailed    CheckKind = "auth"
	CheckNoCredentials CheckKind = "nocred"
	// CheckUnsupported:连接上存的引擎网关已不能驱动(不再支持的引擎留下的存量连接)。
	// 与 NoCredentials 一样不是远端的状况,不记进巡检结论。
	CheckUnsupported CheckKind = "unsupported"
)

// CheckResult 是一次探测的结论。Msg 是给人看的原话(失败时是驱动的错误文本)。
type CheckResult struct {
	Kind CheckKind
	Msg  string
}

// OK 说明连上了。
func (r CheckResult) OK() bool { return r.Kind == CheckOK }

// Check 探测一台实例。
//
// 与 RealPing 同一条路径(openConn + PingContext),只是把结局分了类。RealPing 保留
// (bool, string) 的形状给还没改过来的调用方。
func Check(conn *model.Connection) CheckResult {
	if err := UnsupportedEngineErr(conn.Engine); err != nil {
		return CheckResult{Kind: CheckUnsupported, Msg: err.Error()}
	}
	if !RealExecSupported(conn) {
		// 没有远端可探。这**不是**"连不上":连不上说的是远端,而这里缺的是我们的凭据。
		return CheckResult{Kind: CheckNoCredentials, Msg: "未配置真实执行凭据,无法验证可连性"}
	}
	fresh := !poolCached(conn)
	db, release, err := openConn(conn)
	if err != nil {
		return CheckResult{Kind: classifyDialError(err), Msg: err.Error()}
	}
	defer release()
	// 刚拨出来的池已经在 openAndPing 里 ping 过,不再 ping 第二次;缓存里拿到的池才要
	// 问一句"现在还连得上吗" —— 那是缓存命中时唯一没被回答的问题。
	if fresh {
		return CheckResult{Kind: CheckOK, Msg: fmt.Sprintf("%s %s · 连接正常", conn.Engine, conn.Host)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		err = oracleHint(err)
		return CheckResult{Kind: classifyDialError(err), Msg: err.Error()}
	}
	return CheckResult{Kind: CheckOK, Msg: fmt.Sprintf("%s %s · 连接正常", conn.Engine, conn.Host)}
}

// classifyDialError 把拨号/认证错误分成"认证失败"与"连不上"。
//
// 先按驱动的**类型化错误码**认,认不出再看几条措辞 —— 后者只是兜底,而且刻意窄:
// 宁可漏认(后果是多试几次,回到原样)也不要错认(后果是一台连得上的实例被永久跳过)。
//
// 注意 PostgreSQL 的 42501 permission denied **不在**这里:那是登录成功之后的授权错误,
// 重试不会锁账号,把它当认证失败会让一台好实例挂着越来越旧的"连不上"。
func classifyDialError(err error) CheckKind {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1044, 1045, 1698: // access denied to database / for user / plugin auth
			return CheckAuthFailed
		}
		return CheckUnreachable
	}
	var pe *pq.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case "28P01", "28000": // invalid_password / invalid_authorization_specification
			return CheckAuthFailed
		}
		return CheckUnreachable
	}
	var oe *network.OracleError
	if errors.As(err, &oe) {
		switch oe.ErrCode {
		case 1017, 28000: // invalid username/password / account locked
			return CheckAuthFailed
		}
		return CheckUnreachable
	}
	// 兜底:驱动没给类型(或被包装丢了)时看措辞。
	m := strings.ToLower(err.Error())
	for _, sig := range []string{
		"access denied for user",
		"ora-01017", "ora-28000",
		"password authentication failed",
		"authentication failed",
	} {
		if strings.Contains(m, sig) {
			return CheckAuthFailed
		}
	}
	return CheckUnreachable
}
