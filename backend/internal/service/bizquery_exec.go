package service

// 业务方数据查询的执行入口(见 .scratch/biz-data-query/PRD.md 第 5.1~5.8 节)。
//
// 顺序即判定顺序,任何一步不过都不执行:
//
//  1. 实例仍是业务查询源,且不在维护态;
//  2. 这个人对这个库有**此刻生效**的查询授权(LiveQueryGrant 直接比时间);
//  3. 语句通过 gateway.BizReadOnlyCheck:单条只读、不锁、不写、不碰别的库;
//  4. 没超过并发配额(每人、每个实例);
//  5. 改写 LIMIT、在只读事务里执行(gateway.RealBizQuery),结果服务端脱敏;
//  6. 审计:operator 写"查询授权 #id",说明这一次凭的是哪条授权。
//
// 这条路**不走**能力矩阵与高危字典:业务查询角色在矩阵里全是 deny(见 ensureBizQueryRole),
// 查询权只来自授权;而只读判定比高危字典严得多 —— 字典之外的一切写法也都不允许。

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"velagateway/internal/dto"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// 并发配额(tbl_setting)。
const (
	SettingBizMaxPerUser   = "bizQuery.maxConcurrentPerUser"
	SettingBizMaxPerSource = "bizQuery.maxConcurrentPerSource"
)

// 三类失败,错误码不同(后端开发文档第 10 章):
//
//   - ErrBizQueryDenied:判定拒绝 —— "你不能这么做"(40300);
//   - ErrBizQueryBusy:并发配额已满 —— 限流,稍后重试即可(42900);
//   - *BizQueryFailure:导出时查询本身失败 —— "做了,但没做成",与执行接口一样回 0 + data.error。
var (
	ErrBizQueryDenied = errors.New("biz query denied")
	ErrBizQueryBusy   = errors.New("biz query busy")
)

// BizQueryDenial 是判定拒绝或限流:Ref 是机器可读的原因(code + 参数,前端按读者语言渲染、
// 并据此决定要不要刷新授权列表),Error() 是它的规范中文串(进 msg 与审计)。
// errors.Is 认 ErrBizQueryDenied(或限流时的 ErrBizQueryBusy)。
type BizQueryDenial struct {
	Ref  *model.RuleRef
	busy bool
}

func (e *BizQueryDenial) Error() string { return model.RenderBizReason(e.Ref) }

func (e *BizQueryDenial) Is(target error) bool {
	return (target == ErrBizQueryDenied && !e.busy) || (target == ErrBizQueryBusy && e.busy)
}

func bizDenied(ref *model.RuleRef) error { return &BizQueryDenial{Ref: ref} }

func bizDeny(code string, kv ...string) error { return bizDenied(model.NewRuleRef(code, kv...)) }

// BizQueryFailure 是导出时查询执行失败(库报错、超时、取消)。Ref 是机器可读的原因,Msg 是它的
// 规范中文串。
type BizQueryFailure struct {
	Ref      *model.RuleRef
	Msg      string
	TimedOut bool
}

// bizExecFailure 把一次执行失败归成三种之一:超时已取消、已取消、库报错(驱动原文放参数里,不翻译)。
func bizExecFailure(err error, timeoutSec int) *model.RuleRef {
	switch {
	case bizTimedOut(err):
		return model.NewRuleRef(model.BizExecTimeout, "sec", model.Itoa(timeoutSec))
	case errors.Is(err, context.Canceled):
		return model.NewRuleRef(model.BizExecCancelled)
	default:
		return model.NewRuleRef(model.BizExecFailed, "err", err.Error())
	}
}

// bizFailureTag 是写进审计 operator 的那一小段(见 auditTag)。
func bizFailureTag(ref *model.RuleRef, timeoutSec int) string {
	switch ref.Code {
	case model.BizExecTimeout:
		return fmt.Sprintf("超时 %ds 已取消", timeoutSec)
	case model.BizExecCancelled:
		return "已取消"
	default:
		return "失败"
	}
}

func (e *BizQueryFailure) Error() string { return e.Msg }

