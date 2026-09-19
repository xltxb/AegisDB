package osc

import "time"

// JobStatus 是一次迁移走到哪了。
//
// 它不是给进度条看的,是给**收拾残局的人**看的。一次迁移会跑几小时,期间可能被重启、
// 被中止、被网络打断,而 ADR 0011 最担心的正是这个场景:
//
//	「一个跑到一半的迁移留下的是影子表和一段没追平的 binlog,而人以为自己加了个索引」
//
// 所以状态落库,并且区分得出「还在跑」与「已经结束」—— 分不清的话,一条死在 copying
// 的记录会被当成正在进行,没人去清它留下的影子表。
type JobStatus string

const (
	JobPending   JobStatus = "pending"   // 刚建,还没开跑
	JobPreflight JobStatus = "preflight" // 正在做前置检查
	JobCopying   JobStatus = "copying"   // 分块拷贝存量行
	JobReplaying JobStatus = "replaying" // 等重放追平
	JobCutOver   JobStatus = "cutover"   // 正在切换
	JobDone      JobStatus = "done"
	JobFailed    JobStatus = "failed"
	JobAborted   JobStatus = "aborted"
)

// Unfinished 报告这条记录是不是还没走到终点。
// 重启之后靠它把残局挑出来。
func (s JobStatus) Unfinished() bool {
	switch s {
	case JobDone, JobFailed, JobAborted:
		return false
	}
	return true
}

// next 是每个状态**正常情况下**能去的下一站。
//
// 顺序是固定的,而且不许跳:copying 直接到 done 意味着变更期间的写入一条都没重放,
// 而界面会显示「完成」;pending 直接到 cutover 意味着换上去的是一张空表。
var next = map[JobStatus]JobStatus{
	JobPending:   JobPreflight,
	JobPreflight: JobCopying,
	JobCopying:   JobReplaying,
	JobReplaying: JobCutOver,
	JobCutOver:   JobDone,
}

// canTransition 判断一次状态跃迁是否允许。
//
// 两条规则:正常推进只能走 next 那一格;**任何还在跑的阶段都可以失败或被中止** ——
// 后者是「可中止」这件事的前提,而可中止正是这套东西相对原生 DDL 的卖点之一。
// 终态不再动:一条已完成的迁移被改回 copying,会让它再切换一次,而那时原表已经是新表。
func canTransition(from, to JobStatus) bool {
	if !from.Unfinished() {
		return false
	}
	if to == JobFailed || to == JobAborted {
		return true
	}
	return next[from] == to
}

// Job 是落库的一行:一次在线变更的全部状态。
//
// 表名 tbl_osc_job,字段随 migrations/0002_osc_job.sql。
type Job struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ConnectionID int64     `gorm:"not null;index" json:"connectionId"`
	Schema       string    `gorm:"column:schema_name;size:64;not null" json:"schema"`
	Table        string    `gorm:"column:table_name;size:64;not null" json:"table"`
	Alter        string    `gorm:"column:alter_clause;size:512;not null" json:"alter"`
	Status       JobStatus `gorm:"size:16;not null" json:"status"`

	// Shadow 是影子表名。**失败之后要靠它找到残留**,所以一建出来就写库,
	// 而不是等成功了再记。
	Shadow string `gorm:"size:64;not null;default:''" json:"shadow"`

	// Throttle 是这一次的限流留痕:开着(几个从库、什么阈值),或者没开起来(为什么)。
	//
	// 记的是事实,不是能力。同一套代码在单机实例上跑就是不限流的,而那件事只有
	// 当时那个进程知道 —— 事后从库被拖垮时,这一列是唯一答得上"当时限流开着吗"的
	// 地方。
	Throttle string `gorm:"size:255;not null;default:''" json:"throttle"`

	// Throttled 是同一件事的**布尔面**:这次到底限没限流。
	//
	// 与 Throttle 分开落库,是为了不让界面去解析那句人话。按中文前缀判断"未启用"
	// 的话,后端改一次文案,界面上的警示就悄悄没了 —— 而它恰恰是最不该丢的那一条。
	Throttled bool `gorm:"not null;default:false" json:"throttled"`

	CopiedRows int64  `gorm:"not null;default:0" json:"copiedRows"`
	TotalRows  int64  `gorm:"not null;default:0" json:"totalRows"`
	Err        string `gorm:"size:1024;not null;default:''" json:"err"`

	CreatedBy  string     `gorm:"size:64;not null;default:''" json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	FinishedAt *time.Time `json:"finishedAt"`

	// Running 不落库(`gorm:"-"`):它是**本进程此刻**有没有在推进这条任务,由接口层
	// 从 Runner 填。
	//
	// 库里一条 copying 的记录,可能正在跑,也可能是上个进程死在半路留下的 —— status
	// 分不开这两件事,而界面要靠它决定给不给「中止」、要不要列进「需要人工收拾」。
	Running bool `gorm:"-" json:"running"`
}

func (Job) TableName() string { return "tbl_osc_job" }
