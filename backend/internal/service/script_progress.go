package service

import (
	"context"
	"regexp"
	"strconv"
	"sync"
	"time"

	"aegisdb/internal/model"
)

// 脚本扫描 / 执行的进度(Web 命令行上传脚本弹窗用,2026-10)。
//
// 扫描与执行都是一个同步请求:一份几 MB 的脚本扫十几秒、执行更久,这期间界面只能转圈,人分不清
// 是在跑还是卡住了。前端发请求时带一个自己生成的 progressId,服务端在扫描 / 执行的每一条语句上
// 记一笔;前端在请求返回之前轮询 GET /scripts/progress/:id 画进度。
//
// 进度只放内存:它只在请求进行中有意义,丢了无非是界面退回转圈,不值得进库。按"用户 + id"存,
// 别人的 id 查不到(也就看不到别人在跑什么)。10 分钟没更新的自动清掉。

// ScriptProgress 是一次扫描 / 执行此刻的进度快照。
type ScriptProgress struct {
	Phase     string    `json:"phase"` // scan(字典扫描)| judge(逐条判定)| run(逐条执行)
	Done      int       `json:"done"`  // 本阶段已完成的条数
	Total     int       `json:"total"` // 本阶段总条数
	Current   string    `json:"current,omitempty"` // 正在处理的那条语句的开头
	StartedAt time.Time `json:"startedAt"`
	Finished  bool      `json:"finished"`
}

// scriptProgress 是一次请求的进度记录。nil 也能用(什么都不记):没带 progressId 的请求照旧。
type scriptProgress struct {
	mu      sync.Mutex
	p       ScriptProgress
	updated time.Time
}

func (t *scriptProgress) phase(name string, total int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.p.Phase, t.p.Total, t.p.Done, t.p.Current = name, total, 0, ""
	t.updated = time.Now()
	t.mu.Unlock()
}

// at 记"正在处理第 pos 条(从 1 起)":done = pos-1。
func (t *scriptProgress) at(pos int, sql string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.p.Done, t.p.Current = pos-1, scriptSQLPreview(sql)
	t.updated = time.Now()
	t.mu.Unlock()
}

// done 记"本阶段已完成 n 条"。
func (t *scriptProgress) done(n int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.p.Done = n
	t.updated = time.Now()
	t.mu.Unlock()
}

// Finish 标记整个请求结束(成功、失败、送审都算)。轮询看到它就停。
func (t *scriptProgress) Finish() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.p.Finished, t.p.Current = true, ""
	t.updated = time.Now()
	t.mu.Unlock()
}

func (t *scriptProgress) snapshot() ScriptProgress {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.p
}

const scriptProgressTTL = 10 * time.Minute

// progressIDRe:前端生成的 id(UUID 一类)。限制字符与长度,免得任意字符串进内存表当键。
var progressIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

var scriptProgresses = struct {
	mu sync.Mutex
	m  map[string]*scriptProgress
}{m: map[string]*scriptProgress{}}

func progressKey(u *model.User, id string) string {
	return strconv.FormatInt(u.ID, 10) + ":" + id
}

// StartScriptProgress 为这次请求开一条进度记录;id 为空或不合法返回 nil(不记进度)。
// 同一个 id 再开一次是重新开始(前端每次扫描 / 执行都生成新 id,不会撞)。
func (s *Services) StartScriptProgress(u *model.User, id string) *scriptProgress {
	if u == nil || !progressIDRe.MatchString(id) {
		return nil
	}
	now := time.Now()
	t := &scriptProgress{p: ScriptProgress{StartedAt: now}, updated: now}
	scriptProgresses.mu.Lock()
	defer scriptProgresses.mu.Unlock()
	for k, old := range scriptProgresses.m { // 顺手清掉过期的
		old.mu.Lock()
		stale := now.Sub(old.updated) > scriptProgressTTL
		old.mu.Unlock()
		if stale {
			delete(scriptProgresses.m, k)
		}
	}
	scriptProgresses.m[progressKey(u, id)] = t
	return t
}

// ScriptProgressOf 读 u 自己的一条进度。别人的、过期的、不存在的一律 false。
func (s *Services) ScriptProgressOf(u *model.User, id string) (ScriptProgress, bool) {
	if u == nil || !progressIDRe.MatchString(id) {
		return ScriptProgress{}, false
	}
	scriptProgresses.mu.Lock()
	t, ok := scriptProgresses.m[progressKey(u, id)]
	scriptProgresses.mu.Unlock()
	if !ok {
		return ScriptProgress{}, false
	}
	return t.snapshot(), true
}

type scriptProgressCtxKey struct{}

// WithScriptProgress 把进度记录挂到 ctx 上,扫描 / 执行循环从 ctx 里取。t 为 nil 时原样返回。
func WithScriptProgress(ctx context.Context, t *scriptProgress) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, scriptProgressCtxKey{}, t)
}

func scriptProgressFrom(ctx context.Context) *scriptProgress {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(scriptProgressCtxKey{}).(*scriptProgress)
	return t
}