// bizSlots 记着每人、每个实例正在跑的业务查询数。它挂在 Services 上(Services.bizSlots),
// 零值即可用;不做成包级变量,理由同 Services.sensitiveRules:包级的一份会在多个 Services
// 之间串味。
type bizSlots struct {
	mu     sync.Mutex
	user   map[int64]int
	source map[int64]int
}

func (b *bizSlots) acquire(uid, cid int64, perUser, perSource int) (func(), *model.RuleRef) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.user == nil {
		b.user, b.source = map[int64]int{}, map[int64]int{}
	}
	if b.user[uid] >= perUser {
		return nil, model.NewRuleRef(model.BizBusyUser, "n", model.Itoa(b.user[uid]))
	}
	if b.source[cid] >= perSource {
		return nil, model.NewRuleRef(model.BizBusySource)
	}
	b.user[uid]++
	b.source[cid]++
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.user[uid]--
		b.source[cid]--
	}, nil
}

// MyQueryGrants 列出当前用户自己的授权(生效中与未到开始时间的),给数据查询页左侧用。
func (s *Services) MyQueryGrants(u *model.User) []dto.QueryGrantView {
	out := []dto.QueryGrantView{}
	if u == nil {
		return out
	}
	all, err := s.ListQueryGrants(QueryGrantQuery{Email: u.Email})
	if err != nil {
		return out
	}
	for _, g := range all {
		if g.Status != model.GrantActive && g.Status != model.GrantPending {
			continue
		}
		// 实例被取消了业务查询源标记:授权还在,但查不了 —— 不列出来,免得人点进去才碰壁。
		if c, err := s.Repo.GetConnection(g.SourceID); err != nil || !c.BizQuery {
			continue
		}
		out = append(out, g)
	}
	return out
}

// bizPrep 是一次业务查询通过判定之后、执行之前的全部上下文。查询与导出共用同一份判定,
// 两条路判的东西一字不差。
type bizPrep struct {
	conn       *model.Connection
	grant      *model.QueryGrant
	query      string // 用户写的原文(审计记这个)
	run        string // 补了 LIMIT 的那一份(交给驱动)
	maxRows    int
	timeoutSec int
	operator   string
	release    func()
	// 由谁提供(见 bizquery_replica.go),runBizQuery 填。
	served       string
	replicaError string
}

