package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/apimsg"
	"aegisdb/internal/model"
)

// ApprovalOutcome 是一张审批单被决定或撤回之后、和状态一起落库的那几笔。
//
// 它们必须和抢占状态在**同一个事务**里:先抢到状态、再分开写,任何一笔失败都会留下一张
// "已批准"却没生效的单 —— 最坏的是执行窗口单:单子和通知都说窗口已生效,窗口本身还停在
// 待审批,到点不会开(#143)。放进一个事务,失败就整体回滚,单子还在原状态,调用方拿到错误。
type ApprovalOutcome struct {
	// ApproverID 是实际做决定的人,步骤记在他那一步上(见 DecideStepBy);0 表示记在 active 那一步。
	ApproverID int64
	StepStatus string // 那一步改成什么:approved | rejected | cancelled
	Output     string
	OutputRef  *model.RuleRef
	Rows       int
	At         time.Time
	// WindowID > 0 时,窗口挂着的正是这张单,且窗口仍在待审批,才把它改成 WindowStatus。
	// 窗口已被改过(挂着别的单)或已不是待审批,不算错:那是另一条路径已经收的场。带上
	// status = pending,是因为通过 / 驳回与撤回可能同时到达,一张已决定的窗口不能被第二次覆盖。
	WindowID     int64
	WindowStatus string
}

// DecideApprovalAtomic 抢占一张待审批的单(pending → to,渠道与拒绝原因同一条 UPDATE,见
// ClaimApprovalDecision),并在同一个事务里写下 o。返回是否抢到;抢输时什么都不写。
func (r *Repo) DecideApprovalAtomic(id int64, to, via, rejectReason string, o ApprovalOutcome) (bool, error) {
	return r.settleApproval(id, o, func(tx *gorm.DB) *gorm.DB {
		return tx.Model(&model.Approval{}).
			Where("id = ? AND status = ?", id, model.StatusPending).
			Updates(map[string]any{"status": to, "decided_via": via, "reject_reason": rejectReason})
	})
}

// CancelApprovalAtomic 撤回一张还没跑过的单,并在同一个事务里写下 o。
//
// 它比 ClaimApproval(from→to) 多管一件事:executed_at IS NULL。
//
// 待执行的单子(已批准、尚未执行)也能撤,于是撤回和执行成了两个抢同一张单的动作。
// 只按 status 做 CAS 是不够的:执行那一路先占的是 executed_at(ClaimApprovalExecution),
// 它占住的那一刻 status 仍然是 approved —— 撤回照样能改成 cancelled,而那条命令已经
// 发出去了。库里于是留下一张"已撤回"的单,和一次真实发生过的下发。
//
// 两条路都必须在**同一行、同一次原子更新**里较量:执行占 executed_at 且要求
// status=approved;撤回改 status 且要求 executed_at IS NULL。谁先谁赢,另一个拿到
// RowsAffected=0。
func (r *Repo) CancelApprovalAtomic(id int64, o ApprovalOutcome) (bool, error) {
	return r.settleApproval(id, o, func(tx *gorm.DB) *gorm.DB {
		return tx.Model(&model.Approval{}).
			Where("id = ? AND status IN ? AND executed_at IS NULL",
				id, []string{model.StatusPending, model.StatusApproved}).
			Update("status", model.StatusCancelled)
	})
}

// CancelPendingApprovalAtomic 只撤回仍在待审批的单(pending → cancelled),并在同一个事务里写下 o。
func (r *Repo) CancelPendingApprovalAtomic(id int64, o ApprovalOutcome) (bool, error) {
	return r.settleApproval(id, o, func(tx *gorm.DB) *gorm.DB {
		return tx.Model(&model.Approval{}).
			Where("id = ? AND status = ?", id, model.StatusPending).
			Update("status", model.StatusCancelled)
	})
}

// errClaimLost 只在事务内部用来回滚并告诉外层"没抢到"。
var errClaimLost = errors.New("approval claim lost")

func (r *Repo) settleApproval(id int64, o ApprovalOutcome, claim func(tx *gorm.DB) *gorm.DB) (bool, error) {
	err := r.db.Transaction(func(tx *gorm.DB) error {
		res := claim(tx)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errClaimLost
		}
		if err := decideStepBy(tx, id, o.ApproverID, o.StepStatus, o.At); err != nil {
			return err
		}
		if err := tx.Model(&model.Approval{}).Where("id = ?", id).Updates(map[string]any{
			"result": o.Output, "result_ref": apimsg.RefJSON(o.OutputRef), "result_rows": o.Rows, "decided_at": o.At,
		}).Error; err != nil {
			return err
		}
		if o.WindowID > 0 {
			if err := tx.Model(&model.ExecWindow{}).
				Where("id = ? AND status = ? AND approval_id = ?", o.WindowID, model.WindowPending, id).
				Updates(map[string]any{"status": o.WindowStatus, "decided_at": o.At}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errClaimLost) {
		return false, nil
	}
	return err == nil, err
}
