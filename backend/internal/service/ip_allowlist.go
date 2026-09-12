package service

import (
	"fmt"
	"net"
	"strings"
)

// ValidateIPAllowlist 校验一份 IP / CIDR 白名单文本。
//
// 判定层遇到认不出的条目是**静默跳过**的(ipAllowed / middleware.ipMatches),而那正是
// 这个校验必须放在保存这一刻的理由:
//
//	10.20.0.0/16, 10.20.0.300
//
// 存进去之后,那台 10.20.0.300 —— 或者更常见的,某个手滑多打一位的地址 —— 不在任何
// 规则里,而输入框里那行还明明白白写着。人由此以为那台机器被放行了,直到它被挡在门外。
//
// 前端早就在标红了(lib/ipAllowlist.ts),但校验不能只长在界面上:绕过界面直接调接口
// 一样能存,而这份名单是一道访问控制。
//
// 分隔符与前端、与判定层一致:逗号、换行、空白都算。空名单合法 —— 它表示"不限制"。
func ValidateIPAllowlist(list string) error {
	for _, e := range strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		if strings.Contains(e, "/") {
			if _, _, err := net.ParseCIDR(e); err != nil {
				return fmt.Errorf("网段 %q 不合法", e)
			}
			continue
		}
		if net.ParseIP(e) == nil {
			return fmt.Errorf("地址 %q 不合法", e)
		}
	}
	return nil
}
