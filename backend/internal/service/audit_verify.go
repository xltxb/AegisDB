package service

// 审计链的**校验**这一头。
//
// 在这个文件出现之前,哈希链只有写的那一半:每一行都算了、也存了,但没有任何地方去验它。
// 那种状态下它提供的是"可验证性",而界面上写的是"不可篡改" —— 产品宣称了一个自己从不
// 检查的属性,而这比不写还糟:出事时没有人知道该去验,真去验了才发现得现写脚本。
//
// 这里补上的就是那一半:从一个已知的起点往后逐行重算,第一处对不上的行号摆出来。
//
// 三件事值得单独说:
//
//  1. **分代**。载荷的键集随迁移长过(0039 的 event、0040 的 action、0041 的 ip),老行
//     只能按它写下那天的键集重算。这里从新到老逐代试,第一代对上就算这一行过。
//  2. 逐代试会开一个小口子:把一行的 ip 抹掉、按第四代重算,它也能"验过"。所以再加一条
//     **代不能倒退** —— 链上后面的行不能比前面的行更老。真要伪造,得把它之后的每一行
//     连同代一起重写,那已经不是"改一行"了。
//  3. 这一条**挡不住有备而来的重写**:载荷是裸 SHA-256、算法全公开,拿得到库写权限的人
//     可以把整条尾巴重算一遍,链照样自洽(白盒审计 L-12)。要挡那个得给载荷加密钥
//     (HMAC),那是另一件事,而且会让老行验不过、同样要分代。这里先把"能验"变成"验得了"。

import (
	"fmt"
	"time"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
	"aegisdb/internal/repository"
	"aegisdb/pkg/crypto"
)

// NewForOps 是给**运维子命令**用的最小 Services:只接一个仓储,别的什么都不起。
//
// New() 会顺带拉起导出、异步执行、发布三组 worker(一堆常驻 goroutine)、还会去认领
// 上一轮遗留的任务 —— 那些是"跑起来提供服务"要做的事。而 `verify-audit` 只是读一遍表
// 然后退出,在一个可疑的、甚至是从备份恢复出来的库上启动一整套后台任务,是主动去改它。
func NewForOps(repo *repository.Repo) *Services { return &Services{Repo: repo} }

// auditVerifyBatch 是一次从库里取多少行来验。
//
// 整条链可能有几十万行,一次全捞进内存既慢又可能把进程撑爆;一行一行取又是几十万次往返。
// 按批走,内存是常数。
const auditVerifyBatch = 500

// AuditChainReport 是一次校验的结论。
//
// 它要能回答三句话:验了多少行、过没过、第一处对不上的是哪一行。**不返回"有几处坏"** ——
// 链是前后咬合的,第一处断掉之后,它后面的每一行都会跟着对不上,数出来的那个数字没有意义,
// 只会让人以为损坏面很大。
type AuditChainReport struct {
	// Checked 实际验过的行数。
	Checked int64 `json:"checked"`
	// Total 表里一共有多少行(Checked < Total 表示只验了最近一段)。
	Total int64 `json:"total"`
	OK    bool  `json:"ok"`
	// FromID / ToID 这次验的区间(闭区间;表空时都是 0)。
	FromID int64 `json:"fromId"`
	ToID   int64 `json:"toId"`
	// 第一处对不上的行。OK 为 true 时都是零值。
	BadID     int64     `json:"badId"`
	BadAt     time.Time `json:"badAt"`
	BadActor  string    `json:"badActor"`
	BadReason string    `json:"badReason"` // content | link | gen
	// Gen 最后验过的那一行用的是第几代键集。它会随迁移往上走,不是错误。
	Gen int `json:"gen"`
	// RolloverRows 按"毫秒进位"放行的行数(见 auditRolloverHit)。不是错误,但要摆出来:
	// 这些行是按比库里早 1 秒的时间验过的,看报告的人应当知道放宽发生在哪儿、发生了几次。
	RolloverRows int64 `json:"rolloverRows"`
}

