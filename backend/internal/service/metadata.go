package service

// 元数据同步:把远端库的表清单与表结构定期抓一份到本地。
//
// 这是网关里少数几个**主动去连生产库**的东西之一(其余都是有人按下按钮才连),所以
// 它的每一条边界都是往"少做一点"的方向定的:
//
//   默认关。 打开它意味着这台网关会周期性地登录你的每一台生产实例 —— 那必须是一次
//            明确的决定,不能因为升级了一个版本就自己开始跑。
//   限并发。 88 台实例同时探查,是对同一片网络和同一批库的一次自制的压力测试。
//   跳维护态。 status=maint 的实例是被人为标成"别碰"的,定时任务不该是那个例外。
//   跳模拟连接。 没有凭据就没有远端,写一份空清单进去等于宣布这台库是空的。
//   一台失败不影响别台。 一台连不上就记下原因、接着下一台;整轮中止的表现是"某天
//            之后所有实例的元数据都停在同一时刻",而原因藏在第一台上。
//
// 缓存是**只读副本,不是真相**:判定、执行、导出一律仍然走实时的目标库。一份可能
// 过期的结构如果被拿去做判断,它就从"快"变成了"错"。

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// 设置项。默认值就是"不开、一天一次、两台并发"。
const (
	setMetaSyncEnabled     = "meta.sync.enabled"
	setMetaSyncIntervalHrs = "meta.sync.intervalHours"
	setMetaSyncConcurrency = "meta.sync.concurrency"
)

// metaSyncEnabled 说明这台网关要不要主动去抓远端结构。默认 false —— 见文件注释。
func (s *Services) metaSyncEnabled() bool { return s.settingBool(setMetaSyncEnabled, false) }

// MetaSyncInterval 让 main.go 的定时器每轮读一次当前设置 —— 间隔是运行时可改的,
// 按启动时那个值把 ticker 钉死,改了设置要重启才生效。
func (s *Services) MetaSyncInterval() time.Duration { return s.metaSyncInterval() }

func (s *Services) metaSyncInterval() time.Duration {
	h := s.settingInt(setMetaSyncIntervalHrs, 24)
	if h < 1 {
		h = 1 // 比一小时更频繁的全量扫描,已经不是"缓存"而是"压测"
	}
	if h > 24*30 {
		h = 24 * 30
	}
	return time.Duration(h) * time.Hour
}

func (s *Services) metaSyncConcurrency() int {
	n := s.settingInt(setMetaSyncConcurrency, 2)
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8 // 再高就不是"抓元数据",是对着自己的生产集群并发登录
	}
	return n
}

