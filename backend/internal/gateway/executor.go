package gateway

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
)

// ExecResult is the outcome of a proxied command execution. For a real read,
// Columns/Data carry the actual result set (Data capped at maxResultRows for the
// terminal); for a simulated read they stay empty and the client synthesises a
// preview from Rows.
type ExecResult struct {
	Output string
	Rows   int
	Ms     int
	// 耗时分段(真实执行才有;毫秒):拿到一条可用连接 / 库端执行(发出语句到收到第一批回包) /
	// 读取结果。总耗时慢的时候,先要分清是"连不上、排队等连接"还是"库端就是慢" —— 两者的处置
	// 完全不同,只有一个总数时只能猜。ProcessMs 是行读完之后网关自己的处理(敏感字段打码),
	// 单列出来:它曾经被算进"读取",一条 0 行的查询显示读取 8 秒,把人往库端 / 网络上引。
	ConnMs, QueryMs, FetchMs, ProcessMs int
	// Notice:要用户特别留意的一句话(如 EXPLAIN 被改成只取执行计划);NoticeRef 给界面按语言显示
	Notice    string
	NoticeRef *model.RuleRef
	Columns                  []string   // result-set column names (real reads only)
	Data                     [][]string // result rows as strings, capped (real reads only)
	Truncated                bool       // true when the result set exceeded maxResultRows
	// MaskedColumns:被脱敏的列名。数据本身已经是打码后的了 —— 这个字段只是让
	// 界面能标一句"该列已脱敏",而不是让前端去做脱敏。
	MaskedColumns []string
	// RowsUnknown:数据库没有报影响行数(Oracle 的 PL/SQL 匿名块 BEGIN … END —— 驱动永远给 0,块里
	// 每条语句的行数只在块内的 SQL%ROWCOUNT 里)。这时 Rows 的 0 不是"影响了 0 行",是"不知道";
	// 审批单据此存 -1,界面显示"PL/SQL 块不返回影响行数",而不是一个错的 0。
	RowsUnknown bool
	// 分批显示(Web 命令行,见 result_cache.go):ResultID 非空 = 还有暂存的后续行可"加载更多";
	// TotalRows 是读到的总行数;BeyondCap = 超过了网关一次读取的上限,真实总数更多(改用数据导出)。
	ResultID  string
	TotalRows int
	BeyondCap bool
	Err           error // non-nil when the target DB rejected the statement
	// OutputRef 是 Output 那句话的机器可读身份,给界面用读者的语言重讲一遍。
	// Output 仍然是规范记录(进审计、进工单的 Result),两者一起下发 —— 理由与
	// 裁决的 Rule/RuleRef 完全相同,见 model.RuleRef。
	OutputRef *model.RuleRef
	// Simulated:这个结果是**编出来的**,没有接触任何数据库。
	//
	// 它必须一路带到审计:一条从未发生的执行如果记成 executed,就是在审计链上写下
	// 一句假话(见 model.ResultSimulated)。生产环境不会出现它 —— 模拟一律被拒。
	Simulated bool
	// ServerOutput:终端开了 SET SERVEROUTPUT ON 时,这次执行读回的 DBMS_OUTPUT 行(Oracle)。
	// 出错时也带着 —— 报错之前 PUT_LINE 的内容正是排查要看的东西。
	ServerOutput          []string
	ServerOutputTruncated bool
	// Binds:执行后绑定变量的新值(SQL*Plus 的 VARIABLE,见 oracle_binds.go);没有绑定时为空。
	Binds []Bind
}

// Executor proxies a command to the target instance on behalf of the user.
//
// NOTE: target instances (prod-order-cluster, etc.) are not reachable in a dev
// environment, so execution is simulated — returning realistic row/latency
// figures, mirroring the prototype. Wire a real database/sql driver per engine
// here for production.
type Executor struct{}

func NewExecutor() *Executor { return &Executor{} }

