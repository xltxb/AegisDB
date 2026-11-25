package service

// 这条语句走不走在线变更(.scratch/osc-product PRD §3,工单 01)。
//
// 依次判断,任何一条不满足就照原路径:
//  1. 生产:实例所在分层开了「生产横幅」(dangerBanner)—— 与红色横幅、进入生产确认弹窗同一个
//     判据,不按名字认 prod;分层解析不出来按生产处理(宁可多走一道);
//  2. MySQL 系、配了真实执行凭据;
//  3. 语句是一条针对某张表的 ALTER / CREATE INDEX / DROP INDEX(osc.ParseDDL);
//  4. 目标表数据量达到阈值:估算行数 ≥ osc.minRows 或 数据 + 索引 ≥ osc.minBytes。
//
// 四条都满足后按实例版本归类子句(工单 08,osc.Parsed.Resolve):原生秒完成的(INSTANT / 只改
// 元数据)照原路径、不提示;原生会阻塞写入或重建表的(INPLACE / COPY)走在线变更;在线变更做不了
// 的(外键、分区……)照原路径,但提示"大表直接 DDL"。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
	"velagateway/internal/osc"
)

const (
	defaultOSCMinRows  = 1_000_000
	defaultOSCMinBytes = 1 << 30 // 1 GB
)

type OSCRouteKind int

const (
	OSCNormal    OSCRouteKind = iota // 照原路径,不提示
	OSCOnline                        // 改走在线变更
	OSCBigDirect                     // 照原路径,但提示"大表直接 DDL"
)

// OSCDecision 是一条语句的判定结果,带上判定用到的事实,给提示与审计用。
type OSCDecision struct {
	Kind          OSCRouteKind
	Parsed        osc.Parsed
	Schema, Table string
	Rows, Bytes   int64
	// SizeErr:读不到数据量(照原路径),写进审计。
	SizeErr error
}

// oscThresholds 读设置里的两个阈值;老库没有这两项时按默认值。
func (s *Services) oscThresholds() (rows, bytes int64) {
	return int64(s.settingInt("osc.minRows", defaultOSCMinRows)), int64(s.settingInt("osc.minBytes", defaultOSCMinBytes))
}

// isProductionTier:分层开了生产横幅即生产;解析不出来按生产(宁可多走一道,不可漏)。
//
// 不看「需要 MFA」:不启用 MFA 的部署会把它关掉,生产大表加索引就整个绕开了在线变更
// (2026-10 端到端验收:prod 分层 MFA 关着,120 万行的表照原路径直接 ALTER)。横幅是
// "这是生产"本身的标记,不随认证方式变化。
func (s *Services) isProductionTier(conn *model.Connection) bool {
	t, err := s.tierOf(conn)
	return err != nil || t == nil || t.DangerBanner
}

// oscEngine:在线变更只用于 MySQL 与 PolarDB(MySQL 版)(2026-10 用户定)。MySQL 系里的 TiDB、MariaDB
// 协议相同,但 binlog / 在线 DDL 的语义不同,照原路径 —— 送进来只会被前置检查拦下,DDL 反而执行不了。
// PolarDB(PostgreSQL 版)在引擎族里已归 PostgreSQL;其它引擎(DWS / Oracle / SQLite / MongoDB)
// 本来就不在此列。
func oscEngine(engine string) bool {
	e := strings.ToLower(engine)
	if gateway.EngineFamilyOf(engine) != "mysql" {
		return false
	}
	for _, other := range []string{"tidb", "mariadb"} {
		if strings.Contains(e, other) {
			return false
		}
	}
	return true
}

// OSCRoute 判定一条语句。conn 已经 applyTargetDatabase 过(Database 是这次的目标库)。
func (s *Services) OSCRoute(ctx context.Context, conn *model.Connection, stmt string) OSCDecision {
	d := OSCDecision{Kind: OSCNormal}
	if !oscEngine(conn.Engine) || !gateway.RealExecSupported(conn) {
		return d
	}
	p, ok := osc.ParseDDL(stmt)
	if !ok {
		return d
	}
	if !s.isProductionTier(conn) {
		return d
	}
	d.Parsed = p
	d.Schema, d.Table = p.Schema, p.Table
	if d.Schema == "" {
		d.Schema = conn.Database
	}
	if d.Schema == "" {
		d.SizeErr = apimsg.Errorf("没有选定库,无法确定表 %s 的数据量", d.Table)
		return d
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var ver [3]int
	d.Rows, d.Bytes, ver, d.SizeErr = gateway.OSCTableSize(sctx, conn, d.Schema, d.Table)
	if d.SizeErr != nil {
		return d
	}
	minRows, minBytes := s.oscThresholds()
	if d.Rows < minRows && d.Bytes < minBytes {
		return d
	}
	// 按实例版本归类(工单 08):原生秒完成的照原路径、不提示;原生会阻塞或重建表的走在线变更;
	// 在线变更做不了的照原路径并提示
	d.Parsed.Resolve(ver[0], ver[1], ver[2])
	switch {
	case d.Parsed.Native:
	case d.Parsed.Supported:
		d.Kind = OSCOnline
	default:
		d.Kind = OSCBigDirect
	}
	return d
}

// bigDirectNotice 是"大表直接 DDL"那句提示(规范中文;界面按 RuleRef 用读者的语言)。
func bigDirectNotice(d OSCDecision) (string, *model.RuleRef) {
	why := d.Parsed.Unsupported
	msg := fmt.Sprintf("表 %s 约 %s 行 / %s,这条 DDL 将直接执行(在线变更不支持:%s),可能长时间锁表",
		d.Table, thousands(d.Rows), humanBytes(int(d.Bytes)), why)
	return msg, model.NewRuleRef("oscBigDirect", "table", d.Table, "rows", thousands(d.Rows), "size", humanBytes(int(d.Bytes)), "why", why)
}

func thousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
