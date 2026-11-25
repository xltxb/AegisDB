package service

// 手动同步元数据改在后台跑(2026-10-28)。
//
// 生产 fat-mysql(3000+ 表 / 10 万+ 列,库频繁建删)一次同步十几秒,按钮在 HTTP
// 请求里等完,人就对着转圈等十几秒;写得再快,也挡不住"这台实例这次改动就是多"。
// 现在点下去立刻返回,界面轮询 GET .../metadata/sync 看跑完没有。
//
// 同一台实例同一时刻只跑一个同步 —— 手动点两下、定时任务恰好轮到它,都不该让两次
// 同步同时对齐同一份缓存(两边各自比对、各自删插,互相踩)。占位记在进程内存里:
// 网关是单进程部署,而一次同步最长 metadataTimeout,进程重启它自然也就没了。

import (
	"errors"
	"log/slog"
	"time"

	"velagateway/internal/model"
)

// ErrMetaSyncRunning 这台实例已经有一个同步在跑。
var ErrMetaSyncRunning = errors.New("这台实例正在同步元数据,请稍后查看结果")

// metaRun 一台实例在本进程里的同步状态。
type metaRun struct {
	running bool
	// failed 最近一次跑完时的致命错误(连不上 / 没权限 …);成功或只是"不完整"时为空。
	// 不完整的那句话在 tbl_meta_sync.err 里 —— 两者分开,界面才能把"收下了但缺几个库"
	// 和"什么都没收到"说成两件事。
	failed string
}

// MetaSyncStatus 给界面轮询的同步状态。
type MetaSyncStatus struct {
	Running bool            `json:"running"`
	Failed  string          `json:"failed,omitempty"`
	Sync    *model.MetaSync `json:"sync"`
}

// syncGuarded 占住这台实例再同步;已经有一个在跑时不跑,ran=false。
func (s *Services) syncGuarded(conn *model.Connection) (ran bool, err error) {
	if !s.beginMetaRun(conn.ID) {
		return false, nil
	}
	defer func() { s.endMetaRun(conn.ID, err) }()
	return true, s.syncOneConnection(conn)
}

func (s *Services) beginMetaRun(connID int64) bool {
	s.metaRunsMu.Lock()
	defer s.metaRunsMu.Unlock()
	if s.metaRuns == nil {
		s.metaRuns = map[int64]*metaRun{}
	}
	if r := s.metaRuns[connID]; r != nil && r.running {
		return false
	}
	s.metaRuns[connID] = &metaRun{running: true}
	return true
}

func (s *Services) endMetaRun(connID int64, err error) {
	s.metaRunsMu.Lock()
	defer s.metaRunsMu.Unlock()
	r := &metaRun{}
	if err != nil {
		r.failed = clip(err.Error(), 500)
	}
	s.metaRuns[connID] = r
}

func (s *Services) metaRunOf(connID int64) metaRun {
	s.metaRunsMu.Lock()
	defer s.metaRunsMu.Unlock()
	if r := s.metaRuns[connID]; r != nil {
		return *r
	}
	return metaRun{}
}

// StartConnectionMetadataSync 在后台同步一台实例,立刻返回。
//
// 检查(存在 / 够得到 / 有凭据)仍然当场做、当场报 —— 那些不需要等。已经在跑时
// 不再起一个,照样返回"在跑",界面接着轮询那一个就是了。
func (s *Services) StartConnectionMetadataSync(u *model.User, connID int64) (*MetaSyncStatus, error) {
	conn, err := s.metaSyncTarget(u, connID)
	if err != nil {
		return nil, err
	}
	if s.beginMetaRun(conn.ID) {
		goSafe("metadata-sync", func() {
			started := time.Now()
			var err error
			defer func() {
				if r := recover(); r != nil {
					err = errors.New("同步过程中出现内部错误")
					slog.Error("metadata sync panicked", "conn", conn.Name, "panic", r)
				}
				s.endMetaRun(conn.ID, err)
				slog.Info("metadata sync (background) finished", "conn", conn.Name,
					"took", time.Since(started).String(), "ok", err == nil)
			}()
			err = s.syncOneConnection(conn)
		})
	}
	return &MetaSyncStatus{Running: true, Sync: s.metaSyncStateOf(conn.ID)}, nil
}

// ConnectionMetadataSyncStatus 一台实例此刻在不在同步、上一次的结果。
func (s *Services) ConnectionMetadataSyncStatus(u *model.User, connID int64) (*MetaSyncStatus, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return nil, ErrForbidden
	}
	r := s.metaRunOf(connID)
	return &MetaSyncStatus{Running: r.running, Failed: r.failed, Sync: s.metaSyncStateOf(connID)}, nil
}

func (s *Services) metaSyncStateOf(connID int64) *model.MetaSync {
	all, err := s.Repo.MetaSyncStates()
	if err != nil {
		return nil
	}
	for i := range all {
		if all[i].ConnectionID == connID {
			return &all[i]
		}
	}
	return nil
}
