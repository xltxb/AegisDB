package osc

import "testing"

// 一次迁移会跑几小时,而它中途会被重启、被中止、被网络打断。状态机决定的是
// **重启之后那条记录还能不能被看懂** —— 这正是 ADR 0011 最担心的场景:
//
//   「一个跑到一半的迁移留下的是影子表和一段没追平的 binlog,而人以为自己加了个索引」
//
// 所以状态不是给进度条看的,是给**收拾残局的人**看的。

func TestJobStatus_TheHappyPathGoesThroughEveryStage(t *testing.T) {
	// 顺序是固定的:检查 → 建影子表 → 拷贝 → 追平 → 切换 → 完成。
	// 跳过任何一步都意味着有一段数据没被搬过去。
	seq := []JobStatus{
		JobPending, JobPreflight, JobCopying, JobReplaying, JobCutOver, JobDone,
	}
	for i := 1; i < len(seq); i++ {
		if !canTransition(seq[i-1], seq[i]) {
			t.Errorf("%s → %s 应当允许", seq[i-1], seq[i])
		}
	}
}

// 跳步是最危险的一类错误:copying 直接到 done,意味着变更期间的写入一条都没重放,
// 而界面会显示「完成」。
func TestJobStatus_SkippingAStageIsRefused(t *testing.T) {
	for _, tc := range []struct{ from, to JobStatus }{
		{JobCopying, JobDone},    // 没追平就完成 = 丢掉变更期间的写入
		{JobPending, JobCutOver}, // 没拷贝就切换 = 换上一张空表
		{JobPreflight, JobDone},
		{JobReplaying, JobPending}, // 不能倒退
	} {
		if canTransition(tc.from, tc.to) {
			t.Errorf("%s → %s 不该允许", tc.from, tc.to)
		}
	}
}

// 任何一个还在跑的阶段都可以失败或被中止 —— 这是"可中止"这件事的前提。
func TestJobStatus_AnyRunningStageCanFailOrBeAborted(t *testing.T) {
	for _, from := range []JobStatus{JobPreflight, JobCopying, JobReplaying, JobCutOver} {
		for _, to := range []JobStatus{JobFailed, JobAborted} {
			if !canTransition(from, to) {
				t.Errorf("%s → %s 应当允许:跑着的阶段必须能停下来", from, to)
			}
		}
	}
}

// 终态就是终态。一条已经完成的迁移被改成 copying,意味着它会再切换一次 ——
// 而那时原表已经是新表了。
func TestJobStatus_TerminalStatesDoNotMoveAgain(t *testing.T) {
	for _, from := range []JobStatus{JobDone, JobFailed, JobAborted} {
		for _, to := range []JobStatus{JobPending, JobCopying, JobCutOver, JobDone} {
			if canTransition(from, to) {
				t.Errorf("%s 是终态,不该再跃迁到 %s", from, to)
			}
		}
	}
}

// 重启之后要分得清「还在跑」和「已经结束」。分不清的话,一条死在 copying 的记录
// 会被当成正在进行,没人去收拾它留下的影子表。
func TestJobStatus_UnfinishedWorkIsRecognisable(t *testing.T) {
	for _, s := range []JobStatus{JobPending, JobPreflight, JobCopying, JobReplaying, JobCutOver} {
		if !s.Unfinished() {
			t.Errorf("%s 是未完成状态", s)
		}
	}
	for _, s := range []JobStatus{JobDone, JobFailed, JobAborted} {
		if s.Unfinished() {
			t.Errorf("%s 已经结束了", s)
		}
	}
}
