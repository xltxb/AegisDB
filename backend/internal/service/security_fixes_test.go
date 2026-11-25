package service

import (
	"strings"
	"testing"

	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
)

// MFA 步进验证的宽限期必须绑到**一个会话**上(M-6)。
//
// 原先键里放的是 TokenVersion,而那是用户级的代次:同一个人的每个并发会话共享同一个值。
// 于是受害者在自己浏览器里做过一次生产步进验证之后,一个被盗的 token 在宽限期内(默认
// 30 分钟)对同一台实例也跟着免验 —— 它根本没有证明过持有第二因子。
//
// 这个洞不会报任何错。它的表现是"某次生产操作没有要求输码",而没有人会把这个当成 bug
// 报上来 —— 少一次打扰,只会让人觉得今天顺手。所以它必须有测试。
func TestMFAGraceKeyIsPerSession(t *testing.T) {
	conn := &model.Connection{ID: 7}
	base := &model.User{ID: 1, TokenVersion: 3, SessionID: "sess-aaa"}
	stolen := &model.User{ID: 1, TokenVersion: 3, SessionID: "sess-bbb"} // 同一个人、同一代次、另一个会话

	k1, k2 := mfaGraceKey(base, conn), mfaGraceKey(stolen, conn)
	if k1 == k2 {
		t.Fatalf("两个不同会话算出了同一个宽限键(%q)——被盗 token 会搭上受害者那次验证", k1)
	}
	if !strings.Contains(k1, "sess-aaa") {
		t.Fatalf("宽限键里没有会话标识:%q", k1)
	}
}

// 代次仍然要进键:改密 / 登出 / 角色变更会 bump 它,那一下必须让所有旧条目立刻失配。
func TestMFAGraceKeyStillPinnedToGeneration(t *testing.T) {
	conn := &model.Connection{ID: 7}
	before := mfaGraceKey(&model.User{ID: 1, TokenVersion: 3, SessionID: "s"}, conn)
	after := mfaGraceKey(&model.User{ID: 1, TokenVersion: 4, SessionID: "s"}, conn)
	if before == after {
		t.Fatal("提升令牌代次之后,旧的宽限条目应当立刻失配")
	}
}

// 实例也要进键:在一台生产库上被信任,不说明另一台也该被信任。
func TestMFAGraceKeyIsPerInstance(t *testing.T) {
	u := &model.User{ID: 1, TokenVersion: 3, SessionID: "s"}
	if mfaGraceKey(u, &model.Connection{ID: 1}) == mfaGraceKey(u, &model.Connection{ID: 2}) {
		t.Fatal("不同实例应当各自记各自的宽限期")
	}
}

// 没有会话标识时返回空键,调用方据此**不担保也不记录** —— 失败方向必须是"再验一次"。
//
// 走到这里的是不带 jti 的调用路径(例如开放接口的服务账号)。它们本来就不做步进验证,
// 但这条边界要钉住:将来若有人给这些路径接上 MFA,默认行为是"每次都验",而不是
// "所有人共用一个空键的宽限期" —— 后者会让一个人的验证替所有人担保。
func TestMFAGraceKeyEmptyWithoutSession(t *testing.T) {
	if k := mfaGraceKey(&model.User{ID: 1, TokenVersion: 3}, &model.Connection{ID: 1}); k != "" {
		t.Fatalf("没有会话标识时应当返回空键,实际 %q", k)
	}
}

// 用户目录名不得是 "." 或 ".."(L-16)。
//
// sanitizeFilename 走 filepath.Base,带分隔符的穿越已经没了;但字面的 ".." 过得去,
// 而 Join(savePath, "..") 会把整个上传目录挪到上一级。用户名由管理员设定,所以这是
// 一条"误配置"而非"攻击"路径 —— 代价仍然是文件落在预期之外的地方。
func TestUserDirNameRejectsTraversal(t *testing.T) {
	for _, name := range []string{"..", ".", "  ..  ", "../", "../../etc"} {
		got := userDirName(&model.User{ID: 42, Name: name})
		if got == "." || got == ".." || strings.Contains(got, "/") || strings.Contains(got, "\\") {
			t.Fatalf("用户名 %q 产出了危险的目录名 %q", name, got)
		}
	}
	// 正常名字不受影响 —— 一道把所有人都改名成 user-<id> 的闸,等于把这个功能废掉。
	if got := userDirName(&model.User{ID: 42, Name: "Lin Wei"}); got != "Lin_Wei" {
		t.Fatalf("正常用户名被改坏了:%q", got)
	}
}

// 流水线的备份 / 校验阶段必须拦住写文件子句(M-3)。
//
// 这两个阶段是**唯一不经风险引擎**的执行通道:SQL 由管理员写在流程模板里,之后对着
// 发布单选中的任意实例跑。它们靠一个动词白名单兜底,而 `SELECT … INTO OUTFILE` 的
// 动词**正是 SELECT** —— 白名单放行,`IsRead` 也判它只读,于是一条"只读的校验语句"
// 在数据库服务器上落了一个文件。
//
// 下面第一个断言先把这件事钉死(动词确实是 SELECT,白名单确实兜不住),第二个才是修复
// 本身。少了第一个,后面的人会以为加这道判断是多余的。
func TestPipelineFileWriteClausesAreCaught(t *testing.T) {
	payloads := []string{
		`SELECT * FROM t_user INTO OUTFILE '/var/lib/mysql-files/x.csv'`,
		`SELECT * FROM t_user INTO DUMPFILE '/tmp/x'`,
		`select a,b from t into   outfile '/tmp/y'`,
		`SELECT * FROM t /* 备份 */ INTO OUTFILE '/tmp/z'`,
	}
	for _, sql := range payloads {
		// 1) 动词白名单为什么不够:它们的动词就是 SELECT。
		if v := gateway.ParseVerb(sql); v != "SELECT" {
			t.Fatalf("前提变了:%q 的动词是 %q,不再是 SELECT —— 这条用例的理由需要重写", sql, v)
		}
		// 2) 修复:写文件子句被单独认出来。
		if !gateway.WritesServerFile(sql) {
			t.Fatalf("写文件子句没被拦住:%q", sql)
		}
	}
	// 正常的备份语句不受影响 —— 一道把所有 SELECT 都拒掉的闸会让备份阶段整个不可用。
	for _, ok := range []string{
		`CREATE TABLE bak_t AS SELECT * FROM t`,
		`INSERT INTO bak_t SELECT * FROM t`,
		`SELECT count(1) FROM t`,
	} {
		if gateway.WritesServerFile(ok) {
			t.Fatalf("正常的备份/校验语句被误拦:%q", ok)
		}
	}
}
