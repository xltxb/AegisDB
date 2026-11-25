package service

// 发布收单前的语法与对象检查。
//
// ## 它回答两个问题,而且分得很开
//
//	语法  这条语句在**那台库、那个版本**上写对了没有 —— 由目标库 EXPLAIN 定夺
//	对象  它引用的表/视图在不在 —— 由元数据缓存或目标库的数据字典回答
//
// 两者都**不拦发布**。结果随响应返回、并留在发布单上;卡不卡由发布流程的阶段决定。
// 这是刻意的:语法检查覆盖不到 DDL(见 gateway/precheck.go),对象检查基于可能过期的
// 缓存,拿一个覆盖不全、可能过期的结论去拦正常变更,比不检查更糟。
//
// ## 每一条结论都要说清"是怎么知道的"
//
// 对象检查按「缓存优先,没有再查实时」取数 —— 于是同一个检查有两种准确度。如果结果里
// 看不出这一条是哪一种,读的人就没法判断该不该信它:一份三天前的缓存说"表不存在",
// 很可能只是那张表是昨天建的。所以每条对象结论都带 source,缓存的还带同步时间。
//
// ## 没做的检查必须显示成"没做"
//
// 整份结果里最容易骗人的是沉默:一条没被检查的 DDL 如果不出现在结果里,读的人会默认
// 它通过了。所以 skipped 是一等结论,跟 error 一样要报出来。

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"aegisdb/internal/dto"
	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
	"aegisdb/pkg/sqlutil"
)

// 上限。收单是同步的,这里每多一次往返都加在提单方的等待上。
const (
	maxPrecheckStmts  = 300 // 超出的部分不检查,并在结果里说明
	maxSyntaxChecks   = 50  // 每条 EXPLAIN 都是一次往返
	maxPrecheckIssues = 200 // 结果本身也要能读完
)

// 结论的种类。
const (
	PrecheckSyntax  = "syntax"
	PrecheckObject  = "object"
	PrecheckSkipped = "skipped"
	// PrecheckConstraint 是"这条语句会被**数据**顶回来":表里有 NULL 而要加非空、
	// 有重复值而要加唯一约束。它和 object 分开,是因为处置方式不同 —— object 要改
	// 脚本,constraint 通常要先清洗数据。
	PrecheckConstraint = "constraint"
	// PrecheckOSC:生产大表的索引变更会改走在线变更 —— 它的前置检查结论(.scratch/osc-product)。
	PrecheckOSC = "osc"
)

// maxProbes 一次收单最多跑几条数据探测。每一条都是对生产表的一次只读扫描。
const maxProbes = 20

// 对象检查的取数来源。none 表示这次根本没能核对对象 —— 它和"核对过、都在"是两回事。
const (
	ObjSourceCache = "cache"
	ObjSourceLive  = "live"
	ObjSourceNone  = "none"
)

// PrecheckIssue 是一条结论。
type PrecheckIssue struct {
	Stmt    int    `json:"stmt"` // 第几条语句(1 起)
	Level   string `json:"level"`
	Kind    string `json:"kind"`
	Object  string `json:"object,omitempty"`
	Message string `json:"message"`
	// Ref 是 Message 的机器可读形式(model.RenderPrecheck),界面据此用读者的语言显示;
	// 旧单没有它,界面回落到 Message。
	Ref *model.RuleRef `json:"ref,omitempty"`
	// Source 只在对象结论上有意义:这一条是缓存说的还是目标库说的。
	Source string `json:"source,omitempty"`
}

// PrecheckResult 是整份结果。
type PrecheckResult struct {
	Statements    int             `json:"statements"`
	Errors        int             `json:"errors"`
	Warnings      int             `json:"warnings"`
	Infos         int             `json:"infos"`
	SyntaxChecked int             `json:"syntaxChecked"`
	SyntaxSkipped int             `json:"syntaxSkipped"`
	ObjectSource  string          `json:"objectSource"`
	CacheSyncedAt string          `json:"cacheSyncedAt,omitempty"`
	Note          string          `json:"note,omitempty"`
	// NoteRefs 是 Note 的机器可读形式(一条附注一项);Note 是它们按原写法拼成的一行。
	NoteRefs []model.RuleRef  `json:"noteRefs,omitempty"`
	Issues   []PrecheckIssue `json:"issues"`
}

