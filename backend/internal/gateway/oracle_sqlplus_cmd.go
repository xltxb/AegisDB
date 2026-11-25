package gateway

// SQL*Plus 的客户端命令表(2026-10 sqlplus-compat 工单 01 / 03,见 .scratch/sqlplus-compat)。
//
// oracle_sqlplus.go 管"能翻译成 SQL 的"(SHOW USER、DESC、EXEC);这里管**不能翻译**的那些:
//
//   拒绝     会绕开网关的命令。CONNECT 换成另一个数据库账号 —— 平台的授权、审计都挂在
//            登记的那条连接上,换了账号这些就全落空了;HOST 在网关服务器上跑系统命令;
//            STARTUP / SHUTDOWN 起停实例 …… 不下发,说清为什么。
//   忽略     暂不支持的报表命令(BREAK / COMPUTE / TTITLE …),以及只在交互终端里有意义的
//            命令(DEFINE / ACCEPT / TIMING / COLUMN …)从脚本、审批单、发布单这些通道过来时:
//            不下发、回一句"已忽略" —— 和 SET 一样,一行显示命令不该让整个脚本失败。
//   PROMPT   原样回话(脚本里逐条输出就能看到)。
//   REMARK   注释,忽略。
//
// 判定(EvaluateFor)与执行器(Executor.Run)用**同一个**识别函数:判定放行的,执行器一定
// 不会把它发给数据库;执行器会发的,判定一定按 SQL 判过。
//
// 只认整条语句以这些词开头(切分器已经让它们按行结束,见 pkg/sqlutil/sqlplus.go),
// 缩写按 SQL*Plus 的最短写法。Oracle 的 SQL 没有以这些词开头的语句,所以不会误伤
// SQL;其他引擎一律不走这里。

import (
	"context"
	"strings"
)

// SQLPlusKind 是一条 SQL*Plus 客户端命令的处置方式。
type SQLPlusKind int

const (
	SQLPlusNone    SQLPlusKind = iota // 不是这里管的命令
	SQLPlusRefused                    // 拒绝:会绕开网关
	SQLPlusIgnored                    // 不下发,回"已忽略"
	SQLPlusPrompt                     // PROMPT:原样回话
	SQLPlusRemark                     // REMARK:注释
	SQLPlusExit                       // EXIT / QUIT:脚本在此正常结束(交互终端里由前端拒绝)
	SQLPlusWhenever                   // WHENEVER SQLERROR …:改变脚本里后面语句出错时的走法
	SQLPlusVariable                   // VARIABLE name type:声明绑定变量(oracle_binds.go)
	SQLPlusPrint                      // PRINT [name …]:打印绑定变量
)

// SQLPlusCmd 是识别结果。Name 是命令的全称(大写),Text 是 PROMPT 后面的文字。
type SQLPlusCmd struct {
	Kind   SQLPlusKind
	Name   string
	Text   string
	Reason string // 拒绝的原因(规范中文串)
}

type sqlplusWord struct {
	full, min string // 全称、最短缩写
	kind      SQLPlusKind
	reason    string
}

// 拒绝的原因要让人知道该怎么办,不只是"不支持"。
const (
	whyAccount  = "会换成另一个数据库账号,绕过平台的授权与审计;请在左侧选择平台里登记的连接"
	whyHost     = "会在网关服务器上执行操作系统命令,网关不允许"
	whyInstance = "起停 / 恢复实例要以 SYSDBA 登录数据库服务器操作,网关不提供"
	whyPassword = "修改数据库账号口令请联系 DBA 在平台的连接管理里处理"
	whyCopy     = "跨库搬数据会绕过平台的导出与审批;请用平台的数据导出 / 工单"
	whyEdit     = "网关终端没有本地编辑器;直接在终端里修改后重新执行即可"
	whyNoSuch   = "网关终端不支持这条 SQL*Plus 命令"
	whyAt       = "引用的脚本要在 Web 终端里用 @ 命令运行(连同被引用的文件一起上传),网关会把它们展开成一份脚本再判定"
)