// 三种对不上,分别指向不同的事,所以要分开说:
//
//	content  这一行的内容被改过(重算出来的哈希和存着的不一样)
//	link     它的 prev_hash 没接住上一行 —— 中间有行被删掉,或者被插了一行
//	gen      它按比前面的行更老的键集才算得过 —— 有人抹掉了较新的字段再重算
const (
	auditBadContent = "content"
	auditBadLink    = "link"
	auditBadGen     = "gen"
)

// CanVerifyAuditChain 判谁按得动这一下。
//
// 与审计页的可见性不是一条线:那一条按人收窄("没有监督角色只看得见自己的行"),而校验
// 必须扫全表 —— 链是一条,少看几行就验不成。所以门槛单独立在角色上,用的是同一个监督角色
// 口径(admin / owner / audit)。它同时也是一趟全表扫描,不该是随便谁都能按着玩的按钮。
func (s *Services) CanVerifyAuditChain(u *model.User) bool { return s.canSeeAllActivity(u) }

// VerifyAuditChain 重算审计链并报告第一处对不上的行。
//
// limit > 0 只验**最近** limit 行(界面上按一下就要有结果,而整表可能很大);limit <= 0
// 验全表(命令行用这个)。只验一段同样是有意义的:每一行的哈希覆盖的是它自己的内容加上
// 前一行的哈希,所以窗口内的任何改动都跑不掉 —— 跑掉的只是窗口之外的行。
func (s *Services) VerifyAuditChain(limit int) (*AuditChainReport, error) {
	total, err := s.Repo.CountAudit()
	if err != nil {
		return nil, err
	}
	rep := &AuditChainReport{OK: true, Total: total}
	if total == 0 {
		return rep, nil
	}

	// 窗口的起点,以及**它前面那一行存着的哈希** —— 第一行的 prev_hash 要接的就是它。
	// 取不到(说明窗口是从链头开始的)时,期望值是空串:创世行的前驱就是空。
	startID, prevHash, err := s.auditVerifyAnchor(limit)
	if err != nil {
		return nil, err
	}

	gen := auditGenOldest
	afterID := startID - 1
	for {
		rows, err := s.Repo.AuditRowsAsc(afterID, auditVerifyBatch)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			a := &rows[i]
			afterID = a.ID
			if rep.FromID == 0 {
				rep.FromID = a.ID
			}
			rep.ToID = a.ID
			rep.Checked++

			// 一、它接住上一行了吗。
			if a.PrevHash != prevHash {
				rep.markBad(a.ID, a.OccurredAt, a.ActorName, auditBadLink)
				return rep, nil
			}
			// 二、它的内容还是当初那份吗 —— 从新到老逐代试。
			hitGen := auditContentGen(a)
			if hitGen == 0 {
				if hitGen = auditRolloverHit(a); hitGen != 0 {
					rep.RolloverRows++
				}
			}
			if hitGen == 0 {
				rep.markBad(a.ID, a.OccurredAt, a.ActorName, auditBadContent)
				return rep, nil
			}
			// 三、代不能倒退。
			if hitGen < gen {
				rep.markBad(a.ID, a.OccurredAt, a.ActorName, auditBadGen)
				return rep, nil
			}
			gen = hitGen
			prevHash = a.Hash
		}
		if len(rows) < auditVerifyBatch {
			break
		}
	}
	rep.Gen = gen
	return rep, nil
}

// auditContentGen 从新到老逐代重算,返回对得上的那一代;都对不上返回 0。
func auditContentGen(a *model.AuditLog) int {
	for g := auditGenCurrent; g >= auditGenOldest; g-- {
		if crypto.ChainHash(a.PrevHash, auditPayloadGen(a, g)) == a.Hash {
			return g
		}
	}
	return 0
}

// auditRolloverHit 认领修复前留下的**毫秒进位行**(见 service.go 的 auditStamp)。
//
// 那时写审计用的是纳秒时间,而 occurred_at 是 DATETIME(3),MySQL 入库四舍五入:小数秒
// 够大的那一行被进到下一秒,读回来恰好是一个**整秒**(毫秒位全零),哈希却是按前一秒算的。
//
// 放行的条件收得很窄,三条同时满足才算:
//   - 读回的时间毫秒位为零 —— 进位的结果只可能是这个样子;
//   - 把时间退回 1 秒,其它字段一个不动,某一代载荷就能对上;
//   - 链接、代不倒退两道检查照旧(在调用方)。
//
// 它放宽的只有一件事:能改库的人可以把这类行的时间"往后挪正好 1 秒、且落在整秒上"而不被
// 发现。挪别的幅度、改任何别的字段,照样报 content。用户 2026-11-16 定的取舍:老行坏得
// 不是被人改的,链却因此整条报断,那比这 1 秒的口子更误事。
func auditRolloverHit(a *model.AuditLog) int {
	if a.OccurredAt.Nanosecond() != 0 {
		return 0
	}
	b := *a
	b.OccurredAt = a.OccurredAt.Add(-time.Second)
	return auditContentGen(&b)
}

