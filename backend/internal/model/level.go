package model

// 能力档位的读法与排序,全项目一份。
//
// 档位是三个字符串:allow ≺ approve ≺ deny。这个顺序从前被抄了三遍(判定里两处、
// 仓储里一处),而抄得一模一样并不是问题所在 —— 问题在于三份都用 map 索引,而 map
// 对认不出的键返回零值 **0**,也就是"最宽松"。
//
// 于是读不懂的档位一路绿灯:它自己被当成 allow,在多角色的并集里还会**压过**另一个
// 角色上明明白白的 deny。而消费端那三个分支(== deny / == approve / 否则放行)的
// 兜底方向同样是放行。两处都朝开放的一侧失效,叠在一起就是:矩阵里写着 "Deny" 的
// 那一格,实际是放行。
//
// 这不是假想的输入。能力矩阵的保存接口对档位值零校验,原样写库;而同一个查询函数对
// capability 与 tier 是**显式大小写不敏感**的,注释里写着理由:存进去的大小写本来
// 就不一致。唯独档位值原样返回。
//
// 现在:读不懂就是 deny(规则读不出来等于拒绝,与 ED3 对失败查询的立场一致 —— 一条
// 读不出来的规则不等于没有规则),大小写与首尾空白仍认得住(那是同一个 deny)。

import "strings"

// levelRank orders capability levels by how much they gate. 不导出:排序是这个文件
// 的内部知识,外面只该拿到 LevelOf / StricterLevel / LooserLevel 的答案,不该再拿到
// 一张可以被抄走的表。
var levelRank = map[string]int{LevelAllow: 0, LevelApprove: 1, LevelDeny: 2}

// LevelOf 把存下来的档位读成三个已知值之一。
//
// 大小写与首尾空白不算差别。任何**读不懂**的值(包括空串)读成 LevelDeny —— 兜底必须
// 朝收紧的一侧,而不是朝放行。
func LevelOf(raw string) string {
	switch s := strings.ToLower(strings.TrimSpace(raw)); s {
	case LevelAllow, LevelApprove, LevelDeny:
		return s
	default:
		return LevelDeny
	}
}

// KnownLevel 报告 raw 是不是一个认得出的档位。
//
// 写入端用它把脏值挡在门外:LevelOf 在读的那一侧兜底是对的,但把 "Deny" 收下来、
// 存进去、再在读的时候悄悄变成 deny,等于让人以为自己写对了。
func KnownLevel(raw string) bool {
	_, ok := levelRank[strings.ToLower(strings.TrimSpace(raw))]
	return ok
}

// StricterLevel 返回 a、b 里更收紧的那一档。
func StricterLevel(a, b string) string {
	la, lb := LevelOf(a), LevelOf(b)
	if levelRank[lb] > levelRank[la] {
		return lb
	}
	return la
}

// LooserLevel 返回 a、b 里更宽松的那一档 —— 多角色取并集时用。
//
// 读不懂的那一档在这里是 deny,所以它压不过任何东西:一个写坏了的角色不会因为"读不懂"
// 反而成了最宽松的那个。
func LooserLevel(a, b string) string {
	la, lb := LevelOf(a), LevelOf(b)
	if levelRank[lb] < levelRank[la] {
		return lb
	}
	return la
}
