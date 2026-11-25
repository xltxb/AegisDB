package gateway

// SQL*Plus 的 SET 命令与 SERVEROUTPUT(DBMS_OUTPUT)。
//
// `SET SERVEROUTPUT ON` 不是 SQL:它告诉 SQL*Plus"执行完 PL/SQL 之后,把 DBMS_OUTPUT 缓冲区
// 里的行读出来打印"。数据库不认得这句话,原样发过去是 ORA-00922 / ORA-00900。
//
// 网关承担客户端那一半:
//
//   - 终端在本地识别 SET,开关记在标签页上,每次执行把它带过来(context,见 WithServerOutput);
//   - 开着时,RealRun 在**同一个物理会话**上:DBMS_OUTPUT.ENABLE → 执行 → GET_LINE 读回;
//     连接池里换一条连接,缓冲区就不在了,所以必须钉住一条连接。
//   - 其余 SET(LINESIZE、PAGESIZE、DEFINE…)是 SQL*Plus 的显示设置,网关终端不用它们。
//     绕过终端直接到达执行器的(例如发布单脚本开头的 SET SERVEROUTPUT ON),按"已忽略"回话,
//     不下发 —— 否则一行显示设置就让整个脚本失败。
//
// Oracle 自己的 SET 语句只有三条:SET TRANSACTION / SET ROLE / SET CONSTRAINT(S),它们是
// 真正的 SQL,照常下发。

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	goora "github.com/sijms/go-ora/v2"
)

type serverOutputKey struct{}

// WithServerOutput 标记这次执行要不要读回 DBMS_OUTPUT(终端开了 SET SERVEROUTPUT ON)。
func WithServerOutput(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, serverOutputKey{}, on)
}

// SQLPlusServerOutput 识别一条 SET SERVEROUTPUT ON / OFF。批量执行时用它让开关对同一批后面的
// 语句生效 —— 与 SQL*Plus 跑脚本一样。
func SQLPlusServerOutput(engine, stmt string) (on, ok bool) {
	opt, val, isSet := SQLPlusSet(engine, stmt)
	if !isSet || (opt != "SERVEROUTPUT" && opt != "SERVEROUT") {
		return false, false
	}
	switch strings.ToUpper(strings.Fields(val + " x")[0]) {
	case "ON":
		return true, true
	case "OFF":
		return false, true
	}
	return false, false
}

func serverOutputOn(ctx context.Context) bool {
	v, _ := ctx.Value(serverOutputKey{}).(bool)
	return v
}

var sqlPlusSetRe = regexp.MustCompile(`(?is)^\s*SET\s+([A-Za-z_]+)\b\s*(.*?)\s*;?\s*$`)

// oracleSQLSet 是 Oracle 自己的 SET 语句 —— 它们是 SQL,不是 SQL*Plus 命令。
var oracleSQLSet = map[string]bool{"TRANSACTION": true, "ROLE": true, "CONSTRAINT": true, "CONSTRAINTS": true}

// SQLPlusSet 识别 Oracle 实例上的 SQL*Plus SET 命令。返回选项名(大写)与其余部分。
func SQLPlusSet(engine, stmt string) (option, value string, ok bool) {
	if engineFamily(engine) != familyOracle {
		return "", "", false
	}
	// 只认单行。SQL*Plus 的 SET 本来就是一行;一段多行文本以 SET 开头,多半是切分器没认出这行
	// SET、把下一条语句粘了上来 —— 把整段当 SET 忽略,下一条就被悄悄跳过了(2026-10-28 SET HEAD OFF)。
	// 不认它,就照 SQL 判定与下发,最坏是 Oracle 报一个看得见的错。
	s := strings.TrimSpace(StripComments(stmt))
	if strings.Contains(s, "\n") {
		return "", "", false
	}
	m := sqlPlusSetRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	opt := strings.ToUpper(m[1])
	if oracleSQLSet[opt] {
		return "", "", false
	}
	return opt, strings.TrimSpace(m[2]), true
}

