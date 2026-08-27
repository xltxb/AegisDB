package service

// 审批工单的"谁能决定它" —— 全系统唯一的一处判断。
//
// 它同时服务两个调用点:待办列表(决定按钮亮不亮)和真正的审批动作(决定放不放行)。
// 这一点是刻意的:把同一条规则在服务端判一次、又在前端判一次,两份判断迟早会不
// 一致,而不一致的那一次就是一个亮着却点不动的按钮 —— 点了没反应,还没人说得出
// 为什么。
//
// 它返回的是**理由**而不是布尔值。"不能审"有好几种原因,它们要人做的事完全不同:
// 不在审批链上要去找管理员,自己发起的要去找同事,已处理的只需要刷新页面。一个
// 笼统的拒绝会让人去修错的东西。

import "velagateway/internal/model"

// DecideBlock 说明为什么这个人此刻不能决定这张工单。空字符串 = 可以决定。
type DecideBlock string

const (
	BlockNone       DecideBlock = ""
	BlockNotPending DecideBlock = "settled"   // 已审批 / 已驳回 / 已过期
	BlockSelf       DecideBlock = "self"      // 自己发起的工单(两人控制)
	BlockNotInChain DecideBlock = "not-chain" // 不在这张工单的审批链上
)

// Reason 是给人看的说法。它必须点出**具体是哪一条**,否则等于没说。
func (b DecideBlock) Reason() string {
	switch b {
	case BlockNotPending:
		return "该工单已被处理,请刷新"
	case BlockSelf:
		return "不能审批自己发起的工单(两人控制),请由审批链上的其他成员处理"
	case BlockNotInChain:
		return "仅审批链成员可处理该工单"
	}
	return ""
}

// DecideBlockFor answers whether u may decide ap right now, and if not, why.
//
// 判断顺序就是要说给人听的顺序:先看这张单还在不在待办里(已处理的谁都动不了),
// 再看是不是自己发起的(他在链上,说他"不是链成员"是指错方向),最后才看链成员。
func (s *Services) DecideBlockFor(u *model.User, ap *model.Approval) DecideBlock {
	if u == nil || ap == nil {
		return BlockNotInChain
	}
	if ap.Status != model.StatusPending {
		return BlockNotPending
	}
	// 两人控制(R16):发起人不能决定自己的工单,除非管理员显式开了自审批开关
	// (小团队 / 单人运维)。默认关闭,保住职责分离。
	if u.ID == ap.InitiatorID && !s.settingBool("approval.allowSelfApprove", false) {
		return BlockSelf
	}
	if !s.isChainMember(ap.ID, u) {
		return BlockNotInChain
	}
	return BlockNone
}

// CanDecide is the boolean the console needs on every row.
func (s *Services) CanDecide(u *model.User, ap *model.Approval) bool {
	return s.DecideBlockFor(u, ap) == BlockNone
}

// DecideRefusal carries WHICH rule refused the decision, so the handler can say
// it out loud instead of collapsing every refusal into one sentence.
//
// It is a distinct type rather than one of the package's sentinel errors because
// the reason is the payload: callers that only need "forbidden" can still test
// for it, but the message reaches the operator intact.
type DecideRefusal struct{ Block DecideBlock }

func (e *DecideRefusal) Error() string { return e.Block.Reason() }

// Unwrap lets existing `err == ErrForbidden` call sites keep working through
// errors.Is — a refusal IS a forbidden, it just knows more.
func (e *DecideRefusal) Unwrap() error { return ErrForbidden }
