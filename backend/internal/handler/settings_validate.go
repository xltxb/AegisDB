package handler

// PUT /settings 的逐键校验(2026-10 黑盒测试,工单 02 第 7、8 条)。
//
// 从前这里除了 SSO 与业务查询那几项,其余的键收到什么存什么:导出保留 -1 天、执行时限 0 秒、
// 一个拼错的键名、一个相对路径,都照单落库,而读的一侧按类型解码失败就悄悄退回默认值 ——
// 设置页显示"已保存",实际生效的是另一个数。
//
// 现在每个键都要在下面这张表里:未知键拒绝,非法值整次拒绝(一项都不存)。表里的范围与设置页
// 输入框的上下界一致(SettingsView.tsx 的 clampInt),前端发出的合法请求一个都不会被挡 ——
// TestSettings_FrontendPayloadIsAccepted 把那一整包原样钉住。

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/chat/telegram"
	"aegisdb/internal/service"
)

// settingCheck 校验一个键的值,返回要存的规整值。
type settingCheck func(key string, v any) (any, error)

// settingClear 是"清除这个密钥"的规整值(请求里传的是 JSON null)。
type settingClear struct{}

// maxSettingString 自由文本类设置的长度上限:再长也不会是一个合理的配置值。
const maxSettingString = 4096

func intIn(min, max int64) settingCheck {
	return func(key string, v any) (any, error) {
		var n int64
		switch x := v.(type) {
		case float64:
			if x != math.Trunc(x) || math.IsInf(x, 0) || math.IsNaN(x) {
				return nil, apimsg.Errorf("%s 必须是整数", key)
			}
			n = int64(x)
		case string:
			p, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
			if err != nil {
				return nil, apimsg.Errorf("%s 必须是整数", key)
			}
			n = p
		default:
			return nil, apimsg.Errorf("%s 必须是整数", key)
		}
		if n < min || n > max {
			return nil, apimsg.Errorf("%s 超出范围(%d ~ %d):%d", key, min, max, n)
		}
		return n, nil
	}
}

func boolean(key string, v any) (any, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, apimsg.Errorf("%s 必须是布尔值", key)
	}
	return b, nil
}

func oneOf(vals ...string) settingCheck {
	return func(key string, v any) (any, error) {
		s, ok := v.(string)
		if ok {
			for _, x := range vals {
				if s == x {
					return s, nil
				}
			}
		}
		return nil, apimsg.Errorf("%s 只能是 %s", key, strings.Join(vals, " / "))
	}
}

func str(key string, v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, apimsg.Errorf("%s 必须是字符串", key)
	}
	if len(s) > maxSettingString {
		return nil, apimsg.Errorf("%s 过长", key)
	}
	return s, nil
}

// httpURL:空串(未配置)或一个 http(s) 地址。
func httpURL(key string, v any) (any, error) {
	sv, err := str(key, v)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(sv.(string))
	if s == "" {
		return s, nil
	}
	u, perr := url.Parse(s)
	if perr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, apimsg.Errorf("%s 不是合法的 http(s) 地址:%s", key, s)
	}
	return s, nil
}

// outboundURL:网关会主动去请求的地址(飞书机器人、外部审批)。除了 http(s),还拒绝 link-local
// 与云元数据地址 —— 与 Webhook 地址同一条规则(service.ValidateWebhookEndpoint)。
func outboundURL(key string, v any) (any, error) {
	sv, err := httpURL(key, v)
	if err != nil {
		return nil, err
	}
	s := sv.(string)
	if s == "" {
		return s, nil
	}
	if err := service.ValidateWebhookEndpoint(s); err != nil {
		return nil, fmt.Errorf("%s:%v", key, err)
	}
	return s, nil
}

// absPath:空串(未配置,用默认目录或不接收)或一个绝对路径,且不含 ".." 段。
//
// 同时认 "/" 开头的写法:后端只部署在 Linux 上,但开发机是 Windows,filepath.IsAbs 在那里不认
// "/var/aegisdb/x" —— 不能因为在哪台机器上点了保存就得出不同的结论。
func absPath(key string, v any) (any, error) {
	sv, err := str(key, v)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(sv.(string))
	if s == "" {
		return s, nil
	}
	if !filepath.IsAbs(s) && !strings.HasPrefix(s, "/") {
		return nil, apimsg.Errorf("%s 必须是绝对路径:%s", key, s)
	}
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return nil, apimsg.Errorf("%s 不能包含 \"..\":%s", key, s)
		}
	}
	return s, nil
}

