package service

// 审计链的读侧校验。
//
// 这条哈希链存在的全部理由,是"事后有人动过库,查得出来"。链一直在写:每一行带着前一
// 行的哈希,PrevHash 上还有唯一索引防分叉。但从前**没有任何地方读它** —— 没有 verify
// 端点,没有定时校验,谁也没有验过一次。
//
// (这条链记在 **ADR 0019**:payload 的五个版本、v5 为什么是算法变更而不是字段增补,
// 以及 2026-09-15 之前的老行为什么任何版本都验不过。此前这里写的是"ADR 0006",指错了
// —— 0006 是 open-api release intake。)
//
// 一条没人验的链,和没有链的区别只在出事那天才显出来:那天你才发现,它从三个月前就断了。
//
// ## 这个校验能证明什么,不能证明什么
//
// 能:任何一行被**改过**内容(命令、结果、动作人、分层快照…),或者被从链中间**抽掉**,
// 都会让重算出来的哈希对不上,而且指得出是哪一行。
//
// 不能:从**末尾**整段截掉。剩下的链自己仍然自洽 —— 要查出这种,得把链尾定期锚到网关
// 之外(打时间戳、外发、写只读介质)。这件事这里没有做,所以也不在结论里假装做了。
//
// ## 为什么 payload 的构造必须只有一处
//
// 校验就是"照着写的时候那样再算一遍"。这两段代码一旦分开写,分叉的表现是**校验器
// 报出成片的假警报** —— 而那比不校验更糟:没人会信一个天天喊狼来了的警报,最后的
// 结果是校验被关掉。所以 appendAudit 和这里共用 auditPayload。

import (
	"encoding/json"
	"time"

	"velagateway/internal/model"
	"velagateway/pkg/crypto"
)

// 这条链历史上用过五种 payload 构造。校验必须认全它们。
//
// 每次往哈希里加字段,都刻意**没有**重算历史行 —— 重算等于把证据重新签一遍,那就不是
// 证据了。代价是:一个从 2026-07 跑到今天的部署,链上好几个年代的行都有,而只认最新那一
// 版的校验器会把绝大多数历史行报成"被篡改"。那比不校验更糟:没人会信一个天天喊狼来了
// 的警报,最后的结果是校验被关掉。
//
// 下面这张表是从 git 历史里查出来的,不是照着记忆写的:
//
//	v1  2026-07-17  time actor instance command risk result ap
//	v2  2026-07-21  + database        (记录命令落在哪个库)
//	v3  2026-07-30  + operator        (外部审批人,EA4)
//	v4  2026-08-07  + env + tier      (双快照)
//	v5  2026-09-15  time 改为 UTC 归一(迁 PostgreSQL 时的时区无关化)
//
// v5 与前四版不是同一类改动。v1→v4 每次都是**加字段**,所以分支是 `if version >= n`,
// 老行按老分支照样算得出来。v5 改的是**算法**:同一批字段,时间的写法变了。算法改动
// 没有"只对新行生效"的写法 —— 归一化对每一版都生效(见 auditPayloadFor 里那一行,
// 它不在任何 if 里)。版本号在这里的用处只剩一个:让读侧的逐版尝试先试新写法。
//
// 于是 v5 和 v4 对同一行算出的字节完全一样 —— 归一化无条件生效,与这一行的
// OccurredAt 挂什么 Location 无关。("Location 若已是 UTC 才一样"是另一回事:那说的是
// 老库里按**旧 v4 算法**签下的哈希对不对得上,不是这两个版本今天的输出。这个区别要紧:
// 认定 v4/v5 的输出在非 UTC 行上可区分的人,会觉得 TestAuditPayloadVersions_
// EachIsDistinctAndGrows 里那条无条件的「必须完全相同」写错了,顺手加个条件 ——
// 那就把「加了字段却点名成算法版」的那道防线拆了。)
//
// 这不是 bug:matchAuditRow 从新到旧试,新库里的行第一次就命中 v5。
//
// 为什么不把归一化留给 v5、让 v1..v4 保持老写法以救回老行:救不回来。老行的哈希签的是
// **写入进程当时那个 Location** 印出来的字符串,而那个 Location 哪儿也没存 —— 读回来
// 挂什么时区由驱动、列类型和进程 TZ 决定。保留老写法只会让"老行验不验得过"取决于读的
// 时候恰好碰上哪个 Location,也就是把恒假的警报换成随机的警报。这次刻意选了确定的那
// 一边。
//
// 试多个版本不放宽任何东西:每一版都得拿出一个真实的 SHA-256 前像,而那只有当时那个
// 写入器才拿得出来。
//
// 但有一件事必须说清楚:**一行按 v1 校验通过,只说明 v1 里那七个字段没被动过**。它的
// database / operator / env / tier 当时不在哈希里,所以事后被填进去或改掉,这个校验
// 看不出来。报告按版本分别计数,就是为了让看的人知道自己手里的保证到哪一层为止。
const currentAuditPayloadVersion = 5