func (r *AuditChainReport) markBad(id int64, at time.Time, actor, reason string) {
	r.OK = false
	r.BadID, r.BadAt, r.BadActor, r.BadReason = id, at, actor, reason
}

// auditVerifyAnchor 定出窗口的起点,以及起点之前那一行**存着的**哈希。
//
// 只验最近一段时,第一行的 prev_hash 要和它前面那一行对上,而那一行不在窗口里 —— 所以
// 得单独把它的哈希取出来当锚。锚取的是**存着的值**,不是重算的:窗口之外的行这一趟本来
// 就不负责验。
func (s *Services) auditVerifyAnchor(limit int) (startID int64, prevHash string, err error) {
	if limit <= 0 {
		return 1, "", nil // 从头验:创世行的前驱是空串
	}
	startID, err = s.Repo.AuditIDAtOffsetFromNewest(limit - 1)
	if err != nil || startID == 0 {
		return 1, "", err
	}
	prev, err := s.Repo.AuditRowBefore(startID)
	if err != nil {
		return 0, "", err
	}
	if prev == nil {
		return 1, "", nil // 窗口已经包住了链头
	}
	return startID, prev.Hash, nil
}

// AuditPayloadGen 是 auditPayloadGen 的导出形态,连同 AuditGenRange 一起交给**链外**的
// 核对工具:把导出的 CSV、或者 SIEM 那边收到的 raw_payload,拿回来重算一遍。
//
// 导出它,是因为这条链最有用的用法本来就发生在这套代码之外 —— 网关自己的库是可写的,而
// 推到 SIEM / 数据湖里的那份是只进不出的,两边对一下才叫核对。让外面的人照着注释再实现
// 一遍载荷,等于把"两份实现迟早会不一致"写进设计里。
func AuditPayloadGen(a *model.AuditLog, gen int) []byte { return auditPayloadGen(a, gen) }

// AuditGenRange 给出逐代重试的范围(最老、当前)。
func AuditGenRange() (oldest, current int) { return auditGenOldest, auditGenCurrent }

// AuditChainReason 把机器读的那三个取值翻成一句人话,命令行与接口共用。
func AuditChainReason(reason string) string {
	switch reason {
	case auditBadContent:
		return apimsg.Text("这一行的内容被改过(重算的哈希与存着的不一致)")
	case auditBadLink:
		return apimsg.Text("链断了:它的前驱哈希接不上上一行 —— 中间有行被删除或插入")
	case auditBadGen:
		return apimsg.Text("这一行按比前面更老的键集才算得过 —— 像是有人抹掉了较新的字段再重算")
	default:
		return ""
	}
}

// String 给命令行用的一行结论。
func (r *AuditChainReport) String() string {
	if r.OK {
		s := fmt.Sprintf("审计链完好:验过 %d / %d 行(id %d..%d),当前键集第 %d 代",
			r.Checked, r.Total, r.FromID, r.ToID, r.Gen)
		if r.RolloverRows > 0 {
			s += fmt.Sprintf("\n其中 %d 行是旧版本写入时的毫秒进位(库里时间比哈希晚整 1 秒),已按前一秒核对通过", r.RolloverRows)
		}
		return s
	}
	return fmt.Sprintf("审计链在第 %d 行断开(%s · 操作人 %s · id=%d):%s\n已验过 %d / %d 行(id %d..%d)",
		r.Checked, r.BadAt.Format(time.RFC3339), r.BadActor, r.BadID, AuditChainReason(r.BadReason),
		r.Checked, r.Total, r.FromID, r.ToID)
}