// prepareBizQuery 按顺序做全部判定;任何一步不过都不执行。成功后已占用并发配额,调用方必须
// defer p.release()。
func (s *Services) prepareBizQuery(u *model.User, req dto.BizQueryReq, forExport bool) (*bizPrep, error) {
	query := strings.TrimSpace(req.SQL)
	// 结尾的 \G / \g 是 mysql 客户端的"竖排显示",不是 SQL(2026-11-12):剥掉再判定、再执行 ——
	// 判定看到的就是要下发的那一份。竖排由前端负责画;任何引擎都剥,DWS 上顺手写的 \G 也一样。
	if stripped, ok := gateway.MySQLStripVertical(query); ok {
		query = strings.TrimSpace(stripped)
	}
	if query == "" {
		return nil, bizDeny(model.BizNoSQL)
	}
	conn, err := s.Repo.GetConnection(req.SourceID)
	if err != nil {
		return nil, bizDeny(model.BizNoSource)
	}
	// 先判授权,再说别的:拒绝理由里的实例名只给有授权记录的人看(见 bizSourceLabel)。
	db := strings.TrimSpace(req.Database)
	g := s.LiveQueryGrant(u.ID, conn.ID, db)
	if g == nil {
		return nil, bizDeny(model.BizNoGrant, "source", s.bizSourceLabel(u, conn), "db", db)
	}
	// 库名比较不分大小写时,请求里写的 ORDERS 也能取到 orders 的授权(utf8mb4_unicode_ci),而
	// 区分大小写的库上那是两个库。执行一律用授权上的库名(2026-11 审计 V-12)。
	db = g.DBName
	if !conn.BizQuery {
		return nil, bizDeny(model.BizNotSource, "source", conn.Name)
	}
	if conn.Status == model.ConnStatusMaint {
		return nil, bizDeny(model.BizMaintenance, "source", conn.Name)
	}
	lim := s.BizQueryLimits()
	// 分层关掉了数据查询导出限制(默认 dev / uat,见 tier_switches.go):有查询授权就能导。
	if forExport && s.tierBizExportRestricted(conn) {
		// 两道开关都开才能导:全局开关是"这个网关允不允许业务方把数据带走",授权上的是
		// "审批时有没有批准这个人导出"。
		if !lim.ExportEnabled {
			return nil, bizDeny(model.BizExportOff)
		}
		if !g.AllowExport {
			return nil, bizDeny(model.BizExportDenied, "source", conn.Name, "db", db)
		}
	}
	operator := fmt.Sprintf("查询授权 #%d", g.ID)

	// 实例上的全部库名,用来识别"引用了别的库"。真实实例取不到就不执行:确认不了"别的库"
	// 是哪些,按库收窄就无从谈起,宁可拒绝。
	dbs, derr := s.bizGateDatabases(conn)
	if derr != nil {
		return nil, bizDeny(model.BizDBListFailed, "source", conn.Name, "err", derr.Error())
	}
	if reason := gateway.BizReadOnlyCheck(conn.Engine, query, db, dbs); reason != nil {
		s.recordAuditBy(u, operator, conn, query, model.RiskMid, model.ResultRejected, "", model.AuditEventIntercept)
		return nil, bizDenied(reason)
	}
	// 敏感字段:结果一律按【设置 · 敏感字段】打码(gateway.maskResultSet),但打码按选择列表的
	// 文本比对,经子查询 / UNION / CTE 改名就兜不住。终端上那是"顺手看到",导出因此要审批
	// (export.go 同一个判断);业务查询没有审批这条路,业务方拿到的又是可以导出的生产数据,
	// 所以这种形态直接拒绝 —— 宁可让一条本来干净的查询改写一次,也不让原值离开网关。
	// 分层关掉了脱敏:结果本来就不打码,改名也就谈不上绕过。
	if col := gateway.MaskEvasionColumn(query, s.SensitiveRulesForGateway()); col != "" && s.tierMasks(conn) {
		s.recordAuditBy(u, operator, conn, query, model.RiskHigh, model.ResultRejected, "", model.AuditEventIntercept)
		return nil, bizDeny(model.BizSensitiveEvasion, "column", col)
	}

	maxRows := min(effective(g.MaxRows, lim.DefaultMaxRows), lim.MaxRowsCap)
	timeoutSec := min(effective(g.TimeoutSec, lim.DefaultTimeout), lim.TimeoutCap)
	// 多取一行:读到第 maxRows+1 行才知道"还有更多",截断提示才说得准。
	run, reason := gateway.ApplyRowLimit(conn.Engine, query, maxRows+1)
	if reason != nil {
		s.recordAuditBy(u, operator, conn, query, model.RiskMid, model.ResultRejected, "", model.AuditEventIntercept)
		return nil, bizDenied(reason)
	}
	// 每人每天次数上限(PRD 5.7):数当天已真正执行的(见 Repo.CountBizQueriesSince)。它是限流,
	// 与并发已满同一个回答(42900),次日零点自动恢复。数不出来就按没超处理 —— 这是保护从库的
	// 软限额,不是授权判定,不该因为审计表一时读不到就把所有人挡在外面。
	if lim.MaxDailyPerUser > 0 {
		y, m, d := time.Now().Date()
		if n, err := s.Repo.CountBizQueriesSince(u.ID, time.Date(y, m, d, 0, 0, 0, 0, time.Local)); err == nil && n >= int64(lim.MaxDailyPerUser) {
			return nil, &BizQueryDenial{Ref: model.NewRuleRef(model.BizDailyLimit, "n", model.Itoa(lim.MaxDailyPerUser)), busy: true}
		}
	}
	release, busy := s.bizSlots.acquire(u.ID, conn.ID, lim.MaxPerUser, lim.MaxPerSource)
	if busy != nil {
		return nil, &BizQueryDenial{Ref: busy, busy: true}
	}
	// SQLite 只有一个库(main),它的"数据库"字段是文件路径 —— 换成库名就打开了一个空文件。
	if !strings.Contains(strings.ToLower(conn.Engine), "sqlite") {
		applyTargetDatabase(conn, db)
	}
	return &bizPrep{
		conn: conn, grant: g, query: query, run: run,
		maxRows: maxRows, timeoutSec: timeoutSec, operator: operator, release: release,
	}, nil
}

