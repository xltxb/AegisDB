package service

// 跨环境表结构对比(见 .scratch/schema-diff/PRD.md)。
//
// 判定:菜单 schemadiff(路由上)→ 左右两个实例调用人都能访问(与终端同一个判断)→ 两边引擎
// 同一族。两边并行实时读目录,任一边读失败整次失败(不出半份报告);每次对比写一条审计。

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/internal/schemadiff"
)

// SchemaDiffConnections 列出调用人能访问、引擎支持对比的实例。
func (s *Services) SchemaDiffConnections(u *model.User) ([]dto.SchemaDiffConn, error) {
	conns, err := s.Repo.ListConnections()
	if err != nil {
		return nil, err
	}
	out := []dto.SchemaDiffConn{}
	for i := range conns {
		c := &conns[i]
		fam := schemadiff.FamilyName(c.Engine)
		if fam == "" || !gateway.RealExecSupported(c) || !s.canAccessConn(u, c) {
			continue
		}
		out = append(out, dto.SchemaDiffConn{ID: c.ID, Name: c.Name, Env: c.Env, Engine: c.Engine, Family: fam, Database: defaultDiffDB(c)})
	}
	return out, nil
}

// defaultDiffDB 是一个实例默认比的库:配置的默认库;Oracle 的"数据库"字段是服务名,默认比登录账号
// 自己的 schema;SQLite 只有 main。
func defaultDiffDB(c *model.Connection) string {
	switch schemadiff.FamilyName(c.Engine) {
	case "oracle":
		return strings.ToUpper(c.Username)
	case "sqlite":
		return "main"
	}
	return c.Database
}

// CompareSchemas 比较两个库的结构。
func (s *Services) CompareSchemas(u *model.User, req dto.SchemaDiffReq) (*dto.SchemaDiffResp, error) {
	sides := [2]dto.SchemaDiffSide{req.Left, req.Right}
	names := [2]string{"左边", "右边"}
	var conns [2]*model.Connection
	sideCode := [2]string{"left", "right"}
	for i, sd := range sides {
		c, err := s.Repo.GetConnection(sd.ConnectionID)
		if err != nil {
			return nil, sdDeny(false, sdNoConn, "side", sideCode[i])
		}
		// 够不到的实例和不存在的实例同一个回答:报错带实例名、或两种回答不一样,都能拿 id 枚举出
		// 全部实例(2026-11 审计 V-10)。下拉本来就只列够得到的,正常操作看不到这句。
		if !s.canAccessConn(u, c) {
			return nil, sdDeny(false, sdNoConn, "side", sideCode[i])
		}
		if !gateway.RealExecSupported(c) {
			return nil, sdDeny(false, sdNoCreds, "side", sideCode[i], "instance", c.Name)
		}
		conns[i] = c
	}
	if !schemadiff.SameFamily(conns[0].Engine, conns[1].Engine) {
		return nil, sdDeny(false, sdEngineMismatch, "left", conns[0].Engine, "right", conns[1].Engine)
	}
	for i := range sides {
		if strings.TrimSpace(sides[i].Database) == "" {
			sides[i].Database = defaultDiffDB(conns[i])
		}
		sides[i].Database = strings.TrimSpace(sides[i].Database)
		if schemadiff.FamilyName(conns[i].Engine) != "postgres" {
			sides[i].Schema = "" // 只有 PG / DWS 有"库 + schema"两层
		}
		sides[i].Schema = strings.TrimSpace(sides[i].Schema)
	}

	var snaps [2]*schemadiff.Snapshot
	var errs [2]error
	var wg sync.WaitGroup
	for i := range sides {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			snaps[i], errs[i] = gateway.RealSchemaSnapshot(conns[i], sides[i].Database, sides[i].Schema)
		}(i)
	}
	wg.Wait()

	resp := &dto.SchemaDiffResp{}
	views := [2]*dto.SchemaDiffSideView{&resp.Left, &resp.Right}
	scope := [2]string{}
	for i, c := range conns {
		*views[i] = dto.SchemaDiffSideView{ConnectionID: c.ID, Instance: c.Name, Env: c.Env, Engine: c.Engine,
			Database: sides[i].Database, Schema: sides[i].Schema}
		if snaps[i] != nil {
			views[i].ReadAt, views[i].Tables = snaps[i].ReadAt, len(snaps[i].Tables)
		}
		scope[i] = c.Name + "/" + sides[i].Database
		if sides[i].Schema != "" {
			scope[i] += "." + sides[i].Schema
		}
	}
	command := fmt.Sprintf("SCHEMA-DIFF %s <-> %s", scope[0], scope[1])
	for i, err := range errs {
		if err != nil {
			ref := readFailureRef(err, conns[i].Name, scope[i])
			resp.ErrorRef, resp.ErrorSide = ref, [2]string{"left", "right"}[i]
			resp.Error = fmt.Sprintf("%s(%s)%s", names[i], scope[i], renderReadFailure(ref))
			// 审计里只记失败的归类,不记驱动报错原文:审计是哈希链,写进去就改不掉,而报错原文可能
			// 带着主机名、账号这类不该留在审计里的东西。原文在接口返回里给当事人看。
			s.auditSchemaDiff(u, conns, fmt.Sprintf("%s · %s读取失败(%s)", command, names[i], failureClass(ref)), model.ResultWarn)
			return resp, nil
		}
	}
	// 读到 0 张表不是错误(新环境本来可能是空库),但多半是库名写错了(MySQL 查一个不存在的库
	// 不报错,只是没有行)—— 照常出报告,另告诉页面提示一句。
	switch l, r := len(snaps[0].Tables) == 0, len(snaps[1].Tables) == 0; {
	case l && r:
		resp.EmptySide = "both"
	case l:
		resp.EmptySide = "left"
	case r:
		resp.EmptySide = "right"
	}
	resp.Diff = schemadiff.DiffSchemas(snaps[0], snaps[1], req.Options)
	sm := resp.Diff.Summary
	s.auditSchemaDiff(u, conns, fmt.Sprintf("%s · 仅左 %d · 仅右 %d · 不同 %d · 一致 %d",
		command, sm.OnlyLeft, sm.OnlyRight, sm.Changed, sm.Same), model.ResultExecuted)
	return resp, nil
}