// ipList:逗号 / 空白分隔的 IP 或 CIDR(与中间件 splitCIDRs + ipMatches 同一读法)。写错的一项
// 在匹配时永远不命中,开着白名单时等于把那个来源挡在门外,而界面上看不出哪一项是坏的。
func ipList(key string, v any) (any, error) {
	sv, err := str(key, v)
	if err != nil {
		return nil, err
	}
	s := sv.(string)
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == '\t' || r == '\r' }) {
		if strings.Contains(f, "/") {
			if _, _, cerr := net.ParseCIDR(f); cerr != nil {
				return nil, apimsg.Errorf("%s 里有写错的网段:%s", key, f)
			}
		} else if net.ParseIP(f) == nil {
			return nil, apimsg.Errorf("%s 里有写错的 IP:%s", key, f)
		}
	}
	return s, nil
}

// secret:非空字符串 = 替换;"" = 不改(界面从不回显原值,空着就是没动);null = 清除。
func secret(key string, v any) (any, error) {
	if v == nil {
		return settingClear{}, nil
	}
	return str(key, v)
}

// trimmedSecret:同 secret,去掉首尾空白。密钥多半是粘贴来的,带上一个换行就怎么都连不上,而存进去的
// 值谁也看不见。
func trimmedSecret(key string, v any) (any, error) {
	nv, err := secret(key, v)
	if s, ok := nv.(string); ok {
		return strings.TrimSpace(s), err
	}
	return nv, err
}

// chatAppID:空串或一个应用标识。长度以绑定表里标识列的宽度为限:更长的应用谁也绑不上。
func chatAppID(key string, v any) (any, error) {
	sv, err := str(key, v)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(sv.(string))
	if !chatAppIDRe.MatchString(s) {
		return nil, apimsg.Errorf("%s 只能是字母、数字、下划线和短横线,最多 64 个字符", key)
	}
	return s, nil
}

var chatAppIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)

// chatBotToken:同 trimmedSecret,另外要像一个机器人令牌 —— 它会拼进请求的地址。报错不带值。
func chatBotToken(key string, v any) (any, error) {
	nv, err := trimmedSecret(key, v)
	if s, ok := nv.(string); ok && err == nil && s != "" && !telegram.ValidToken(s) {
		return nil, apimsg.Errorf("%s 不像机器人令牌(形如 123456789:AA…)", key)
	}
	return nv, err
}

// chatProxy:同 trimmedSecret,另外要是一个代理地址。它可能带口令,按密钥处理,报错不带值。
func chatProxy(key string, v any) (any, error) {
	nv, err := trimmedSecret(key, v)
	if s, ok := nv.(string); ok && err == nil {
		if _, perr := telegram.ParseProxy(s); perr != nil {
			return nil, apimsg.Errorf("%s 只能是 http、https 或 socks5 的代理地址(如 socks5://用户:口令@主机:1080)", key)
		}
	}
	return nv, err
}

// passthrough:由专门的整体校验负责的键(业务查询那一组,见 service.CheckBizQuerySettings)。
func passthrough(_ string, v any) (any, error) { return v, nil }