// addNote 记一条附注。Note 每次重算,无论之后从哪条路径返回,两者都一致。
func (r *PrecheckResult) addNote(ref *model.RuleRef) {
	r.NoteRefs = append(r.NoteRefs, *ref)
	r.Note = model.JoinPrecheckNotes(r.NoteRefs)
}

// add 记一条结论。Message 一律由 Ref 渲染(规范中文串),不在各处各拼一遍。
func (r *PrecheckResult) add(i PrecheckIssue) {
	if i.Ref != nil {
		i.Message = model.RenderPrecheck(i.Ref)
	}
	switch i.Level {
	case model.RiskHigh, "error":
		r.Errors++
	case "warn":
		r.Warnings++
	default:
		r.Infos++
	}
	if len(r.Issues) < maxPrecheckIssues {
		r.Issues = append(r.Issues, i)
	}
}

// precheckRelease 检查一份发布内容。
//
// 它永远返回一份结果,不返回 error:检查本身失败(连不上、没凭据)也是一条要告诉人的
// 结论,而不是让收单失败 —— 收单失败会让人以为是 SQL 有问题。
func (s *Services) precheckRelease(conn *model.Connection, stmts []string) *PrecheckResult {
	res := &PrecheckResult{Statements: len(stmts), Issues: []PrecheckIssue{}}
	if len(stmts) > maxPrecheckStmts {
		res.addNote(model.NewRuleRef(model.PreNoteTruncated, "total", model.Itoa(len(stmts)), "n", model.Itoa(maxPrecheckStmts)))
		stmts = stmts[:maxPrecheckStmts]
	}
	if !gateway.RealExecSupported(conn) {
		res.ObjectSource = ObjSourceNone
		res.add(PrecheckIssue{Level: "info", Kind: PrecheckSkipped,
			Ref: model.NewRuleRef(model.PreNoCreds)})
		return res
	}

	known, source, syncedAt := s.knownObjects(conn, res)
	res.ObjectSource = source
	if !syncedAt.IsZero() {
		res.CacheSyncedAt = syncedAt.Format(time.RFC3339)
	}

	// 字段信息**一律实时取**,不用缓存:字段比建表变得频繁得多,一份昨天的缓存会把
	// 昨天刚加的列报成"不存在",而那句话会让人去改一个本来正确的脚本。
	//
	// 一次取完这批脚本改到的所有表,而不是一条语句问一次 —— 收单是同步的。
	cols, colErr := s.alterColumns(conn, stmts)
	if colErr != nil {
		res.add(PrecheckIssue{Level: "info", Kind: PrecheckSkipped,
			Ref: model.NewRuleRef(model.PreColsUnreadable, "err", clip(colErr.Error(), 200))})
	}

	// 同一批脚本里 `CREATE TABLE t; INSERT INTO t …` 是正常写法:第二条的 t 在提交
	// 这一刻确实不存在。所以边走边记这批脚本自己建的和删的。
	created := map[string]bool{}
	dropped := map[string]bool{}
	// 字段同理:前一条 ALTER 加的列,后一条用它不算"不存在"。
	addedCols := map[string]bool{}
	droppedCols := map[string]bool{}
	syntaxBudget := maxSyntaxChecks
	probeBudget := maxProbes

	for i, raw := range stmts {
		n := i + 1
		refs := sqlutil.ObjectRefs(raw)
		objOK := true

		if source != ObjSourceNone && refs.Recognised {
			for _, r := range refs.Requires {
				key := refKey(r)
				switch {
				case created[key]:
					// 这批脚本前面建的,不算缺
				case dropped[key]:
					objOK = false
					res.add(PrecheckIssue{Stmt: n, Level: "error", Kind: PrecheckObject, Object: refName(r),
						Ref: model.NewRuleRef(model.PreObjDropped), Source: source})
				case !known[key]:
					objOK = false
					res.add(PrecheckIssue{Stmt: n, Level: "error", Kind: PrecheckObject, Object: refName(r),
						Ref: model.NewRuleRef(model.PreObjMissing), Source: source})
				}
			}
			for _, r := range refs.Creates {
				key := refKey(r)
				if (known[key] || created[key]) && !hasIfNotExists(raw) {
					res.add(PrecheckIssue{Stmt: n, Level: "warn", Kind: PrecheckObject, Object: refName(r),
						Ref: model.NewRuleRef(model.PreObjExists), Source: source})
				}
				created[key] = true
				delete(dropped, key)
			}
			for _, r := range refs.Drops {
				dropped[refKey(r)] = true
				delete(created, refKey(r))
			}
		} else if source != ObjSourceNone && !refs.Recognised {
			res.add(PrecheckIssue{Stmt: n, Level: "info", Kind: PrecheckSkipped,
				Ref: model.NewRuleRef(model.PreObjUnrecognised)})
		}

		// 字段与约束。只对 ALTER TABLE 做 —— 它是唯一会因为"列已存在/不存在"或
		// "表里的数据不满足新约束"而失败的常见形状。
		if colErr == nil {
			probeBudget = s.checkAlter(conn, res, n, raw, cols, created, addedCols, droppedCols, probeBudget)
		}

		// 语法。对象缺失时不查:EXPLAIN 会因为"表不存在"报错,把同一件事报成语法问题。
		switch {
		case !gateway.SyntaxCheckable(conn.Engine, raw):
			res.SyntaxSkipped++
			res.add(PrecheckIssue{Stmt: n, Level: "info", Kind: PrecheckSkipped,
				Ref: syntaxSkipReason(conn.Engine, raw)})
		case !objOK:
			res.SyntaxSkipped++
			res.add(PrecheckIssue{Stmt: n, Level: "info", Kind: PrecheckSkipped,
				Ref: model.NewRuleRef(model.PreSyntaxAfterMiss)})
		case syntaxBudget <= 0:
			res.SyntaxSkipped++
		default:
			syntaxBudget--
			res.SyntaxChecked++
			if err := gateway.CheckSyntax(conn, raw); err != nil {
				res.add(PrecheckIssue{Stmt: n, Level: "error", Kind: PrecheckSyntax,
					Ref: model.NewRuleRef(model.PreSyntaxError, "err", clip(err.Error(), 400))})
			}
		}
	}
	if syntaxBudget <= 0 && res.SyntaxSkipped > 0 {
		res.addNote(model.NewRuleRef(model.PreNoteSyntaxCap, "n", model.Itoa(maxSyntaxChecks)))
	}
	s.precheckOSC(conn, stmts, res)
	return res
}

