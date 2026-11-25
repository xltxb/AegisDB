package model

import "time"

// OSCJob 是一次生产大表的在线加 / 删索引(.scratch/osc-product,ADR 0011)。
//
// 进度由运行器每隔几秒从引擎的 Status 落到这里,页面读库不读进程内存 —— 刷新、换人看都一样;
// 网关重启时进行中的任务:有检查点的判 interrupted(影子表保留,人工续跑 / 放弃),其余判失败。
type OSCJob struct {
	ID           int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	ConnectionID int64  `gorm:"index:idx_osc_job_conn;not null" json:"connectionId"`
	Instance     string `gorm:"size:64;not null" json:"instance"`
	Database     string `gorm:"column:db_name;size:128;not null" json:"database"`
	Table        string `gorm:"column:tbl_name;size:64;not null" json:"table"`
	// Statement 是发起人写的原文(审计与界面显示);ChangeJSON 是引擎要的结构化变更(osc.Change)。
	Statement  string `gorm:"type:text;not null" json:"statement"`
	ChangeJSON string `gorm:"column:change_json;type:text;not null" json:"-"`
	// 来源:终端发起的挂在审批单上,流水线发起的挂在发布单上。
	ApprovalID  int64  `gorm:"not null;default:0;index:idx_osc_job_approval" json:"approvalId,omitempty"`
	ApNo        string `gorm:"size:32" json:"apNo,omitempty"`
	ReleaseID   int64  `gorm:"not null;default:0" json:"releaseId,omitempty"`
	InitiatorID int64  `gorm:"not null" json:"initiatorId"`
	Initiator   string `gorm:"size:64" json:"initiator"`
	// 判定时读到的数据量(估算)与前置检查的警告(JSON 数组),给审批人看。
	SizeRows  int64  `gorm:"not null;default:0" json:"sizeRows"`
	SizeBytes int64  `gorm:"not null;default:0" json:"sizeBytes"`
	Warnings  string `gorm:"type:text" json:"warnings,omitempty"`

	Status string `gorm:"size:16;not null;index:idx_osc_job_status" json:"status"` // OSC* 常量
	Phase  string `gorm:"size:16" json:"phase"`
	// 进度快照(osc.Status)
	RowsEstimated   int64  `gorm:"not null;default:0" json:"rowsEstimated"`
	RowsCopied      int64  `gorm:"not null;default:0" json:"rowsCopied"`
	Chunks          int    `gorm:"not null;default:0" json:"chunks"`
	RowsReplayed    int64  `gorm:"not null;default:0" json:"rowsReplayed"`
	CutOverAttempts int    `gorm:"not null;default:0" json:"cutOverAttempts"`
	Throttled       bool   `gorm:"not null;default:false" json:"throttled"`
	// ThrottleReason:为什么在限流(主库负载 / 从库延迟 / 从库延迟未知),迁移 0050
	ThrottleReason string `gorm:"size:255;not null;default:''" json:"throttleReason,omitempty"`
	// ThrottleReasonRef:ThrottleReason 的机器可读身份(JSON 的 RuleRef,apiMsg;迁移 0061)
	ThrottleReasonRef string `gorm:"type:text" json:"throttleReasonRef,omitempty"`
	// Checkpoint:引擎的检查点(osc.Checkpoint 的 JSON),续跑用;界面不需要,迁移 0051
	Checkpoint string `gorm:"type:text" json:"-"`
	Paused          bool   `gorm:"not null;default:false" json:"paused"`
	Error           string `gorm:"type:text" json:"error,omitempty"`
	// ErrorRef 是 Error 那句话的机器可读身份(JSON 的 RuleRef,apiMsg / outText,迁移 0060),界面按读者语言显示;
	// 空 = 固定句(读出时按原句补)或 0060 之前的老行(界面回落到原串)。
	ErrorRef string `gorm:"type:text" json:"errorRef,omitempty"`
	// OldTable:成功切换后保留下来的原表(_t_del),由 DBA 择时手动删除。
	OldTable string `gorm:"size:64" json:"oldTable,omitempty"`

	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func (OSCJob) TableName() string { return "tbl_osc_job" }

// OSCJob.Status
const (
	OSCAwaiting = "awaiting" // 已建,等审批 / 等人点执行
	OSCRunning  = "running"
	OSCDone     = "done"
	OSCFailed   = "failed"
	OSCAborted  = "aborted"
	// OSCInterrupted:网关重启打断了迁移,影子表与检查点都在,等人「续跑」或「放弃」
	OSCInterrupted = "interrupted"
)

// OSCJobActive 说这个状态是否还占着这张表(同一张表同时只允许一个进行中的迁移)。
func OSCJobActive(status string) bool { return status == OSCRunning }