// settingSchema 是 PUT /settings 认得的全部键。加一个设置,就在这里加一行 —— 漏了的键会被拒绝,
// 这正是要的效果:一个读的一侧不认识的键,存进去也什么都不控制。
var settingSchema = map[string]settingCheck{
	// 网关
	"gateway.defaultPolicy":       oneOf("strict", "approve-1", "audit-only"),
	"gateway.execTimeout":         intIn(1, 3600),
	"gateway.asyncExecTimeout":    intIn(1, 86400),
	"gateway.approvalExecTimeout": intIn(1, 86400),
	"terminal.resultPageSize":     intIn(100, 50000),
	"strictMode":                  passthrough, // 旧客户端还在发,SaveSettings 里静默丢弃(迁移 0030)

	// 品牌(系统名称 / LOGO):空串 = 未配置,各处保持原字样
	service.SettingBrandName:   brandName,
	service.SettingBrandNameEn: brandName,
	service.SettingBrandLogo:   brandLogo,

	// 在线变更
	"osc.minRows":           intIn(1, 1<<53),
	"osc.minBytes":          intIn(1, 1<<53),
	"osc.maxThreadsRunning": intIn(0, 10000),
	"osc.maxReplicaLagSec":  intIn(0, 86400),
	"osc.chunkSize":         intIn(1, 100000),

	// 路径
	"script.savePath":     absPath,
	"inspection.savePath": absPath,
	"export.savePath":     absPath,

	// 导出
	"export.maxRows":       intIn(0, 1<<53),
	"export.maxBytes":      intIn(0, 1<<53),
	"export.execTimeout":   intIn(1, 86400),
	"export.retentionDays": intIn(0, 3650),

	// 元数据同步 / 连通性巡检
	"meta.sync.enabled":          boolean,
	"meta.sync.intervalHours":    intIn(1, 720),
	"meta.sync.concurrency":      intIn(1, 8),
	"conn.check.enabled":         boolean,
	"conn.check.intervalMinutes": intIn(1, 1440),
	"conn.check.concurrency":     intIn(1, 16),

	// 审批
	"approval.onTimeout":        oneOf("auto-reject", "auto-escalate", "keep-waiting"),
	"approval.timeoutMinutes":   intIn(0, 43200),
	"approval.escalate":         boolean,
	"approval.allowSelfApprove": boolean,

	// 外部飞书审批(审批魔方):对接已移除(#137),这 8 个键仍收下、不生效 —— 老调用方整包提交
	// 不该因此报错。
	"approval.external.enabled":          boolean,
	"approval.external.releaseEnabled":   boolean,
	"approval.external.baseURL":          outboundURL,
	"approval.external.token":            secret,
	"approval.external.aiGroup":          str,
	"approval.external.callbackBaseURL":  httpURL,
	"approval.external.callbackSecret":   secret,
	"approval.external.callbackAllowIPs": ipList,

	// 安全
	"security.sessionTTL":          oneOf("4h", "8h", "24h"),
	"security.requireMFA":          boolean,
	"security.mfaMandatory":        boolean,
	"security.mfaGraceMinutes":     intIn(0, 720),
	"security.idleLock":            boolean,
	"security.idleMinutes":         intIn(1, 1440),
	"security.adminIdleMinutes":    intIn(1, 1440),
	"security.ipAllowEnabled":      boolean,
	"security.ipAllowlist":         ipList,
	"security.adminIpAllowEnabled": boolean,
	"security.adminIpAllowlist":    ipList,

	// SSO(启用时的整体校验另见 checkSSOSettings)
	"sso.enabled":    boolean,
	"sso.baseUrl":    httpURL,
	"sso.cookieName": str,

	// 通知
	"notify.lark":        boolean,
	"notify.email":       boolean,
	"notify.push":        boolean,
	"notify.larkWebhook": outboundURL,
	"notify.larkSecret":  secret,
	"notify.larkChannel": str,
	"notify.consoleURL":  httpURL,

	// 聊天审批卡片(启用时的整体校验另见 checkChatSettings)
	"chat.cardLang":       oneOf("zh", "en"),
	"chat.lark.enabled":   boolean,
	"chat.lark.region":    oneOf("feishu", "lark"),
	"chat.lark.appId":     chatAppID,
	"chat.lark.appSecret": trimmedSecret,

	"chat.telegram.enabled":    boolean,
	"chat.telegram.botToken":   chatBotToken,
	"chat.telegram.includeSql": boolean,
	"chat.telegram.proxyToken": chatProxy,

	// 业务方数据查询:类型与范围由 service.CheckBizQuerySettings 叠到当前值上整体校验
	service.SettingBizDefaultMaxRows:  passthrough,
	service.SettingBizMaxRowsCap:      passthrough,
	service.SettingBizDefaultTimeout:  passthrough,
	service.SettingBizTimeoutCap:      passthrough,
	service.SettingBizMaxGrantDays:    passthrough,
	service.SettingBizMaxPerUser:      passthrough,
	service.SettingBizMaxPerSource:    passthrough,
	service.SettingBizMaxDailyPerUser: passthrough,
	service.SettingBizExportEnabled:   passthrough,
	service.SettingBizApplyURL:        httpURL, // 空串 = 不给申请链接
}

// validateSettings 逐键校验一次提交,返回规整后的值。任何一项不对都返回错误,调用方一项都不存。
//
// current 返回某键此刻存着的原始 JSON。与存量值一字不差的键不再校验、原样放行:设置页每次保存都把
// 几十个键整包发回来,库里只要有一个存量值不合现在的规则(相对路径、没写 http 前缀的地址……),
// 改任何一项都存不下(6364b74 起,2026-10-26 回归排查)。只校验这次真的改了的键。
func validateSettings(body map[string]any, current func(key string) string) (map[string]any, error) {
	out := make(map[string]any, len(body))
	for k, v := range body {
		check, ok := settingSchema[k]
		if !ok {
			return nil, apimsg.Errorf("不认识的设置项:%s", k)
		}
		if v != nil && !isSecretKey(k) && current != nil {
			if old := current(k); old != "" && old == toJSON(v) {
				out[k] = v
				continue
			}
		}
		if v == nil && !isSecretKey(k) {
			return nil, apimsg.Errorf("%s 不能为空值(null)", k)
		}
		nv, err := check(k, v)
		if err != nil {
			return nil, err
		}
		out[k] = nv
	}
	return out, nil
}