// precheckOSC 给发布单预检加上在线变更的结论:哪几条会改走在线变更、它们的前置检查(阻塞算错误,
// 警告照列)、大表上做不了的 DDL 会直接执行。和别的预检结论一样不拦发布 —— 执行阶段会再查一遍。
func (s *Services) precheckOSC(conn *model.Connection, stmts []string, res *PrecheckResult) {
	ctx := context.Background()
	for i, raw := range stmts {
		d := s.OSCRoute(ctx, conn, raw)
		n := i + 1
		size := humanBytes(int(d.Bytes))
		switch d.Kind {
		case OSCBigDirect:
			res.add(PrecheckIssue{Stmt: n, Level: "warn", Kind: PrecheckOSC, Object: d.Table,
				Ref: model.NewRuleRef(model.PreOSCBigDirect, "table", d.Table, "rows", thousands(d.Rows), "size", size, "why", d.Parsed.Unsupported)})
		case OSCOnline:
			res.add(PrecheckIssue{Stmt: n, Level: "info", Kind: PrecheckOSC, Object: d.Table,
				Ref: model.NewRuleRef(model.PreOSCOnline, "table", d.Table, "rows", thousands(d.Rows), "size", size)})
			blockers, warnings, err := s.OSCPreflight(ctx, conn, d.Schema, d.Table, d.Parsed.Change)
			if err != nil {
				res.add(PrecheckIssue{Stmt: n, Level: "warn", Kind: PrecheckOSC, Object: d.Table,
					Ref: model.NewRuleRef(model.PreOSCUnread, "err", clip(err.Error(), 200))})
				continue
			}
			for _, b := range blockers {
				res.add(PrecheckIssue{Stmt: n, Level: "error", Kind: PrecheckOSC, Object: d.Table,
					Ref: model.NewRuleRef(model.PreOSCBlocked, "code", b.Code, "reason", b.Reason)})
			}
			for _, w := range warnings {
				res.add(PrecheckIssue{Stmt: n, Level: "warn", Kind: PrecheckOSC, Object: d.Table,
					Ref: model.NewRuleRef(model.PreOSCWarn, "code", w.Code, "reason", w.Reason)})
			}
		}
	}
}