// bizGateDatabases 是判定"别的库"用的库名全集。真实实例每次现查(gateway.RealDatabaseNames):
// 元数据缓存漏掉没有表的库,也会被行数上限截断,拿它当全集,漏掉的库就能被跨库查询。
// 没有凭据的开发 / 演示连接只能用元数据缓存。
func (s *Services) bizGateDatabases(conn *model.Connection) ([]string, error) {
	if !gateway.RealExecSupported(conn) {
		names, _ := s.Repo.MetaDatabaseNames(conn.ID)
		return names, nil
	}
	// 与查询打到同一台库:配了从库就问从库(连不上回落主库)。
	target, _, _ := s.bizTarget(conn)
	names, err := gateway.RealDatabaseNames(target)
	if err == nil && len(names) == 0 && gateway.CrossDatabaseChecked(conn.Engine) {
		err = errors.New("实例没有返回任何库")
	}
	return names, err
}

// runBizQuery 执行一次已经通过判定的查询。没有凭据的连接在开发 / 演示环境走模拟,生产一律拒绝。
func (s *Services) runBizQuery(ctx context.Context, p *bizPrep) (res gateway.BizQueryResult, simulated bool, err error) {
	ctx = s.withTierMasking(ctx, p.conn) // 分层关掉了脱敏:只剩口令列兜底
	timeout := time.Duration(p.timeoutSec) * time.Second
	if !gateway.RealExecSupported(p.conn) {
		if !gateway.SimulationAllowed() {
			return res, false, gateway.ErrSimulationDisabled
		}
		r := s.Executor.Run(ctx, p.conn, p.run, timeout)
		res.Columns, res.Data = r.Columns, r.Data
		if len(res.Data) > p.maxRows {
			res.Data, res.Truncated = res.Data[:p.maxRows], true
		}
		return res, true, r.Err
	}
	target, served, rerr := s.bizTarget(p.conn)
	p.served, p.replicaError = served, rerr
	res, err = gateway.RealBizQuery(ctx, target, p.run, p.maxRows, timeout)
	return res, false, err
}

// bizTimedOut 认出"库端时限先触发"时各引擎的报错:MySQL 3024(max_execution_time)、
// MariaDB 1969(max_statement_time)、PostgreSQL 的 statement_timeout。
func bizTimedOut(err error) bool {
	msg := err.Error()
	return errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(msg, "maximum statement execution time") ||
		strings.Contains(msg, "max_statement_time exceeded") ||
		strings.Contains(msg, "statement timeout")
}

// auditTag 把这次执行的结果附在 operator 上:依据的授权、返回行数、是否截断、耗时。
//
// 放在 operator 而不是加列:operator 已经在审计哈希链的载荷里,事后改不了;为几项统计给一张
// 哈希链表加列,要同时改载荷与校验,不值得。
func auditTag(p *bizPrep, rows int, truncated bool, ms int, extra string) string {
	t := fmt.Sprintf("%s · %d 行", p.operator, rows)
	if truncated {
		t += "(已截断)"
	}
	t += fmt.Sprintf(" · %dms", ms)
	if tag := servedTag(p.served); tag != "" {
		t += " · " + tag
	}
	if extra != "" {
		t += " · " + extra
	}
	return fitRunes(t, 128)
}

