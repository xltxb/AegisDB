package handler

import (
	"errors"

	"github.com/gin-gonic/gin"

	"velagateway/internal/middleware"
	"velagateway/internal/osc"
	"velagateway/pkg/resp"
)

// MySQL 在线表结构变更(ADR 0011)的接口。
//
// 整条链路是:前置检查 → 建影子表 → 分块拷贝 → binlog 回放 → 原子改名。它跑在
// **生产库**上,所以这里的每一个决定都偏向「宁可拒绝」。

// oscGate 是接口在运行期需要知道的两件事:runner,以及这套东西现在开没开。
//
// Enabled 是个函数而不是一个 bool —— 开关住在 bootstrap 的 Config 里,而 bootstrap
// 反过来 import 这个包,直接传配置结构就成了循环导入;传一个读取函数既避开了它,
// 也让开关在**请求发生时**才被读到。
type oscGate struct {
	runner  *osc.Runner
	enabled func() bool
}

// AttachOSC 接上在线变更。不接的话这些路由压根不该注册。
func (h *Handler) AttachOSC(runner *osc.Runner, enabled func() bool) {
	h.osc = &oscGate{runner: runner, enabled: enabled}
}

// oscCaveats 是这套东西**当前**还没做到的事,一字不落地送到界面上。
//
// 它不写在文档里而是从接口返回,理由很直接:要按下"开始迁移"的那个人不会去读 ADR。
// 这里每加一条,发起页面上就多一行警告。做完一件就删一条 —— 别让它变成没人看的样板。
func oscCaveats() []string {
	return []string{
		"没有从库延迟限流:MaxLag 目前没有数据源,全表拷贝不会因为从库追不上而自己减速。" +
			"主从架构下,一张大表可能把从库拖出可观的延迟。",
		"只验过 ADD INDEX 一类不改变行内容的变更;改列类型、改字符集的语义没有覆盖。",
		"切换会保留原表(改名成 _del 后缀),磁盘空间不会立刻释放,需要人工确认后再删。",
	}
}

// OSCStatus 告诉控制台这套东西现在能提供什么 —— 省得界面自己去猜、或者把警告
// 硬编码一份在前端。
func (h *Handler) OSCStatus(c *gin.Context) {
	resp.OK(c, gin.H{
		"enabled": h.osc != nil && h.osc.enabled(),
		"caveats": oscCaveats(),
	})
}

// OSCJobs 列出最近的迁移任务。
//
// **关着的时候也读得到**:一次半途失败留下的影子表和没追平的 binlog,恰恰是在
// 人关掉开关之后最需要被看见的东西。把列表也一起锁上,等于把残局藏起来。
func (h *Handler) OSCJobs(c *gin.Context) {
	if h.osc == nil {
		resp.OK(c, []osc.Job{})
		return
	}
	jobs, err := h.osc.runner.Recent(c.Request.Context(), 50)
	if err != nil {
		resp.Fail(c, resp.CodeInternalError, "读取迁移任务失败:"+err.Error())
		return
	}
	// running 不在库里 —— 它是本进程此刻的事实。没有它的话,一条死在 copying 的记录
	// 和一条正在 copying 的记录在界面上长得一模一样。
	for i := range jobs {
		jobs[i].Running = h.osc.runner.IsRunning(jobs[i].ID)
	}
	resp.OK(c, jobs)
}

// OSCJob 读一条任务,给界面轮询进度用。
func (h *Handler) OSCJob(c *gin.Context) {
	if h.osc == nil {
		resp.Fail(c, resp.CodeNotFound, "任务不存在")
		return
	}
	job, err := h.osc.runner.Get(c.Request.Context(), pathID(c))
	if err != nil {
		resp.Fail(c, resp.CodeNotFound, "任务不存在")
		return
	}
	job.Running = h.osc.runner.IsRunning(job.ID)
	resp.OK(c, job)
}

// OSCStart 发起一次在线变更。
func (h *Handler) OSCStart(c *gin.Context) {
	if h.osc == nil || !h.osc.enabled() {
		resp.Fail(c, resp.CodeOscDisabled,
			"在线表结构变更尚未启用。它还缺少从库延迟限流,在主从环境下可能把从库拖垮;"+
				"确认风险后由管理员在配置里打开 osc.enabled。")
		return
	}
	var req struct {
		ConnectionID int64  `json:"connectionId"`
		Schema       string `json:"schema"`
		Table        string `json:"table"`
		Alter        string `json:"alter"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Fail(c, resp.CodeBadRequest, "参数有误:"+err.Error())
		return
	}
	if req.ConnectionID == 0 || req.Schema == "" || req.Table == "" || req.Alter == "" {
		resp.Fail(c, resp.CodeBadRequest, "实例、库、表、变更语句都不能为空")
		return
	}

	var by string
	if u := middleware.CurrentUser(c); u != nil {
		by = u.Name
	}
	job, err := h.osc.runner.Start(c.Request.Context(), osc.StartRequest{
		ConnectionID: req.ConnectionID, Schema: req.Schema, Table: req.Table,
		Alter: req.Alter, CreatedBy: by,
	})
	if err != nil {
		// 前置检查不通过要**把每一条阻塞项都列出来**。一条条试出来,在生产上就是
		// 一个个变更窗口。
		var pe *osc.PreflightError
		if errors.As(err, &pe) {
			reasons := make([]string, 0, len(pe.Blockers))
			for _, b := range pe.Blockers {
				reasons = append(reasons, b.Reason)
			}
			resp.FailData(c, resp.CodeIntercepted,
				"前置检查未通过,这次变更不能用在线方式做", gin.H{"blockers": reasons})
			return
		}
		resp.Fail(c, resp.CodeBadRequest, err.Error())
		return
	}
	resp.OK(c, job)
}

// OSCAbort 叫停一个正在跑的任务。
//
// 中止**不是**"当没发生过":影子表可能已经建了、拷了一半。runner 在退出路径上做清理,
// 但清理本身也可能失败 —— 所以任务会停在 aborted 而不是消失,残留由人按列表去收。
func (h *Handler) OSCAbort(c *gin.Context) {
	if h.osc == nil {
		resp.Fail(c, resp.CodeNotFound, "任务不存在")
		return
	}
	if err := h.osc.runner.Abort(c.Request.Context(), pathID(c)); err != nil {
		resp.Fail(c, resp.CodeBadRequest, err.Error())
		return
	}
	resp.OK(c, gin.H{"ok": true})
}
