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
func (r *Repo) SettingInt(key string, def int) int {
	v, err := r.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	var n int
	if json.Unmarshal([]byte(v), &n) == nil {
		return n
	}
	return def
}