// SQLPlusSetNotice 是执行器收到一条 SQL*Plus SET 时的回话(以 · 开头:提示,不是执行结果)。
func SQLPlusSetNotice(option string) string {
	switch option {
	case "SERVEROUTPUT", "SERVEROUT":
		return "· SET SERVEROUTPUT 是 SQL*Plus 客户端命令,由网关终端处理;这条通道不回显 DBMS_OUTPUT,已忽略"
	case "AUTOCOMMIT", "AUTO":
		return "· SET AUTOCOMMIT 不生效:网关每条语句执行后即提交,已忽略"
	case "DEFINE", "DEF":
		return "· SET DEFINE 已忽略:网关不做 & 替换变量,& 按原文下发"
	}
	return "· SET " + option + " 是 SQL*Plus 显示设置,网关不使用,已忽略"
}

// 读回的上限:DBMS_OUTPUT 是给人看的调试输出,不是数据通道。
const (
	maxServerOutputLines = 2000
	maxServerOutputBytes = 1 << 20
)

// enableServerOutput 在这条会话上打开 DBMS_OUTPUT(不限缓冲区大小)。
func enableServerOutput(ctx context.Context, sc *sql.Conn) error {
	_, err := sc.ExecContext(ctx, "BEGIN DBMS_OUTPUT.ENABLE(NULL); END;")
	return err
}

// 批量读回:一次往返在库里循环 GET_LINE,最多 serverOutputBatch 行,用 serverOutputSep 连成一个
// CLOB 带回来。
//
// 原先一行一次往返:2000 行就是 2000 次,机器一忙就超过读回时限。批量之后 2000 行是四五次往返。
// 用 CLOB 而不是 VARCHAR2 输出参数:go-ora 的字符串输出参数实际只装得下约 8000 个字符(实测
// 8000 可以、16000 报 ORA-06502),一行长输出就会让整次读回失败 —— 原先逐行读也有这个问题。
// 仍然分批:读到一半超时时,已经读回的批次还在(见 RealRun)。
const (
	serverOutputBatch = 500
	serverOutputSep   = "\x1e" // ASCII RS:输出里不会自然出现,换行符则可能(PUT_LINE 可以带 CHR(10))
)

const serverOutputBatchSQL = `DECLARE
  l VARCHAR2(32767); st INTEGER; buf CLOB; n INTEGER := 0; fin INTEGER := 0; mx INTEGER := :1;
BEGIN
  DBMS_LOB.CREATETEMPORARY(buf, TRUE);
  LOOP
    EXIT WHEN n >= mx;
    DBMS_OUTPUT.GET_LINE(l, st);
    IF st != 0 THEN fin := 1; EXIT; END IF;
    n := n + 1;
    buf := buf || l || CHR(30);
    EXIT WHEN DBMS_LOB.GETLENGTH(buf) > ` + "1048576" + `;
  END LOOP;
  :2 := buf; :3 := n; :4 := fin;
END;`

// plsqlExecer 是 collectServerOutput 需要的那一点会话能力(*sql.Conn 满足;测试里用假的)。
type plsqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// collectServerOutput 读出这条会话 DBMS_OUTPUT 缓冲区里的行。truncated 表示后面还有、没读回来
// (超过上限,或读到一半出错 —— 出错时已经读到的行照样返回,不整个丢掉)。
// 读完之后把剩余的缓冲区清掉,免得这条连接回到池子里带着别人的输出。
func collectServerOutput(ctx context.Context, sc plsqlExecer) (lines []string, truncated bool, err error) {
	size := 0
	for {
		var buf goora.Clob
		var n, fin int64
		// 多要一行:要得到说明后面还有,才知道该标截断
		want := min(serverOutputBatch, maxServerOutputLines-len(lines)+1)
		if _, err = sc.ExecContext(ctx, serverOutputBatchSQL, want,
			goora.Out{Dest: &buf}, goora.Out{Dest: &n}, goora.Out{Dest: &fin}); err != nil {
			return lines, true, err
		}
		var batch []string
		if n > 0 {
			batch = strings.Split(buf.String, serverOutputSep)
			batch = batch[:len(batch)-1] // 每行后面都跟一个分隔符,最后一段是空的
		}
		for _, line := range batch {
			if len(lines) >= maxServerOutputLines || size+len(line) > maxServerOutputBytes {
				truncated = true
				break
			}
			lines = append(lines, line)
			size += len(line)
		}
		if truncated || fin != 0 {
			break
		}
	}
	// 关掉:剩余缓冲区一并丢弃,这条连接回到池子里是干净的。
	_, _ = sc.ExecContext(ctx, "BEGIN DBMS_OUTPUT.DISABLE; END;")
	return lines, truncated, nil
}