func auditPayloadFor(a *model.AuditLog, version int) []byte {
	m := map[string]any{
		// UTC 归一 —— 哈希不能跟着读回来的 Location 走。存进 TIMESTAMPTZ 的是绝对
		// 时刻,取出来挂什么时区由驱动和会话决定,而链的定义必须只认那个时刻本身。
		// 不放在 if version >= 5 里:v5 标的是算法变更,对每一版都生效(见上)。
		"time": a.OccurredAt.UTC().Format(time.RFC3339), "actor": a.ActorName,
		"instance": a.Instance, "command": a.Command, "risk": a.Risk,
		"result": a.Result, "ap": a.ApprovalNo,
	}
	if version >= 2 {
		m["database"] = a.Database
	}
	if version >= 3 {
		m["operator"] = a.Operator
	}
	if version >= 4 {
		m["env"] = a.Env
		m["tier"] = a.TierCode
	}
	b, _ := json.Marshal(m)
	return b
}

// auditPayload 是**写入**时用的那一版 —— 永远是最新的。读侧校验用 auditPayloadFor
// 逐版去试。两边共用同一段构造,一处说了算:它们一旦分开写,分叉的表现就是成片的假警报。
func auditPayload(a *model.AuditLog) []byte {
	return auditPayloadFor(a, currentAuditPayloadVersion)
}

// matchAuditRow 找出这一行是按哪一版算的哈希。0 表示哪一版都对不上。
//
// 从新到旧试:今天写进来的行第一次就命中,不必把五版都算一遍。
func matchAuditRow(prev string, a *model.AuditLog) int {
	for v := currentAuditPayloadVersion; v >= 1; v-- {
		if crypto.ChainHash(prev, auditPayloadFor(a, v)) == a.Hash {
			return v
		}
	}
	return 0
}

// ChainReport 是一次校验的结论。
type ChainReport struct {
	OK      bool  `json:"ok"`
	Checked int   `json:"checked"`
	FirstID int64 `json:"firstId"`
	LastID  int64 `json:"lastId"`
	// BrokenID 是第一条对不上的行。0 表示没有。
	BrokenID int64  `json:"brokenId"`
	Reason   string `json:"reason"`
	// ByVersion 按 payload 版本分别计数。看的人据此知道自己手里的保证到哪一层为止:
	// 按 v1 通过的行,它的 database/operator/env/tier 当时不在哈希里。
	//
	// v4 从此报不出来:v4 与 v5 的字节相同,逐版尝试从新往旧走,所以 v4 的行一律记在
	// v5 名下(包括当年真由 v4 写入器写下的老行)。两者覆盖的字段集相同,这个字段要
	// 表达的"保证到哪一层"因此没有变。
	ByVersion map[int]int `json:"byVersion"`
	// Note 说清这个结论覆盖不到什么 —— 一句"链完好"如果让人以为末尾截断也查得出来,
	// 那它就成了假的安全感。
	Note string `json:"note"`
}

const chainNote = "本校验覆盖:内容被改、行被从链中间抽掉。覆盖不到:从链尾整段截断(需把链尾锚到网关之外)。"

// VerifyAuditChain 从头到尾重算一遍审计链。
//
// 两件事分开查,因为它们指向不同的事故:
//   - **接口**:这一行的 prev_hash 是不是上一行的 hash。对不上说明中间少了行,或者顺序被动过。
//   - **内容**:这一行的 hash 能不能用它自己的字段重算出来。对不上说明这一行被改过。
//
// 报第一条对不上的行就停:链是顺序的,第一处断裂之后的"异常"全都是它的回声,继续报只会
// 把真正要看的那一行淹掉。
func (s *Services) VerifyAuditChain(u *model.User) (*ChainReport, error) {
	// 只对监督角色开放,和"看得见全部活动"同一把尺子(auditActor)。
	//
	// 两个理由。其一,它要从创世行一路重算到链尾——一次显式的整表扫描,不该谁都能按。
	// 其二,结论本身是关于**别人**的记录有没有被动过:一个只看得见自己那几行的人,
	// 拿到"第 8231 行被改过"这个答案,得到的是一条他本不该看见的信息。
	if !s.canSeeAllActivity(u) {
		return nil, ErrForbidden
	}
	rows, err := s.Repo.AuditChainRows()
	if err != nil {
		return nil, err
	}
	rep := &ChainReport{OK: true, Checked: len(rows), Note: chainNote, ByVersion: map[int]int{}}
	if len(rows) == 0 {
		return rep, nil
	}
	rep.FirstID, rep.LastID = rows[0].ID, rows[len(rows)-1].ID

	prev := "" // 创世行的前驱是空串
	for i := range rows {
		a := &rows[i]
		if a.PrevHash != prev {
			rep.OK = false
			rep.BrokenID = a.ID
			if i == 0 {
				rep.Reason = "链的第一行不是创世行:它的 prev_hash 不为空,说明它前面本来还有行"
			} else {
				rep.Reason = "这一行接不上上一行:prev_hash 与上一行的 hash 不符,中间有行被删掉或顺序被动过"
			}
			return rep, nil
		}
		v := matchAuditRow(prev, a)
		if v == 0 {
			rep.OK = false
			rep.BrokenID = a.ID
			rep.Reason = "这一行的内容与它自己的哈希对不上:写下之后被改过"
			return rep, nil
		}
		rep.ByVersion[v]++
		prev = a.Hash
	}
	return rep, nil
}