// auditSchemaDiff 给这次对比写审计:左右是两个实例时各记一条,按实例筛审计时两边都查得到。
// operator 留空 —— 它表示"授权者",这里是操作人自己读了结构,没有别人授权。
func (s *Services) auditSchemaDiff(u *model.User, conns [2]*model.Connection, command, result string) {
	s.recordAuditBy(u, "", conns[0], command, model.RiskLow, result, "", model.AuditEventExec)
	if conns[1].ID != conns[0].ID {
		s.recordAuditBy(u, "", conns[1], command, model.RiskLow, result, "", model.AuditEventExec)
	}
}

// SchemaDiffDenial 是对比前的拒绝:实例不存在 / 没配凭据 / 引擎不同(40001),无权访问一边(40300)。
// 与读失败一样带原因代码,前端按读者语言显示(sdErr_* 词条);Error() 是规范中文串(msg)。
type SchemaDiffDenial struct {
	Ref       *model.RuleRef
	Forbidden bool
}

func (e *SchemaDiffDenial) Error() string { return renderSchemaDiffReason(e.Ref) }

func sdDeny(forbidden bool, code string, kv ...string) error {
	return &SchemaDiffDenial{Ref: model.NewRuleRef(code, kv...), Forbidden: forbidden}
}

// 对比前拒绝的原因代码。side 参数是 left / right,前端换成读者语言的"左边 / 右边"。
const (
	sdNoConn         = "sdNoConn"         // 实例不存在或无权访问(args: side)
	sdNoCreds        = "sdNoCreds"        // 实例没配真实执行凭据(args: side, instance)
	sdEngineMismatch = "sdEngineMismatch" // 两边引擎族不同(args: left, right)
)

var sideZh = map[string]string{"left": "左边", "right": "右边"}

func renderSchemaDiffReason(r *model.RuleRef) string {
	a := r.Args
	switch r.Code {
	case sdNoConn:
		if a["side"] == "" {
			return "实例不存在或无权访问"
		}
		return sideZh[a["side"]] + "的实例不存在或无权访问"
	case sdNoCreds:
		return sideZh[a["side"]] + "实例 " + a["instance"] + " 未配置真实执行凭据,读不了表结构"
	case sdEngineMismatch:
		return "两边引擎不同(" + a["left"] + " / " + a["right"] + "),表结构无法对比"
	}
	return renderReadFailure(r)
}