var sqlplusWords = []sqlplusWord{
	{"CONNECT", "CONN", SQLPlusRefused, whyAccount},
	{"DISCONNECT", "DISC", SQLPlusRefused, whyAccount},
	{"HOST", "HO", SQLPlusRefused, whyHost},
	{"STARTUP", "STARTUP", SQLPlusRefused, whyInstance},
	{"SHUTDOWN", "SHUTDOWN", SQLPlusRefused, whyInstance},
	{"RECOVER", "RECOVER", SQLPlusRefused, whyInstance},
	{"PASSWORD", "PASSW", SQLPlusRefused, whyPassword},
	{"COPY", "COPY", SQLPlusRefused, whyCopy},
	{"EDIT", "ED", SQLPlusRefused, whyEdit},
	// 脚本末尾的 EXIT 很常见:到这里脚本正常结束(见 ScriptFlow),不是错误
	{"EXIT", "EXIT", SQLPlusExit, ""},
	{"QUIT", "QUIT", SQLPlusExit, ""},
	{"WHENEVER", "WHENEVER", SQLPlusWhenever, ""},
	// @ / START 嵌套的文件网关拿不到;终端的 @ 命令会先在本地展开(sqlplus-compat 08)
	{"START", "STA", SQLPlusRefused, whyAt},
	{"XQUERY", "XQUERY", SQLPlusRefused, whyNoSuch},
	{"ATTRIBUTE", "ATTRIBUTE", SQLPlusRefused, whyNoSuch},
	{"STORE", "STORE", SQLPlusRefused, whyNoSuch},

	// 暂不支持的报表命令
	{"BREAK", "BRE", SQLPlusIgnored, ""},
	{"COMPUTE", "COMP", SQLPlusIgnored, ""},
	{"TTITLE", "TTI", SQLPlusIgnored, ""},
	{"BTITLE", "BTI", SQLPlusIgnored, ""},
	{"REPHEADER", "REPH", SQLPlusIgnored, ""},
	{"REPFOOTER", "REPF", SQLPlusIgnored, ""},
	{"PAUSE", "PAU", SQLPlusIgnored, ""},
	// 交互终端里的命令:到了脚本 / 审批单 / 发布单这些通道就只能忽略
	{"COLUMN", "COL", SQLPlusIgnored, ""},
	{"CLEAR", "CL", SQLPlusIgnored, ""},
	{"DEFINE", "DEF", SQLPlusIgnored, ""},
	{"UNDEFINE", "UNDEF", SQLPlusIgnored, ""},
	{"ACCEPT", "ACC", SQLPlusIgnored, ""},
	{"TIMING", "TIMI", SQLPlusIgnored, ""},
	{"SPOOL", "SPO", SQLPlusIgnored, ""},
	// 行编辑器与 HELP(sqlplus-compat 11):只在交互终端里有意义。单字母缩写只认整条就是这个词
	// (leadingWord 要求后面是空白 / 分号 / 结尾),Oracle 的 SQL 没有以 L / A / C / I 开头的语句
	{"LIST", "L", SQLPlusIgnored, ""},
	{"APPEND", "A", SQLPlusIgnored, ""},
	{"CHANGE", "C", SQLPlusIgnored, ""},
	{"DEL", "DEL", SQLPlusIgnored, ""},
	{"INPUT", "I", SQLPlusIgnored, ""},
	{"SAVE", "SAV", SQLPlusIgnored, ""},
	{"GET", "GET", SQLPlusIgnored, ""},
	{"HELP", "HELP", SQLPlusIgnored, ""},
	{"VARIABLE", "VAR", SQLPlusVariable, ""},
	{"PRINT", "PRI", SQLPlusPrint, ""},

	{"PROMPT", "PRO", SQLPlusPrompt, ""},
	{"REMARK", "REM", SQLPlusRemark, ""},
}

// OracleClientCommand 识别 Oracle 实例上一条不能翻译成 SQL 的 SQL*Plus 命令。
func OracleClientCommand(engine, stmt string) (SQLPlusCmd, bool) {
	if engineFamily(engine) != familyOracle {
		return SQLPlusCmd{}, false
	}
	// 先剥注释再认,与判定看到的文本同一个口径(SQLPlusSet 也是这样):批量执行时判定拿的是
	// 去掉可执行注释标记的文本、执行器拿的是保留标记的文本。两边如果一个剥一个不剥,
	// `/*!PROMPT*/ DROP TABLE t` 会被判定当成 PROMPT 放行,而执行器把它当 SQL 发下去。
	s := strings.TrimSpace(StripComments(stmt))
	if s == "" {
		return SQLPlusCmd{}, false
	}
	// HOST 的两个符号写法:`!ls`、`$dir`。Oracle SQL 没有以它们开头的语句。
	if s[0] == '!' || s[0] == '$' {
		return SQLPlusCmd{Kind: SQLPlusRefused, Name: "HOST", Reason: whyHost}, true
	}
	if s[0] == '@' {
		return SQLPlusCmd{Kind: SQLPlusRefused, Name: "@", Reason: whyAt}, true
	}
	word := strings.ToUpper(leadingWord(s))
	if word == "" {
		return SQLPlusCmd{}, false
	}
	rest := strings.TrimSpace(s[len(word):])
	// ARCHIVE LOG 是两个词
	if word == "ARCHIVE" && strings.HasPrefix(strings.ToUpper(rest), "LOG") {
		return SQLPlusCmd{Kind: SQLPlusRefused, Name: "ARCHIVE LOG", Reason: whyInstance}, true
	}
	// SHOW ERRORS 不带对象:上一条 CREATE 的编译错误已经随那条语句报出(oracle_create_errors.go);
	// 交互终端会自己补上对象名再发。带对象的那种由 OracleSQLPlus 翻译成查询。
	if word == "SHOW" && oraShowErrBareRe.MatchString(s) {
		return SQLPlusCmd{Kind: SQLPlusIgnored, Name: "SHOW ERRORS"}, true
	}
	for _, w := range sqlplusWords {
		if len(word) < len(w.min) || !strings.HasPrefix(w.full, word) {
			continue
		}
		c := SQLPlusCmd{Kind: w.kind, Name: w.full, Reason: w.reason}
		if w.kind == SQLPlusPrompt || w.kind == SQLPlusWhenever || w.kind == SQLPlusVariable || w.kind == SQLPlusPrint {
			c.Text = rest
		}
		return c, true
	}
	return SQLPlusCmd{}, false
}