// Run executes sql against conn within the given execution timeout. When the
// connection is configured for real execution (credentials present, supported
// engine) it opens the target DB and runs the statement; otherwise it returns a
// simulated result (demo instances are unreachable in dev; timeout is ignored).
func (x *Executor) Run(ctx context.Context, conn *model.Connection, sql string, timeout time.Duration) ExecResult {
	// Ms is MEASURED here, around the call itself, for every path.
	//
	// A real run never set it, so the console reported every query as "0ms"; the
	// simulated one reported rand.Intn(40)+4 — an invented latency printed in the
	// same place, in the same format, as a measurement. A number nobody measured
	// is worse than no number, because it is indistinguishable from one that was.
	//
	// It times the execution, not the request: judgement, approval routing and
	// the audit write are the gateway's own overhead and do not belong in a figure
	// an operator reads as "how long the database took".
	started := time.Now()
	elapsed := func() int { return int(time.Since(started).Milliseconds()) }

	// SQL*Plus 的 SET(显示设置 / SERVEROUTPUT)不是 SQL:终端在本地处理;从别的通道(发布单脚本、
	// 开放接口)来的,回一句"已忽略",不下发 —— 一行显示设置不该让整个脚本失败。
	// 其余不能翻译成 SQL 的 SQL*Plus 命令(见 oracle_sqlplus_cmd.go):拒绝的让这一条失败
	// (脚本遇错即停),忽略的回一句话,PROMPT 原样回话。与判定用同一个识别函数。
	if c, ok := OracleClientCommand(conn.Engine, sql); ok {
		switch c.Kind {
		case SQLPlusRefused:
			msg := SQLPlusRefusedNotice(c)
			return ExecResult{Output: msg, Err: errors.New(strings.TrimPrefix(msg, "· ")), Ms: elapsed(),
				OutputRef: model.NewRuleRef(model.OutSQLPlusRefused, "cmd", c.Name, "reason", c.Reason)}
		case SQLPlusPrompt:
			return ExecResult{Output: c.Text, Ms: elapsed()}
		case SQLPlusExit:
			return ExecResult{Output: SQLPlusIgnoredNotice(c), Ms: elapsed(),
				OutputRef: model.NewRuleRef(model.OutSQLPlusExit, "cmd", c.Name)}
		case SQLPlusVariable:
			// 声明由调用方记(ScriptFlow / 终端标签);这里只说清认没认出来
			b, ok := ParseVariable(c.Text)
			if !ok {
				msg := "· VARIABLE " + c.Text + ":类型不支持(支持 NUMBER、BINARY_FLOAT/DOUBLE、CHAR/NCHAR、VARCHAR2/NVARCHAR2(n)、CLOB/NCLOB;REFCURSOR 暂不支持)"
				return ExecResult{Output: msg, Err: errors.New(strings.TrimPrefix(msg, "· ")), Ms: elapsed(),
					OutputRef: model.NewRuleRef(model.OutSQLPlusVarBad, "text", c.Text)}
			}
			return ExecResult{Output: "· VARIABLE " + b.Name + " " + b.Type + " 已声明", Ms: elapsed(),
				OutputRef: model.NewRuleRef(model.OutSQLPlusVarDeclared, "name", b.Name, "type", b.Type)}
		case SQLPlusPrint:
			cols, row, bad := printBinds(bindsFrom(ctx), strings.Fields(c.Text))
			if bad != "" {
				msg := "· PRINT:绑定变量 " + strings.ToUpper(bad) + " 没有声明"
				return ExecResult{Output: msg, Err: errors.New(strings.TrimPrefix(msg, "· ")), Ms: elapsed(),
					OutputRef: model.NewRuleRef(model.OutSQLPlusPrintBad, "name", strings.ToUpper(bad))}
			}
			if len(cols) == 0 {
				return ExecResult{Output: "· PRINT:还没有声明任何绑定变量", Ms: elapsed(),
					OutputRef: model.NewRuleRef(model.OutSQLPlusPrintNone)}
			}
			return ExecResult{Output: "+ 1 rows", Rows: 1, Columns: cols, Data: [][]string{row}, Ms: elapsed()}
		case SQLPlusWhenever:
			return ExecResult{Output: SQLPlusIgnoredNotice(c), Ms: elapsed(),
				OutputRef: model.NewRuleRef(model.OutSQLPlusWhenever, "text", c.Text)}
		default:
			return ExecResult{Output: SQLPlusIgnoredNotice(c), Ms: elapsed(),
				OutputRef: model.NewRuleRef(model.OutSQLPlusIgnored, "cmd", c.Name)}
		}
	}
	if opt, _, ok := SQLPlusSet(conn.Engine, sql); ok {
		return ExecResult{Output: SQLPlusSetNotice(opt), Ms: elapsed(),
			OutputRef: model.NewRuleRef(model.OutSQLPlusSetIgnored, "option", opt)}
	}
	// 判定层拆不出语句 = 判定层认为这里只有注释或空白,那就真的不下发。
	//
	// 判定与下发读的是两份文本:判定读剥过注释的,下发读原文。服务器要是把某种"注释"当语句执行,
	// 判定看到的就是空,而空语句按只读放行 —— /*T! …*/(2026-10)、/*M! …*/(2026-11)都是这么
	// 绕过全部判定的。这里是那一类问题的兜底:再冒出一种没见过的可执行注释,也到不了服务器。
	if engineFamily(conn.Engine) != familyMongo && len(SplitForJudge(conn.Engine, sql)) == 0 {
		return ExecResult{Output: "· 只有注释或空白,没有可执行的语句", Ms: elapsed(), OutputRef: model.NewRuleRef(model.OutNothingToRun)}
	}
	if RealExecSupported(conn) {
		res, err := RealRun(ctx, conn, sql, timeout)
		if err != nil {
			// 操作员按下 Ctrl+C 与"数据库报错"是两件事,不能都说成"执行失败"。
			//
			// 而且**取消不等于没执行**:取消时网关会要求目标库终止语句(MySQL 系由网关
			// 另开连接发 KILL QUERY,见 killquery.go;PostgreSQL 发 CancelRequest;Oracle
			// 发 break),语句可能已经跑完了、也可能跑了一半。这句话必须说准 —— 说成
			// "已取消,未执行"会让人以为库里什么都没发生,那是这个功能最坏的一种错。
			if errors.Is(err, context.Canceled) {
				return ExecResult{Output: "· 已取消 —— 目标库可能已执行或部分执行,请自行核对", Err: err, Ms: elapsed(),
					OutputRef: model.NewRuleRef(model.OutExecCancelled), ServerOutput: res.ServerOutput}
			}
			// 超时:没人按键,是等太久了。原先落进下面的"数据库执行失败",带着驱动吐出来的
			// "invalid connection" —— 看起来像库出了故障,而实际是网关按时限停掉了它。
			if errors.Is(err, context.DeadlineExceeded) {
				sec := int(timeout.Seconds())
				if sec <= 0 {
					sec = 30
				}
				return ExecResult{
					Output: fmt.Sprintf("· 执行超时(超过 %d 秒),已要求目标库终止该语句 —— 语句可能已部分执行,请自行核对", sec),
					Err:    err, Ms: elapsed(), ServerOutput: res.ServerOutput,
					OutputRef: model.NewRuleRef(model.OutExecTimeout, "sec", strconv.Itoa(sec)),
				}
			}
			// 驱动原文不翻译:它是目标库说的话,翻过来就不是它说的了。网关自己的话(apimsg,如编译错误
			// 清单、只读守卫拒绝)嵌成 part,界面按读者语言显示。
			ref := model.NewRuleRef(model.OutExecFailed, "err", err.Error())
			if r := apimsg.RefOf(err); r != nil {
				ref.Parts = []model.RuleRef{*r}
			}
			return ExecResult{Output: "· 数据库执行失败: " + err.Error(), Err: err, Ms: elapsed(), ServerOutput: res.ServerOutput,
				OutputRef: ref}
		}
		res.Ms = elapsed()
		return res
	}
	// 生产环境不返回模拟数据 —— 见 simulation.go。
	//
	// 放在这里而不是入口处:模拟分支只有一个,把闸设在它面前,新加的调用路径天然被
	// 覆盖(与脱敏放在 RealRun/RealQueryEach 里是同一条理由)。
	if !SimulationAllowed() {
		return ExecResult{
			Output:    "· " + ErrSimulationDisabled.Error(),
			Err:       ErrSimulationDisabled,
			Ms:        elapsed(),
			OutputRef: model.NewRuleRef(model.OutExecFailed, "err", ErrSimulationDisabled.Error()),
		}
	}
	// SIMULATED-PATH: exec-run —— 见 docs/simulated-paths.md。
	//
	// 这里合成的行数与真实执行的输出**长得一模一样**,用户分辨不出。一台忘了填密码的
	// 实例跑 SELECT 会拿到一份看似合理的假结果 —— 那是这条路径明知的欠账,选项写在
	// 那份清单的"待补充"里。
	//
	// Simulated: the ROW COUNT is still synthetic (there is no database to ask),
	// but the duration is the real time this took. The output text no longer
	// carries it — the console renders Ms uniformly for both paths.
	if IsRead(sql) {
		rows := rand.Intn(9000) + 200
		return ExecResult{Output: fmt.Sprintf("+ %s rows", thousands(rows)), Rows: rows, Ms: elapsed(), Simulated: true}
	}
	affected := rand.Intn(5)
	return ExecResult{Output: fmt.Sprintf("执行成功 · %d 行受影响", affected), Rows: affected, Ms: elapsed(),
		OutputRef: model.NewRuleRef(model.OutExecAffected, "n", strconv.Itoa(affected)), Simulated: true}
}

// Test 探一次连通性 —— **真的连过去**,不是模拟。
//
// 它从前是 `sleep(120ms); return true, "已接入网关"`,一个无条件说好话的桩。批量巡检
// 用的就是它,于是 88 台实例全被报成可连,而其中有的连 dial 都超时。一个永远说好话的
// 巡检比没有巡检更糟:没有巡检时人会自己去试,有一个说"可连"的巡检时人不会。
//
// 没配凭据的模拟连接报**不可连**,并说清是为什么 —— 把"我们没配凭据"说成"那台库是
// 好的",是同一种谎的另一种说法。
func (x *Executor) Test(conn *model.Connection) (bool, string) {
	return RealPing(conn)
}

func thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	return strings.Join(out, ",")
}