// knownObjects 取这台实例上已有的对象清单 —— 缓存优先,缓存里没有再查实时。
//
// "缓存里没有"指的是**这台实例一条缓存都没有**(从没同步过),不是"某张表不在缓存里":
// 后者恰恰是要报出来的结论,回落实时就把这个检查变成了永远查实时。
func (s *Services) knownObjects(conn *model.Connection, res *PrecheckResult) (map[string]bool, string, time.Time) {
	rows, err := s.Repo.MetaTablesOf(conn.ID, effectiveDatabase(conn))
	if err == nil && len(rows) > 0 {
		set := map[string]bool{}
		var newest time.Time
		for _, t := range rows {
			gateway.AddTableKey(set, t.SchemaName, t.Name)
			if t.SyncedAt.After(newest) {
				newest = t.SyncedAt
			}
		}
		return set, ObjSourceCache, newest
	}
	set, lerr := gateway.LiveTableNames(conn)
	if lerr != nil {
		res.add(PrecheckIssue{Level: "info", Kind: PrecheckSkipped,
			Ref: model.NewRuleRef(model.PreObjsUnreadable, "err", clip(lerr.Error(), 200))})
		return nil, ObjSourceNone, time.Time{}
	}
	return set, ObjSourceLive, time.Time{}
}

func refKey(r sqlutil.Ref) string {
	if r.Schema != "" {
		return strings.ToLower(r.Schema + "." + r.Name)
	}
	return strings.ToLower(r.Name)
}

func refName(r sqlutil.Ref) string {
	if r.Schema != "" {
		return r.Schema + "." + r.Name
	}
	return r.Name
}

func hasIfNotExists(sql string) bool {
	return strings.Contains(strings.ToUpper(sql), "IF NOT EXISTS")
}

func syntaxSkipReason(engine, sql string) *model.RuleRef {
	verb := gateway.ParseVerb(sql)
	switch gateway.EngineFamilyOf(engine) {
	case "oracle":
		return model.NewRuleRef(model.PreSyntaxOracle)
	case "mongo":
		return model.NewRuleRef(model.PreSyntaxNotSQL)
	}
	if verb == "" {
		return model.NewRuleRef(model.PreSyntaxSkipped)
	}
	return model.NewRuleRef(model.PreSyntaxVerb, "verb", verb)
}

// precheckJSON 把结果序列化进发布单。失败时留空 —— 一个存不下的检查结果不该
// 让整次收单失败。
func precheckJSON(res *PrecheckResult) string {
	if res == nil {
		return ""
	}
	b, err := json.Marshal(res)
	if err != nil {
		return ""
	}
	return string(b)
}