// leadingWord 是开头那一串字母。
func leadingWord(s string) string {
	i := 0
	for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		i++
	}
	// 后面紧跟的必须是空白、分号或结尾:`HOSTNAME` 不是 HOST 的变体,`CONN/` 也不是一个词
	if i < len(s) && !isSpaceByte(s[i]) && s[i] != ';' {
		return ""
	}
	return s[:i]
}

// SQLPlusRefusedNotice 是拒绝时的回话(以 · 开头:提示,不是执行结果)。
func SQLPlusRefusedNotice(c SQLPlusCmd) string {
	return "· " + c.Name + " 是 SQL*Plus 命令,网关不支持:" + c.Reason
}

// ScriptFlow 是一份脚本按顺序执行时,SQL*Plus 的 WHENEVER / EXIT 对流程的影响(sqlplus-compat 07)。
//
// 网关的默认是**遇错即停**(与平台一直以来的行为一致,不是 SQL*Plus 的默认 CONTINUE):脚本里
// 显式写了 WHENEVER SQLERROR CONTINUE,后面的语句出错才接着跑;WHENEVER SQLERROR EXIT 又改回
// 停下。EXIT / QUIT 让脚本在那里正常结束。
//
// EXIT 后面的 COMMIT / ROLLBACK 能认、不生效:网关每条语句执行完即提交,没有可回滚的东西。
// OSERROR 同理:网关里没有操作系统命令,它不会被触发。
//
// 所有按顺序跑脚本的地方共用它(终端批量、\i / @ 脚本、审批单执行脚本、发布流水线),
// 同一份脚本在哪条通道上跑,走法都一样。
type ScriptFlow struct {
	engine string
	cont   bool
	binds  []Bind // 脚本里 VARIABLE 声明的绑定变量与当前值
}

// NewScriptFlow 开始一份脚本。非 Oracle 实例上它什么都不改变。
func NewScriptFlow(engine string) *ScriptFlow { return &ScriptFlow{engine: engine} }

// NewScriptFlowCtx 同 NewScriptFlow,接着 ctx 上已有的绑定变量(终端标签页声明过的)往下跑。
func NewScriptFlowCtx(ctx context.Context, engine string) *ScriptFlow {
	return &ScriptFlow{engine: engine, binds: bindsFrom(ctx)}
}

// Observe 在执行一条语句之前调用:WHENEVER SQLERROR … 从这一条之后生效;VARIABLE 声明变量。
func (f *ScriptFlow) Observe(stmt string) {
	c, ok := OracleClientCommand(f.engine, stmt)
	if ok && c.Kind == SQLPlusVariable {
		if b, ok := ParseVariable(c.Text); ok {
			f.binds = DeclareBind(f.binds, b)
		}
		return
	}
	if !ok || c.Kind != SQLPlusWhenever {
		return
	}
	w := strings.Fields(strings.ToUpper(c.Text))
	if len(w) < 2 || w[0] != "SQLERROR" {
		return
	}
	switch w[1] {
	case "CONTINUE":
		f.cont = true
	case "EXIT":
		f.cont = false
	}
}

// Context 给这一条带上脚本到目前为止的绑定变量。
func (f *ScriptFlow) Context(ctx context.Context) context.Context { return WithBinds(ctx, f.binds) }

// Update 用执行结果里读回的新值更新绑定变量(PL/SQL 改了它们)。
func (f *ScriptFlow) Update(binds []Bind) {
	if len(binds) > 0 {
		f.binds = binds
	}
}

// Binds 是脚本到目前为止的绑定变量。
func (f *ScriptFlow) Binds() []Bind { return f.binds }

// ContinueOnError:这一条出错之后要不要接着跑。
func (f *ScriptFlow) ContinueOnError() bool { return f.cont }

// IsExit:这一条是 EXIT / QUIT,脚本在这里正常结束。
func (f *ScriptFlow) IsExit(stmt string) bool {
	c, ok := OracleClientCommand(f.engine, stmt)
	return ok && c.Kind == SQLPlusExit
}

// SQLPlusIgnoredNotice 是忽略时的回话。
func SQLPlusIgnoredNotice(c SQLPlusCmd) string {
	if c.Name == "SHOW ERRORS" {
		return "· SHOW ERRORS 未指明对象:上一条 CREATE 的编译错误已随那条语句报出,已忽略"
	}
	switch c.Kind {
	case SQLPlusExit:
		return "· " + c.Name + ":脚本在此结束"
	case SQLPlusWhenever:
		return "· WHENEVER " + c.Text + ":对这份脚本后面的语句生效"
	}
	return "· " + c.Name + " 是 SQL*Plus 客户端命令,这条通道不处理,已忽略"
}
