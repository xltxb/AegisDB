package gateway

import (
	"strings"
	"testing"
)

// MaskHash 必须加盐(L-11)。
//
// 无盐的 SHA-256 对**低熵**值等于没打码:手机号一共 11 位、身份证号的结构是公开的,
// 把全空间跑一遍再反查回去,一台笔记本几分钟的事。而这个打码值是给人看的、会进导出、
// 会进截图 —— 它离开网关之后就不再受任何控制。
//
// 这一组钉三件事,少任何一件这个功能就白做:
//
//  1. 盐**真的**参与了计算(换盐必须换结果);
//  2. 同一套部署里同一个值仍然稳定(hash 这种打码方式的全部用处就是"看不到原值,
//     但看得出这两行是同一个人");
//  3. 盐设不上时**不退回**无盐 —— 那是悄悄地把防护去掉,而没有任何地方会报错。
func TestMaskHashIsSalted(t *testing.T) {
	const v = "110101199003074819" // 一个身份证号形状的低熵值

	SetMaskSalt([]byte("deployment-A"))
	a := MaskValue(v, MaskHash)

	SetMaskSalt([]byte("deployment-B"))
	b := MaskValue(v, MaskHash)

	if a == b {
		t.Fatalf("换了盐结果没变(%q)——盐没有参与计算,等于还是无盐 SHA-256", a)
	}
	if !strings.HasPrefix(a, "#") || len(a) != 11 {
		t.Fatalf("打码值的形状变了:%q(期望 # 开头、总长 11)", a)
	}
}

// 同一套部署里,同一个值每次都得到同一个打码值 —— 否则"这两行是同一个人"就看不出来,
// 而那正是选 hash 而不是 full 的理由。
func TestMaskHashIsStableWithinDeployment(t *testing.T) {
	SetMaskSalt([]byte("same-deployment"))
	v := "13800138000"
	a, b := MaskValue(v, MaskHash), MaskValue(v, MaskHash)
	if a != b {
		t.Fatal("同一套部署里同一个值应当得到同一个打码值")
	}
	if MaskValue("13800138000", MaskHash) == MaskValue("13800138001", MaskHash) {
		t.Fatal("不同的值不该撞成同一个打码值")
	}
}

// 空盐不得把盐**清掉**。
//
// 这一条防的是一次很安静的退化:某个新入口(新的 cmd、某个工具、一个测试夹具)忘了灌盐,
// 于是 SetMaskSalt("") 把上一次的盐抹掉,打码悄悄退回可离线还原的状态 —— 没有任何报错,
// 打码值看起来也完全正常。
func TestSetMaskSaltRefusesToClear(t *testing.T) {
	SetMaskSalt([]byte("real-salt"))
	before := MaskValue("13800138000", MaskHash)
	SetMaskSalt(nil)
	SetMaskSalt([]byte{})
	if after := MaskValue("13800138000", MaskHash); after != before {
		t.Fatalf("空盐把已设的盐清掉了:%q -> %q", before, after)
	}
}
