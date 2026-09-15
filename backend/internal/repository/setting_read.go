package repository

// 设置项的读法,全项目一份。
//
// 从前 middleware、webhook 的 Dispatcher、service 的 Services 各带一对逐字相同的
// settingString / settingBool。它们共用的不只是"解一段 JSON"这件显然的事,还有一条
// **不显然**的兼容规则:解不出 JSON 时,把首尾的引号裁掉当字符串用 —— 那是历史上没做
// JSON 编码就直接存进去的值。
//
// 这种兼容规则正是抄三遍会出事的那一类:有人在自己那一份里把它去掉(看着像多余的),
// 另外两份还留着,于是同一个 key 在两个地方读出两个值,而两边都不报错。

import (
	"encoding/json"
	"strconv"
	"strings"
)

// SettingString 读一个字符串设置(JSON 编码),读不到就用 def。
//
// 空值与"这一行不存在"是一回事:都当没配过。
func (r *Repo) SettingString(key, def string) string {
	v, err := r.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var s string
	if json.Unmarshal([]byte(v), &s) == nil {
		return s
	}
	// 解不出 JSON —— 老数据是裸着存的。裁掉首尾引号是能救回来的最好结果。
	return strings.Trim(v, `"`)
}

// SettingBool 读一个布尔设置(JSON 编码),读不懂就用 def。
//
// 刻意不认 "1"/"yes"/"on" 这类写法:一个读不懂的开关该保持它的默认,而不是由这里
// 猜运维想开还是想关。
func (r *Repo) SettingBool(key string, def bool) bool {
	v, err := r.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var b bool
	if json.Unmarshal([]byte(v), &b) == nil {
		return b
	}
	return def
}

// SettingInt 读一个整数设置(JSON 编码),读不懂就用 def。
//
// 同一个整数会以两种形态落库:seed 与默认值写的是裸的 JSON 数字 `2`,而从
// PUT /api/v1/settings 传字符串进来时存的是 JSON 字符串 `"2"`。两者是同一个值的
// 两种编码,所以都认。
//
// 只认前者的后果是**静默回落到默认值**:设置页上明明填了、库里明明有,读出来却是
// def,而调用方拿到的是一个合法的数字,不会有任何错误冒出来 —— 一个设置"看起来
// 生效了其实没有",比它明确报错难查得多。
//
// 这和隔壁 SettingBool"刻意不认 1/yes/on"不是一回事:那里拒绝的是**猜语义**
// (运维写 1 到底想开还是想关,这里没资格替他决定);这里认的是同一个字面量的另一种
// 写法,没有任何要猜的东西。读不懂的仍然回落 def —— 见下面 Atoi 那一步。
func (r *Repo) SettingInt(key string, def int) int {
	v, err := r.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var n int
	if json.Unmarshal([]byte(v), &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal([]byte(v), &s) == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
	}
	return def
}
