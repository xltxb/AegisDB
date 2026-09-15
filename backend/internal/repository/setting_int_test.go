package repository

import (
	"testing"

	"velagateway/internal/testsupport"
)

// 设置值是 JSON 编码的,而同一个整数可能以两种形态落库:
//
//   - 裸的 JSON 数字 `2` —— seed 和默认值走的这条路
//   - JSON 字符串 `"2"` —— 从 PUT /api/v1/settings 传字符串进来时走的这条
//
// 两者是同一个值的两种编码,不是两个值。只认前者的后果是**静默回落到默认值**:
// 设置页上明明填了、库里明明有,读出来却是 def —— 而调用方拿到的是一个合法的
// 数字,不会有任何错误冒出来。
//
// 这不同于 SettingBool 那句"刻意不认 1/yes/on":那里拒绝的是**猜语义**,
// 这里认的是同一个字面量的另一种写法。
func TestSettingInt_AcceptsBothJSONShapes(t *testing.T) {
	db := testsupport.NewDB(t)
	r := New(db)

	for _, c := range []struct {
		name  string
		store string
		want  int
	}{
		{"裸 JSON 数字", `2`, 2},
		{"JSON 字符串", `"2"`, 2},
		{"带空格的 JSON 字符串", `" 42 "`, 42},
		{"负数字符串", `"-1"`, -1},
		{"读不懂的值保持默认", `"abc"`, 7},
		{"空字符串保持默认", `""`, 7},
		{"JSON 对象保持默认", `{"a":1}`, 7},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := r.SetSettings(map[string]string{"probe.key": c.store}); err != nil {
				t.Fatalf("SetSettings: %v", err)
			}
			if got := r.SettingInt("probe.key", 7); got != c.want {
				t.Errorf("库里存 %s 时 SettingInt = %d, 想要 %d", c.store, got, c.want)
			}
		})
	}
}

// 没有这一行时用默认值。
func TestSettingInt_MissingKeyUsesDefault(t *testing.T) {
	db := testsupport.NewDB(t)
	r := New(db)
	if got := r.SettingInt("never.set", 99); got != 99 {
		t.Errorf("SettingInt = %d, 想要默认值 99", got)
	}
}