// SweepMetadata 跑一轮全量同步。关着就什么都不做,安全地反复调用。
//
// 与 SweepExportRetention 同一种形状:一个可以挂在 ticker 上、也可以在启动时调一次的
// 幂等方法。它不返回错误 —— 单台的失败记在 tbl_meta_sync 上,而整轮的失败没有调用方
// 可以处理;日志与那张表才是它汇报的地方。
func (s *Services) SweepMetadata() {
	if !s.metaSyncEnabled() {
		return
	}
	conns, err := s.Repo.ListConnections()
	if err != nil {
		slog.Warn("metadata sync: list connections failed", "err", err)
		return
	}
	started := time.Now()
	sem := make(chan struct{}, s.metaSyncConcurrency())
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, failed, skipped := 0, 0, 0

	for i := range conns {
		c := conns[i]
		// 维护态的实例是被人为标成"别碰"的。定时任务不该是那个例外。
		if c.Status == "maint" {
			mu.Lock()
			skipped++
			mu.Unlock()
			continue
		}
		// 没有凭据就没有远端可探。写一份空清单进去等于宣布这台库是空的。
		if !gateway.RealExecSupported(&c) {
			mu.Lock()
			skipped++
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(conn model.Connection) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := s.syncOneConnection(&conn); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			mu.Lock()
			ok++
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	slog.Info("metadata sync finished",
		"ok", ok, "failed", failed, "skipped", skipped, "took", time.Since(started).String())
}

// SyncConnectionMetadata 同步一台实例 —— 也是"立刻同步这一台"的入口。
//
// 访问控制在这里判:一个够不到这台实例的人,不该能让网关去连它(那是一条把实例
// 存在性、可达性甚至账号有效性问出来的旁路)。
func (s *Services) SyncConnectionMetadata(u *model.User, connID int64) error {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return ErrForbidden
	}
	if !gateway.RealExecSupported(conn) {
		return fmt.Errorf("该实例未配置真实执行凭据,无法探查元数据")
	}
	return s.syncOneConnection(conn)
}

// syncOneConnection 探查一台实例并整个替换它的缓存。
//
// 失败也要写 tbl_meta_sync:没有它,"这台实例为什么一张表都没有"就没有答案 ——
// 是还没轮到它、连不上、账号没权限,还是它真的空着?这四种在界面上长得一模一样,
// 而只有第一种是正常的。
func (s *Services) syncOneConnection(conn *model.Connection) error {
	state := &model.MetaSync{ConnectionID: conn.ID, StartedAt: time.Now()}
	fail := func(err error) error {
		state.FinishedAt = time.Now()
		state.Err = clip(err.Error(), 500)
		if serr := s.Repo.SaveMetaSync(state); serr != nil {
			slog.Warn("metadata sync: save state failed", "conn", conn.Name, "err", serr)
		}
		slog.Warn("metadata sync failed", "conn", conn.Name, "err", err)
		return err
	}

	tRows, cRows, err := gateway.RealMetadata(conn)
	// 截断不是失败:拿到的那部分仍然有用,但必须留下痕迹,否则"少了一半"没人知道。
	truncated := err == gateway.ErrMetaTruncated
	if err != nil && !truncated {
		return fail(err)
	}

	tables := make([]model.MetaTable, 0, len(tRows))
	for _, t := range tRows {
		tables = append(tables, model.MetaTable{
			DBName: t.Database, SchemaName: t.Schema, Name: t.Table,
			Kind: t.Kind, Comment: clip(t.Comment, 500),
		})
	}
	cols := make([]model.MetaColumn, 0, len(cRows))
	for _, c := range cRows {
		cols = append(cols, model.MetaColumn{
			DBName: c.Database, SchemaName: c.Schema, TableName_: c.Table, Ordinal: c.Ordinal,
			Name: c.Name, DataType: clip(c.DataType, 128), Nullable: c.Nullable,
			ColDefault: clip(c.Default, 500), Comment: clip(c.Comment, 500), IsPK: c.IsPK,
		})
	}
	if err := s.Repo.ReplaceMetadata(conn.ID, tables, cols); err != nil {
		return fail(err)
	}

	dbs := map[string]bool{}
	for _, t := range tables {
		dbs[t.DBName] = true
	}
	state.FinishedAt = time.Now()
	state.Databases, state.Tables, state.Columns = len(dbs), len(tables), len(cols)
	state.Err = ""
	if truncated {
		state.Err = gateway.ErrMetaTruncated.Error()
	}
	if err := s.Repo.SaveMetaSync(state); err != nil {
		slog.Warn("metadata sync: save state failed", "conn", conn.Name, "err", err)
	}
	return nil
}

// SearchMetadata 在**够得到的那些实例**里按表名 / 列名检索缓存。
//
// 范围收在 SQL 里(见 repository.SearchMetaTables):够不到那台实例的人,连它有哪些
// 表都不该看见 —— 表名和列名本身就是信息。
func (s *Services) SearchMetadata(u *model.User, q string, limit int) ([]model.MetaTable, []model.MetaColumn, error) {
	ids := s.AccessibleConnIDs(u)
	tables, err := s.Repo.SearchMetaTables(ids, q, limit)
	if err != nil {
		return nil, nil, err
	}
	cols, err := s.Repo.SearchMetaColumns(ids, q, limit)
	if err != nil {
		return nil, nil, err
	}
	return tables, cols, nil
}

// ConnectionMetadata 读一台实例缓存下来的表(可选按库过滤)与同步状态。
func (s *Services) ConnectionMetadata(u *model.User, connID int64, db string) ([]model.MetaTable, *model.MetaSync, error) {
	conn, err := s.Repo.GetConnection(connID)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	if !s.canAccessConn(u, conn) {
		return nil, nil, ErrForbidden
	}
	tables, err := s.Repo.MetaTablesOf(connID, db)
	if err != nil {
		return nil, nil, err
	}
	var state *model.MetaSync
	if all, serr := s.Repo.MetaSyncStates(); serr == nil {
		for i := range all {
			if all[i].ConnectionID == connID {
				state = &all[i]
				break
			}
		}
	}
	return tables, state, nil
}
