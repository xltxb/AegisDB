package service

import (
	"strings"
	"unicode"
)

// 邮箱是账号的身份,但库里的比较不一定认这一点:tbl_user 没写 COLLATE,MySQL 默认的 _ai_ci 排序规则
// 不分重音,`LOWER(email) = ?` 查 josé@corp.io 能查到 jose@corp.io。于是
//
//   - SSO:IdP 上一个只差重音的邮箱就能冒充别人,并自动绑定(2026-11 审计 V-09);
//   - 登录:每种重音写法各算一个频控桶,按账号的退避被绕过(V-11)。
//
// 所以查到之后再比一次,比较只折叠大小写。Unicode 的大小写折叠不能直接用:strings.EqualFold 认为
// K(开尔文符号)等于 k、ſ 等于 s,strings.ToLower 也会把 K 变成 k,那又是一种"差一个字符的邮箱"。

// normEmailForCompare 去掉首尾空白并折叠大小写,但绝不把非 ASCII 字符折叠成 ASCII。
func normEmailForCompare(s string) string {
	return strings.Map(func(r rune) rune {
		l := unicode.ToLower(r)
		if r > unicode.MaxASCII && l <= unicode.MaxASCII {
			return r
		}
		return l
	}, strings.TrimSpace(s))
}

// sameEmail 报告两个邮箱是不是同一个地址(只差大小写和首尾空白)。
func sameEmail(a, b string) bool { return normEmailForCompare(a) == normEmailForCompare(b) }
