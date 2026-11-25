package service

import (
	"strconv"
	"strings"

	"velagateway/internal/dto"
	"velagateway/internal/gateway"
	"velagateway/internal/model"
)

// bizBrowseSource:浏览库结构(表、列)前的判定 —— 实例仍是业务查询源、不在维护态、调用人对该库有此刻
// 生效的授权。与执行同一套前半段。
func (s *Services) bizBrowseSource(u *model.User, sourceID int64, database string) (*model.Connection, string, error) {
	conn, err := s.Repo.GetConnection(sourceID)
	if err != nil {
		return nil, "", bizDeny(model.BizNoSource)
	}
	db := strings.TrimSpace(database)
	g := s.LiveQueryGrant(u.ID, conn.ID, db)
	if g == nil {
		return nil, "", bizDeny(model.BizNoGrant, "source", s.bizSourceLabel(u, conn), "db", db)
	}
	if !conn.BizQuery {
		return nil, "", bizDeny(model.BizNotSource, "source", conn.Name)
	}
	if conn.Status == model.ConnStatusMaint {
		return nil, "", bizDeny(model.BizMaintenance, "source", conn.Name)
	}
	return conn, g.DBName, nil // 用授权上的库名,见 prepareBizQuery
}

// bizSourceLabel 是拒绝理由里实例的说法。对这台实例有过授权的人(含已过期、已撤销)本来就知道它叫什么,
// 照旧显示名字,好让他知道该去续哪一条;没有过的人只给编号,否则拿 id 一路试就能枚举出全部实例名
// (2026-11 审计 V-10)。
func (s *Services) bizSourceLabel(u *model.User, conn *model.Connection) string {
	if n, err := s.Repo.CountQueryGrantsOn(u.ID, conn.ID); err == nil && n > 0 {
		return conn.Name
	}
	return "#" + strconv.FormatInt(conn.ID, 10)
}

func identQuote(engine string) string {
	if gateway.EngineFamilyOf(engine) == "mysql" {
		return "`"
	}
	return `"`
}

// bizSystemSchemas:补全取列时不许指向的系统库 / schema(业务查询本来就不让读它们)。
var bizSystemSchemas = map[string]bool{
	"information_schema": true, "mysql": true, "performance_schema": true, "sys": true,
	"pg_catalog": true, "pg_toast": true,
}

// BizColumns 列出授权库里一张表的列,给数据查询页的自动补全用(2026-10-26)。
//
// 判定与 BizTables 相同;另外表名带限定时,MySQL 系 / Oracle 的限定名必须就是授权的那个库(别的库连列名
// 都不该看到),PG 系的限定名是库里面的 schema,但不许是系统 schema。读不到不算拒绝:回 0 + Error。
func (s *Services) BizColumns(u *model.User, sourceID int64, database, table string) (*dto.BizColumnsResp, error) {
	conn, db, err := s.bizBrowseSource(u, sourceID, database)
	if err != nil {
		return nil, err
	}
	qual, t := gateway.SplitQualified(table)
	if t == "" {
		return nil, ErrBadRequest
	}
	if qual != "" {
		if bizSystemSchemas[strings.ToLower(qual)] {
			return nil, bizDeny(model.BizNoGrant, "source", conn.Name, "db", qual)
		}
		if gateway.EngineFamilyOf(conn.Engine) != "postgres" && !strings.EqualFold(qual, db) {
			return nil, bizDeny(model.BizNoGrant, "source", conn.Name, "db", qual)
		}
	}
	out := &dto.BizColumnsResp{Columns: []string{}}
	if !gateway.RealExecSupported(conn) {
		return out, nil // 演示连接没有可补的列,也不编一份
	}
	target, _, _ := s.bizTarget(conn) // 与查询同一台库:有从库走从库
	applyTargetDatabase(target, db)
	cols, cerr := gateway.TableColumns(target, table)
	if cerr != nil {
		out.Error = cerr.Error()
		return out, nil
	}
	out.Columns = cols
	return out, nil
}

// BizTables 列出业务方授权库里的表与视图,给数据查询页的"表"下拉用。
//
// 判定与执行同一套前半段:实例仍是业务查询源、不在维护态、调用人对该库有此刻生效的授权 ——
// 表名本身就是库结构信息,没有授权的库连有哪些表都不该看到。不占并发配额、不写审计:它只读
// 库目录,与终端树展开一样。
//
// 读不到(连不上、账号看不到目录)不算拒绝:回 0 + Error,下拉里空着,手写 SQL 照样能查。
func (s *Services) BizTables(u *model.User, sourceID int64, database string) (*dto.BizTablesResp, error) {
	conn, db, err := s.bizBrowseSource(u, sourceID, database)
	if err != nil {
		return nil, err
	}

	out := &dto.BizTablesResp{Tables: []string{}, Quote: identQuote(conn.Engine)}
	if gateway.RealExecSupported(conn) {
		target, _, _ := s.bizTarget(conn) // 与查询同一台库:有从库走从库
		names, truncated, err := gateway.RealBizTables(target, db)
		if err != nil {
			out.Error = err.Error()
			return out, nil
		}
		out.Tables, out.Truncated = names, truncated
		return out, nil
	}
	// 没有凭据的开发 / 演示连接:与执行同一条规矩,生产上不给模拟数据。
	if !gateway.SimulationAllowed() {
		out.Error = gateway.ErrSimulationDisabled.Error()
		return out, nil
	}
	// SIMULATED-PATH: biz-tables —— 见 docs/simulated-paths.md。
	objs, _ := s.Repo.SchemaForConnection(conn.ID)
	for _, o := range objs {
		if o.Database == db {
			out.Tables = append(out.Tables, o.Tbl)
		}
	}
	return out, nil
}