// BizQueryExec 执行一条业务查询。
func (s *Services) BizQueryExec(ctx context.Context, u *model.User, req dto.BizQueryReq) (*dto.BizQueryResp, error) {
	p, err := s.prepareBizQuery(u, req, false)
	if err != nil {
		return nil, err
	}
	defer p.release()

	started := time.Now()
	res, simulated, err := s.runBizQuery(ctx, p)
	resp := &dto.BizQueryResp{MaxRows: p.maxRows, TimeoutSec: p.timeoutSec, Columns: []string{}, Rows: [][]string{},
		Ms: int(time.Since(started).Milliseconds()), Simulated: simulated, Served: p.served, ReplicaError: p.replicaError}
	if err != nil {
		if errors.Is(err, gateway.ErrSimulationDisabled) {
			return nil, bizDeny(model.BizSimulationOff, "err", err.Error())
		}
		if refused := s.bizRefusedAtExec(u, p, err); refused != nil {
			return nil, refused
		}
		ref := bizExecFailure(err, p.timeoutSec)
		resp.ErrorRef, resp.Error, resp.TimedOut = ref, model.RenderBizReason(ref), ref.Code == model.BizExecTimeout
		s.recordAuditBy(u, auditTag(p, 0, false, resp.Ms, bizFailureTag(ref, p.timeoutSec)), p.conn, p.query, model.RiskLow, model.ResultWarn, "", model.AuditEventExec)
		return resp, nil
	}
	resp.Columns, resp.Rows = nonNil(res.Columns), res.Data
	if resp.Rows == nil {
		resp.Rows = [][]string{}
	}
	resp.Truncated, resp.MaskedColumns = res.Truncated, res.MaskedColumns
	result := model.ResultExecuted
	if simulated {
		result = model.ResultSimulated
	}
	s.recordAuditBy(u, auditTag(p, len(resp.Rows), resp.Truncated, resp.Ms, ""), p.conn, p.query, model.RiskLow, result, "", model.AuditEventExec)
	return resp, nil
}

// BizQueryExport 把一次业务查询的结果导出为 CSV(同步,至多授权的行数上限)。
//
// 判定与查询完全相同(prepareBizQuery),多两道开关:全局导出开关与授权上的「允许导出」。结果永远
// 是脱敏的 —— 业务方没有原值通道,导出也不例外。单元格做防公式注入,文件带 BOM(Excel 直接打开
// 不乱码)。写一条 result=exported 的审计。
func (s *Services) BizQueryExport(ctx context.Context, u *model.User, req dto.BizQueryReq) (filename string, body []byte, err error) {
	p, err := s.prepareBizQuery(u, req, true)
	if err != nil {
		return "", nil, err
	}
	defer p.release()
	started := time.Now()
	res, _, err := s.runBizQuery(ctx, p)
	ms := int(time.Since(started).Milliseconds())
	if err != nil {
		if errors.Is(err, gateway.ErrSimulationDisabled) {
			return "", nil, bizDeny(model.BizSimulationOff, "err", err.Error())
		}
		if refused := s.bizRefusedAtExec(u, p, err); refused != nil {
			return "", nil, refused
		}
		ref := bizExecFailure(err, p.timeoutSec)
		f := &BizQueryFailure{Ref: ref, Msg: model.RenderBizReason(ref), TimedOut: ref.Code == model.BizExecTimeout}
		s.recordAuditBy(u, auditTag(p, 0, false, ms, "导出失败 · "+bizFailureTag(ref, p.timeoutSec)), p.conn, p.query, model.RiskLow, model.ResultWarn, "", model.AuditEventExec)
		return "", nil, f
	}
	var buf bytes.Buffer
	buf.WriteString(utf8BOM)
	w := csv.NewWriter(&buf)
	head := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		head[i] = csvSanitize(c)
	}
	_ = w.Write(head)
	for _, r := range res.Data {
		rec := make([]string, len(r))
		for i, v := range r {
			rec[i] = csvSanitize(v)
		}
		_ = w.Write(rec)
	}
	w.Flush()
	name := fmt.Sprintf("%s_%s.csv", safeFileStem(req.Database), time.Now().Format("20060102_150405"))
	s.recordAuditBy(u, auditTag(p, len(res.Data), res.Truncated, ms, "导出 CSV"), p.conn, p.query, model.RiskLow, model.ResultExported, "", model.AuditEventExec)
	return name, buf.Bytes(), nil
}

// bizRefusedAtExec:执行层在下发前拒绝了(Oracle 查依赖发现自治事务等,见 gateway.BizRefused)——
// 与判定层的拒绝同一个回答、同一条拦截审计,不算执行失败。不是这种情况返回 nil。
func (s *Services) bizRefusedAtExec(u *model.User, p *bizPrep, err error) error {
	var refused *gateway.BizRefused
	if !errors.As(err, &refused) {
		return nil
	}
	s.recordAuditBy(u, p.operator, p.conn, p.query, model.RiskHigh, model.ResultRejected, "", model.AuditEventIntercept)
	return bizDenied(refused.Ref)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