// openPrecheckOf 把存在单子上的检查结果翻成对外的形状。
//
// 解析不出来就返回 nil:一份读不懂的结果,宁可让字段整个不出现,也不要给对接方一份
// 半截的、看起来像"没问题"的结果。老单子(0036 之前)没有这一栏,走的也是这条路。
func openPrecheckOf(raw string) *dto.OpenPrecheck {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var res PrecheckResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil
	}
	out := &dto.OpenPrecheck{
		Statements: res.Statements, Errors: res.Errors, Warnings: res.Warnings,
		SyntaxChecked: res.SyntaxChecked, SyntaxSkipped: res.SyntaxSkipped,
		ObjectSource: res.ObjectSource, CacheSyncedAt: res.CacheSyncedAt, Note: res.Note,
		Issues: make([]dto.OpenPrecheckIssue, 0, len(res.Issues)),
	}
	for _, i := range res.Issues {
		out.Issues = append(out.Issues, dto.OpenPrecheckIssue{
			Stmt: i.Stmt, Level: i.Level, Kind: i.Kind,
			Object: i.Object, Message: i.Message, Source: i.Source,
		})
	}
	return out
}

// alterColumns 一次取回这批脚本里所有 ALTER TABLE 目标表的列。
//
// 没有 ALTER 就不连库:一份纯 DML 的发布单不该因为这个功能多一次往返。
func (s *Services) alterColumns(conn *model.Connection, stmts []string) (map[string]map[string]bool, error) {
	targets := []sqlutil.Ref{}
	seen := map[string]bool{}
	for _, raw := range stmts {
		t, _, ok := sqlutil.AlterOps(raw)
		if !ok {
			continue
		}
		k := refKey(t)
		if seen[k] {
			continue
		}
		seen[k] = true
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return map[string]map[string]bool{}, nil
	}
	return gateway.LiveColumnsOf(conn, targets)
}

