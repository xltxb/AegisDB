package service

import "testing"

func TestValidateIPAllowlist(t *testing.T) {
	for _, ok := range []string{
		"", "   ",
		"10.20.0.1",
		"10.20.0.0/16",
		"10.20.0.1, 10.20.0.2",
		"10.20.0.0/16\n192.168.1.1",
		"::1, fd00::/8",
		"::ffff:10.0.0.1",
	} {
		if err := ValidateIPAllowlist(ok); err != nil {
			t.Errorf("%q 应当合法,实际 %v", ok, err)
		}
	}

	for _, bad := range []string{
		"10.20.0.300",            // 手滑多打一位 —— 最常见的那一种
		"10.20.0.0/33",           // 前缀长度越界
		"10.20.0.1, 10.20.0.300", // 一条对一条错:整份都不能存,否则人以为两条都生效了
		"not-an-ip",
		"10.20.0.0/",
	} {
		if err := ValidateIPAllowlist(bad); err == nil {
			t.Errorf("%q 应当被拒 —— 判定层遇到认不出的条目是静默跳过的,人会以为它生效了", bad)
		}
	}
}