// 读目录失败的原因代码(前端 sdErr_* 词条按读者语言显示)。
const (
	sdReadFailed  = "sdReadFailed"  // 连不上、没权限、库报错(args: err 驱动原文,不翻译)
	sdReadTimeout = "sdReadTimeout" // 超过时限(args: sec)
	sdTooLarge    = "sdTooLarge"    // 表太多(args: n)
)

func readFailureRef(err error, instance, scope string) *model.RuleRef {
	switch {
	case errors.Is(err, gateway.ErrSnapshotTimeout):
		return model.NewRuleRef(sdReadTimeout, "instance", instance, "db", scope, "sec", model.Itoa(gateway.SnapshotTimeoutSec))
	case errors.Is(err, gateway.ErrSnapshotTooLarge):
		return model.NewRuleRef(sdTooLarge, "instance", instance, "db", scope, "n", model.Itoa(gateway.MaxSnapshotTables))
	}
	return model.NewRuleRef(sdReadFailed, "instance", instance, "db", scope, "err", err.Error())
}

// renderReadFailure 是原因的规范中文串(接口 error 字段;前端认得 errorRef 时不用它)。
func renderReadFailure(r *model.RuleRef) string {
	switch r.Code {
	case sdReadTimeout:
		return "读取表结构超过 " + r.Args["sec"] + " 秒"
	case sdTooLarge:
		return "库里超过 " + r.Args["n"] + " 张表,一次对比读不完,请缩小范围"
	}
	return "读取表结构失败:" + r.Args["err"]
}

// failureClass 是写进审计的失败归类(不含驱动原文)。
func failureClass(r *model.RuleRef) string {
	switch r.Code {
	case sdReadTimeout:
		return "超时"
	case sdTooLarge:
		return "表太多"
	}
	return "连接或读取出错"
}

// SchemaDiffDatabases 列出一个实例上可以比的库(页面的库名下拉)。判定与对比本身一样:能访问这个
// 实例、配了凭据、引擎支持对比。读不到库列表不是拒绝 —— 页面退回手填库名,所以原因放在 readErr 里。
func (s *Services) SchemaDiffDatabases(u *model.User, id int64) (names []string, readErr string, err error) {
	c, err := s.schemaDiffConn(u, id)
	if err != nil {
		return nil, "", err
	}
	names, rerr := gateway.DiffDatabaseNames(c)
	if rerr != nil {
		return nil, rerr.Error(), nil
	}
	return names, "", nil
}

// SchemaDiffSchemas 列出 PG / DWS 一个库里可以比的 schema(页面的 schema 下拉)。判定同 SchemaDiffDatabases;
// 不是 PG 系的实例没有这一层,回空列表。
func (s *Services) SchemaDiffSchemas(u *model.User, id int64, database string) (names []string, readErr string, err error) {
	c, err := s.schemaDiffConn(u, id)
	if err != nil {
		return nil, "", err
	}
	if schemadiff.FamilyName(c.Engine) != "postgres" {
		return []string{}, "", nil
	}
	names, rerr := gateway.DiffSchemaNames(c, database)
	if rerr != nil {
		return nil, rerr.Error(), nil
	}
	return names, "", nil
}

// schemaDiffConn 是库 / schema 下拉共用的判定:实例存在、调用人能访问、配了凭据、引擎支持对比。
func (s *Services) schemaDiffConn(u *model.User, id int64) (*model.Connection, error) {
	c, err := s.Repo.GetConnection(id)
	if err != nil {
		return nil, sdDeny(false, sdNoConn)
	}
	if !s.canAccessConn(u, c) {
		return nil, sdDeny(false, sdNoConn) // 与不存在同一个回答,见 CompareSchemas
	}
	if !gateway.RealExecSupported(c) || schemadiff.FamilyName(c.Engine) == "" {
		return nil, sdDeny(false, sdNoCreds, "instance", c.Name)
	}
	return c, nil
}