// checkAlter 查一条 ALTER TABLE 的字段与约束,返回剩余的探测预算。
//
// 三类结论:
//
//	字段已存在 / 不存在  看结构就能答,不碰数据
//	数据违反约束        必须探数据 —— 表里有行而要加无默认值的非空列、列里有 NULL
//	                   而要改成非空、列上有重复值而要加唯一约束
//	未能核对            探测失败或预算用尽,如实说,绝不当成通过
func (s *Services) checkAlter(
	conn *model.Connection, res *PrecheckResult, n int, raw string,
	cols map[string]map[string]bool, created, addedCols, droppedCols map[string]bool, budget int,
) int {
	table, ops, ok := sqlutil.AlterOps(raw)
	if !ok {
		return budget
	}
	tkey := refKey(table)
	// 这批脚本自己刚建的表:它此刻还没有列可查,查了只会得到一串假的"字段不存在"。
	if created[tkey] {
		return budget
	}
	have, known := cols[tkey]
	if !known {
		// 表在目标库上查不到列。可能是表不存在(上面已经报过了),也可能是权限 ——
		// 无论哪种,这里都不该下"字段不存在"的结论。
		res.add(PrecheckIssue{Stmt: n, Level: "info", Kind: PrecheckSkipped, Object: refName(table),
			Ref: model.NewRuleRef(model.PreTableColsUnread)})
		return budget
	}

	colKey := func(c string) string { return tkey + "." + strings.ToLower(c) }
	exists := func(c string) bool {
		k := colKey(c)
		if droppedCols[k] {
			return false
		}
		return have[strings.ToLower(c)] || addedCols[k]
	}
	missing := func(c string, op string) {
		res.add(PrecheckIssue{Stmt: n, Level: "error", Kind: PrecheckObject,
			Object: refName(table) + "." + c, Source: gateway.ObjSourceLiveTag,
			Ref: &model.RuleRef{Code: model.PreColMissing, Parts: []model.RuleRef{{Code: op}}}})
	}

	for _, op := range ops {
		switch op.Kind {
		case sqlutil.AlterAddColumn:
			if exists(op.Column) {
				res.add(PrecheckIssue{Stmt: n, Level: "error", Kind: PrecheckObject,
					Object: refName(table) + "." + op.Column, Source: gateway.ObjSourceLiveTag,
					Ref: model.NewRuleRef(model.PreColExists)})
				continue
			}
			addedCols[colKey(op.Column)] = true
			delete(droppedCols, colKey(op.Column))
			// 加一个非空且没有默认值的列:表里只要有一行就会失败。
			if op.NotNull && !op.HasDefault {
				budget = s.probeInto(res, n, budget, refName(table),
					model.NewRuleRef(model.PreProbeNotNull),
					func() (bool, error) { return gateway.ProbeRowsExist(conn, table) })
			}
		case sqlutil.AlterDropColumn:
			if !exists(op.Column) {
				missing(op.Column, model.PreOpDropColumn)
				continue
			}
			droppedCols[colKey(op.Column)] = true
			delete(addedCols, colKey(op.Column))
		case sqlutil.AlterModifyColumn, sqlutil.AlterSetNotNull:
			if !exists(op.Column) {
				missing(op.Column, model.PreOpModifyColumn)
				continue
			}
			if op.NotNull {
				col := op.Column
				budget = s.probeInto(res, n, budget, refName(table)+"."+col,
					model.NewRuleRef(model.PreProbeNulls),
					func() (bool, error) { return gateway.ProbeNullsExist(conn, table, col) })
			}
		case sqlutil.AlterChangeColumn:
			if !exists(op.Column) {
				missing(op.Column, model.PreOpRename)
				continue
			}
			if !strings.EqualFold(op.Column, op.NewColumn) && exists(op.NewColumn) {
				res.add(PrecheckIssue{Stmt: n, Level: "error", Kind: PrecheckObject,
					Object: refName(table) + "." + op.NewColumn, Source: gateway.ObjSourceLiveTag,
					Ref: model.NewRuleRef(model.PreRenameTarget)})
				continue
			}
			droppedCols[colKey(op.Column)] = true
			addedCols[colKey(op.NewColumn)] = true
		case sqlutil.AlterAddUnique:
			gone := false
			for _, c := range op.Columns {
				if !exists(c) {
					missing(c, model.PreOpAddUnique)
					gone = true
				}
			}
			if gone {
				continue
			}
			cs := op.Columns
			budget = s.probeInto(res, n, budget, refName(table)+"("+strings.Join(cs, ", ")+")",
				model.NewRuleRef(model.PreProbeDuplicates),
				func() (bool, error) { return gateway.ProbeDuplicatesExist(conn, table, cs) })
		}
	}
	return budget
}

// probeInto 跑一条数据探测并把结论记下来,返回剩余预算。
//
// 三种结局都要说话:命中(会失败)、没命中(不报,不占版面)、探不了(如实说"未能核对")。
// 第三种最要紧 —— 把探不了说成没问题,就是拿一个没做的检查冒充通过。
func (s *Services) probeInto(res *PrecheckResult, n, budget int, object string, hitRef *model.RuleRef, run func() (bool, error)) int {
	if budget <= 0 {
		res.add(PrecheckIssue{Stmt: n, Level: "info", Kind: PrecheckSkipped, Object: object,
			Ref: model.NewRuleRef(model.PreProbeBudget, "n", model.Itoa(maxProbes))})
		return budget
	}
	hit, err := run()
	if err != nil {
		res.add(PrecheckIssue{Stmt: n, Level: "info", Kind: PrecheckSkipped, Object: object,
			Ref: model.NewRuleRef(model.PreProbeFailed, "err", clip(err.Error(), 200))})
		return budget - 1
	}
	if hit {
		res.add(PrecheckIssue{Stmt: n, Level: "error", Kind: PrecheckConstraint, Object: object,
			Source: gateway.ObjSourceLiveTag, Ref: hitRef})
	}
	return budget - 1
}
