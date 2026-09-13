package review

import "encoding/json"

// 规则的**出厂参数**只写一次。
//
// `Rule.Params` 的注释写着「"" = built-in defaults」—— 种子给的那份 JSON 和检查器里的
// 兜底值说的本该是同一件事。两处各写一份的代价已经出现过:
//
//   · `dws.max.join.tables` 的标题写着「不超过 8 张」、种子给 8,而检查器在 params 为空
//     时用 16 —— 同一条规则两个阈值,而界面上显示的是标题里那个 8。
//   · `dws.forbid.volatile.in.subquery` 的两份函数列表互有对方没有的条目:种子有
//     `gen_random_uuid`(PG 13+ 生成 UUID 的标准写法),检查器默认没有。params 一空,
//     这条规则查的就是另一组函数。
//
// params 什么时候会空?管理员在界面上把它清掉、或者照着内置规则手工新建一条 —— 而那时
// 他期待的显然是「照标题说的那样」。所以两边都从这里取。

// defaultMaxJoinTables 是单条 SQL 允许关联的表数上限(DWS RULE 52,B 端 8 张)。
const defaultMaxJoinTables = 8

// defaultVolatileFns 是子查询里禁用的不稳定函数(DWS RULE 42「不下推写法」)。
//
// 它们的共同点是**同一条语句里每次求值都可能给出不同结果**,因而阻止子查询下推 ——
// 序列(nextval / currval / lastval)、各种 UUID 生成、随机数、以及会随调用时刻变化的
// 时间函数。
var defaultVolatileFns = []string{
	"nextval", "currval", "lastval",
	"uuid", "uuid_generate_v1", "uuid_generate_v4", "gen_random_uuid", "sys_guid",
	"random", "clock_timestamp",
}

// jsonParams 把一组出厂参数写成 Rule.Params 里那串 JSON。
//
// 内置规则表(builtins.go)用它,于是种子里的值和检查器的兜底值来自同一个常量,不可能
// 再分叉。
func jsonParams(kv map[string]any) string {
	b, err := json.Marshal(kv)
	if err != nil { // 参数表是代码里写死的,编不出 JSON 只可能是写错了
		panic("review: bad builtin params: " + err.Error())
	}
	return string(b)
}
