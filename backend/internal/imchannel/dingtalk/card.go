package dingtalk

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"velagateway/internal/imchannel"
	"velagateway/internal/model"
)

// 卡片模板变量(cardParamMap 的键)。模板由管理员在钉钉卡片平台搭,搭法见
// docs/external-approval-setup.md「钉钉交互审批」;这里的名字与手册一一对应,改一边就得改另一边。
const (
	paramTitle   = "title"
	paramSummary = "summary"
	paramReason  = "reason"
	paramCommand = "command"
	paramConsole = "consoleUrl"
	paramStatus  = "status" // pending:显示按钮;done:隐藏按钮、显示 result
	paramResult  = "result"
	paramTip     = "tip" // 点击后回给点击者的那句话(钉钉卡片回调没有 toast)

	statusPending = "pending"
	statusDone    = "done"
)

// commandLimit:钉钉卡片文本组件没有公开的长度上限。命令在通用层已截到 800,这里再兜一次底。
const commandLimit = 2000

// apNoRe / trackRe:点击回调里只凭 outTrackId 认出是哪张单,所以 outTrackId 只认网关自己造的形状,
// 单号只认白名单 —— 别让任意字符串流进后面的查询和提示语。
var (
	apNoRe  = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)
	trackRe = regexp.MustCompile(`^aegis-([A-Za-z0-9-]{1,32})-[0-9a-f]{16}$`)
)

// newTrackID 给一张卡片造 outTrackId。同一张单发给几个人就是几张卡片,所以带一段随机数。
func newTrackID(apNo string) (string, error) {
	if !apNoRe.MatchString(apNo) {
		return "", fmt.Errorf("无法用作卡片标识的单号 %q", apNo)
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "aegis-" + apNo + "-" + hex.EncodeToString(b), nil
}

// trackApNo 从 outTrackId 里取回单号。
func trackApNo(track string) (string, bool) {
	m := trackRe.FindStringSubmatch(track)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// cardParams 是交给模板的变量。**全部是纯文本**:模板里用「文本」组件显示,不用 Markdown ——
// 发起人写的原因、SQL 里的 [ ]( ) * ` < & 都原样显示,没有可注入的链接或富文本,也就不必转义。
func cardParams(c imchannel.ApprovalCard) map[string]string {
	risk := "🟠 中危"
	if c.Risk == model.RiskHigh {
		risk = "🔴 高危"
	}
	env := strings.ToUpper(c.Env)
	if c.Tier != "" && !strings.EqualFold(c.Tier, c.Env) {
		env += "(" + c.Tier + ")"
	}
	db := c.Database
	if strings.TrimSpace(db) == "" {
		db = "默认"
	}
	return map[string]string{
		paramTitle:   risk + " · 待你审批 · " + c.ApNo,
		paramSummary: "环境 " + env + " · 实例 " + c.Instance + " / " + db + "\n发起人 " + c.Initiator,
		paramReason:  c.Reason,
		paramCommand: clipRunes(c.Command, commandLimit),
		paramConsole: c.ConsoleURL,
		paramStatus:  statusPending,
		paramResult:  "",
		paramTip:     "",
	}
}

// closedParams 是收卡时要改的变量。只改这三个(updateCardDataByKey):单号、命令、原因原样留在卡片上。
func closedParams(final imchannel.CardState) map[string]string {
	return map[string]string{paramStatus: statusDone, paramResult: final.Text, paramTip: ""}
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
