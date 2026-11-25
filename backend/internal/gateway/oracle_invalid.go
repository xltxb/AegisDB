package gateway

// Oracle 无效对象的清点与批量重编译。
//
// 一次表结构变更、一次被依赖过程的重建,能让几十个包、视图、触发器一起变成 INVALID。
// Oracle 会在下次调用时**隐式**重编译,所以这些对象看起来还能用 —— 直到某个业务请求
// 第一次踩上去,编译错误才在生产流量里出现。DBA 要的是变更窗口里就把它们编完、当场
// 看到哪些编不过,而不是等电话。
//
// 这就是 utlrp / UTL_RECOMP 在做的事,但那两个要 SYSDBA,而网关连的是业务账号。
// 所以这里自己走一遍:清点 → 判定 → 逐个编 → 回读状态。
//
// 三件必须守住的:
//
//  1. **执行仍然走单个编译那条路**(RealCompileObject)。批量不是一条新的下发通道,
//     它只是把同一件事做 N 次 —— 包括"ALTER … COMPILE 即使失败也返回成功、必须回读
//     all_objects.status 与 all_errors"这条(见 oracle_compile.go 的文件注释)。
//     另起一条更快的路,就等于给自己留一个不回读的后门。
//  2. **按依赖分层,再多轮**。编译顺序会影响结果:先编了依赖别人的那个,它照样失败;
//     等被依赖的编好了,它才能过。所以先读 all_dependencies,把这批对象按依赖关系分层
//     (被依赖的在下层,依赖别人的在上层),逐层往上编 —— 这正是 utlrp / UTL_RECOMP 的
//     做法。分层之后仍然保留"上一轮修好了至少一个就再来一轮"(最多三轮)作兜底:
//     包与包体互相引用这种环分不出上下,依赖图读不到时也得有条路。
//  3. **上限**。一次请求最多处理 maxRecompileTargets 个对象,超出的**明确报告为跳过**,
//     而不是悄悄只做一半 —— 悄悄做一半的后果是有人以为清干净了。

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
)

// maxRecompileTargets 一次批量重编译的对象上限。
//
// 每个对象要发 1~2 条 ALTER 再加两条回读查询,还要写一行审计;不设上限的话,一个
// 刚做完大改的 schema 能让这个请求跑上几分钟并写下上千行审计。超出的部分会明确
// 出现在报告的 Skipped 里,人再点一次即可。
const maxRecompileTargets = 200

// MaxRecompileTargets 让服务层用同一个上限做裁剪与"跳过"计数 —— 两处各写一个常量,
// 迟早会对不上,而对不上的表现是报告里的数字和实际做的事不一致。
func MaxRecompileTargets() int { return maxRecompileTargets }

// maxRecompilePasses 见文件注释第 2 条。
const maxRecompilePasses = 3

// InvalidObject 是一个处于 INVALID 状态的可编译对象。
//
// Units 记的是**具体哪几个单元**失效了:一个包的规范是好的、只有包体 INVALID,和
// 两者都 INVALID,对 DBA 来说不是一回事。编译动作本身按 Kind 走(编包就是规范加包体
// 一起编,理由见 oracleCompileSQL)。
type InvalidObject struct {
	Owner string   `json:"owner"`
	Name  string   `json:"name"`
	Kind  string   `json:"kind"`  // package / type / procedure / function / trigger / view / materialized view
	Units []string `json:"units"` // PACKAGE BODY / TYPE BODY / VIEW …
}

// RecompileItem 是一个对象重编译之后的结局。
type RecompileItem struct {
	Owner  string        `json:"owner"`
	Name   string        `json:"name"`
	Kind   string        `json:"kind"`
	Status string        `json:"status"` // VALID / INVALID / ERROR
	Errors []CompileDiag `json:"errors,omitempty"`
	Err    string        `json:"err,omitempty"` // 连编译都没跑起来时的原因
	Passes int           `json:"passes"`        // 这个对象实际被编了几轮
	// Level 是它在这批对象的依赖图里的层:1 = 不依赖这批里的任何对象,n = 它依赖的
	// 对象最高在 n-1 层。同一层的对象互不依赖,先编低层再编高层。
	Level int `json:"level"`
	// Cyclic:它和这批里的别的对象互相依赖(两个互相调用的包),分不出上下,被放在
	// 最高一层,靠多轮兜底。
	Cyclic bool `json:"cyclic,omitempty"`
}

// RecompileReport 是一次批量重编译的全部结果。
type RecompileReport struct {
	Owner   string `json:"owner"`
	Total   int    `json:"total"`   // 本次真正处理的对象数
	Fixed   int    `json:"fixed"`   // 编完变 VALID 的
	Failed  int    `json:"failed"`  // 编完仍然 INVALID 或出错的
	Skipped int    `json:"skipped"` // 超出上限、本次没碰的
	Passes  int    `json:"passes"`
	Levels  int    `json:"levels"` // 依赖分了几层
	Cyclic  int    `json:"cyclic"` // 处于依赖环里、分不出层的对象数
	// DepsNote 是依赖图读不到时的说明。此时全部按第 1 层、按清单顺序编,只靠多轮 ——
	// 人看到"3 轮还剩很多"时要知道这次没有分层,而不是以为分层也救不了。
	DepsNote string          `json:"depsNote,omitempty"`
	Ms       int             `json:"ms"`
	Items    []RecompileItem `json:"items"`
}

// DepEdge 是这批对象之间的一条依赖:targets[From] 依赖 targets[To]。
// 只记这批对象**内部**的边:一个对象依赖的东西如果不在这批里(别的 schema、或本来就
// VALID),编译顺序管不到它,记下来也没用。
type DepEdge struct{ From, To int }

// oracleCompileKind 把 all_objects.object_type 映射到编译动作的类型。
// 认不出来的返回空 —— 同义词、Java 类等等也会 INVALID,但它们不是 ALTER … COMPILE
// 能修的东西,列出来只会让人以为按一下就好了。
func oracleCompileKind(objectType string) string {
	switch strings.ToUpper(strings.TrimSpace(objectType)) {
	case "PACKAGE", "PACKAGE BODY":
		return "package"
	case "TYPE", "TYPE BODY":
		return "type"
	case "PROCEDURE":
		return "procedure"
	case "FUNCTION":
		return "function"
	case "TRIGGER":
		return "trigger"
	case "VIEW":
		return "view"
	case "MATERIALIZED VIEW":
		return "materialized view"
	}
	return ""
}

// oracleInvalidTypes 是清点时会去看的 object_type,与 oracleCompileKind 一一对应。
var oracleInvalidTypes = []string{
	"PACKAGE", "PACKAGE BODY", "TYPE", "TYPE BODY",
	"PROCEDURE", "FUNCTION", "TRIGGER", "VIEW", "MATERIALIZED VIEW",
}

// RealInvalidObjects 清点一个 schema 下所有 INVALID 的可编译对象。
//
// 只读,不改任何东西 —— 它和列对象、看 DDL 属于同一类操作,所以不经过 DDL 判定。
func RealInvalidObjects(conn *model.Connection, owner string) ([]InvalidObject, error) {
	if engineFamily(conn.Engine) != familyOracle {
		return nil, apimsg.Errorf("无效对象清点是 Oracle 专有能力,当前实例引擎为 %s", conn.Engine)
	}
	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	lookupOwner, err := oracleLookupOwner(ctx, db, owner)
	if err != nil {
		return nil, err
	}

	// 类型清单是代码里的常量,不是用户输入,所以直接拼进 IN;绑定变量在这里帮不上忙
	// (Oracle 的 IN 列表没法用一个绑定变量表示)。
	quoted := make([]string, 0, len(oracleInvalidTypes))
	for _, t := range oracleInvalidTypes {
		quoted = append(quoted, "'"+t+"'")
	}
	rows, err := db.QueryContext(ctx,
		`SELECT object_name, object_type FROM all_objects
		 WHERE owner = :1 AND status = 'INVALID'
		   AND object_type IN (`+strings.Join(quoted, ",")+`)
		 ORDER BY object_name, object_type`, lookupOwner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 一个包的 PACKAGE 与 PACKAGE BODY 是两行,但只有一个编译动作 —— 合成一条,
	// 否则报告里会出现两个同名条目,而它们其实是被同一次 ALTER 一起编的。
	idx := map[string]int{}
	out := []InvalidObject{}
	for rows.Next() {
		var name, otype string
		if err := rows.Scan(&name, &otype); err != nil {
			return nil, err
		}
		kind := oracleCompileKind(otype)
		if kind == "" {
			continue
		}
		key := kind + "\x00" + name
		if i, ok := idx[key]; ok {
			out[i].Units = append(out[i].Units, strings.ToUpper(otype))
			continue
		}
		idx[key] = len(out)
		out = append(out, InvalidObject{
			Owner: lookupOwner, Name: name, Kind: kind,
			Units: []string{strings.ToUpper(otype)},
		})
	}
	return out, rows.Err()
}

// RealInvalidDependencies 读这批对象之间的依赖关系(all_dependencies)。
//
// 只看同一个 owner 内、且两端都在这批里的边。包体依赖包规范这种边会被折掉:两者在
// 这里是同一个编译动作(见 RealInvalidObjects 里的合并),自己依赖自己没有意义。
func RealInvalidDependencies(conn *model.Connection, targets []InvalidObject) ([]DepEdge, error) {
	if len(targets) < 2 {
		return nil, nil
	}
	db, release, err := openConn(conn)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), objectQueryTimeout)
	defer cancel()

	owner := strings.ToUpper(strings.TrimSpace(targets[0].Owner))
	quoted := make([]string, 0, len(oracleInvalidTypes))
	for _, t := range oracleInvalidTypes {
		quoted = append(quoted, "'"+t+"'")
	}
	in := strings.Join(quoted, ",")
	rows, err := db.QueryContext(ctx,
		`SELECT name, type, referenced_name, referenced_type FROM all_dependencies
		 WHERE owner = :1 AND referenced_owner = :2
		   AND type IN (`+in+`) AND referenced_type IN (`+in+`)`, owner, owner)
	if err != nil {
		return nil, apimsg.Errorf("读取对象依赖失败: %w", err)
	}
	defer rows.Close()
	var raw []depRow
	for rows.Next() {
		var r depRow
		if err := rows.Scan(&r.Name, &r.Type, &r.RefName, &r.RefType); err != nil {
			return nil, err
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return edgesAmong(targets, raw), nil
}

// depRow 是 all_dependencies 的一行:Name/Type 依赖 RefName/RefType。
type depRow struct{ Name, Type, RefName, RefType string }

// edgesAmong 把 all_dependencies 的行折成这批对象内部的边。单独拆出来是为了不用真实
// Oracle 也能钉住"包体→包规范折掉、不在这批里的忽略、重复边只记一次"这几条。
func edgesAmong(targets []InvalidObject, raw []depRow) []DepEdge {
	key := func(kind, name string) string {
		return strings.ToLower(kind) + "|" + strings.ToUpper(strings.TrimSpace(name))
	}
	idx := make(map[string]int, len(targets))
	for i, o := range targets {
		idx[key(o.Kind, o.Name)] = i
	}
	seen := map[DepEdge]bool{}
	var out []DepEdge
	for _, r := range raw {
		fk, tk := oracleCompileKind(r.Type), oracleCompileKind(r.RefType)
		if fk == "" || tk == "" {
			continue
		}
		from, ok1 := idx[key(fk, r.Name)]
		to, ok2 := idx[key(tk, r.RefName)]
		if !ok1 || !ok2 || from == to {
			continue
		}
		e := DepEdge{From: from, To: to}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// levelize 按依赖关系分层:第 1 层不依赖这批里的任何对象,往上每一层只依赖更低的层。
// 剥不下来的(处于环里,或依赖环里对象的)全放进最后一层,按清单顺序 —— 它们靠多轮
// 兜底。返回每层的对象下标,以及最后那一层里有多少个对象。
func levelize(n int, edges []DepEdge) (levels [][]int, cyclic int) {
	rev := make([][]int, n) // rev[j] = 依赖 j 的对象
	indeg := make([]int, n)
	for _, e := range edges {
		if e.From < 0 || e.From >= n || e.To < 0 || e.To >= n || e.From == e.To {
			continue
		}
		rev[e.To] = append(rev[e.To], e.From)
		indeg[e.From]++
	}
	placed := make([]bool, n)
	var cur []int
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			cur = append(cur, i)
		}
	}
	for len(cur) > 0 {
		levels = append(levels, cur)
		var next []int
		for _, i := range cur {
			placed[i] = true
			for _, j := range rev[i] {
				indeg[j]--
				if indeg[j] == 0 {
					next = append(next, j)
				}
			}
		}
		// 同一层内保持清单顺序:报告读起来稳定,审计里的顺序也可复现。
		sort.Ints(next)
		cur = next
	}
	var rest []int
	for i := 0; i < n; i++ {
		if !placed[i] {
			rest = append(rest, i)
		}
	}
	if len(rest) > 0 {
		levels = append(levels, rest)
		cyclic = len(rest)
	}
	return levels, cyclic
}

// RealInvalidDependents 找出**直接或间接**依赖 root、且当前处于 INVALID 的对象。
//
// 用在单个编译之后的连带编译上:编好一个包,依赖它的那几个 Oracle 已经标成了
// INVALID,DBA 接下来要做的就是把它们也编掉。与批量重编译同一个范围 —— 同一个
// owner 内;跨 schema 的依赖不跟。
func RealInvalidDependents(conn *model.Connection, root InvalidObject) ([]InvalidObject, error) {
	all, err := RealInvalidObjects(conn, root.Owner)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, nil
	}
	root.Owner = all[0].Owner // 空 owner 已被解析成当前用户
	// root 自己若还在清单里要先剔掉:edgesAmong 按 kind|NAME 建索引,同一个键出现两次,
	// 后一个会盖掉 set[0],指向 root 的边就落到了那份副本上。
	set := []InvalidObject{root}
	for _, o := range all {
		if !(strings.EqualFold(o.Kind, root.Kind) && strings.EqualFold(o.Name, root.Name)) {
			set = append(set, o)
		}
	}
	edges, err := RealInvalidDependencies(conn, set)
	if err != nil {
		return nil, err
	}
	return dependentsOf(set, edges), nil
}

// dependentsOf 返回 set[1:] 中直接或间接依赖 set[0] 的对象,保持清单顺序。
//
// set[0] 自己如果也出现在后面(它编完仍是 INVALID 时清点会再列出它)不算自己的依赖方。
func dependentsOf(set []InvalidObject, edges []DepEdge) []InvalidObject {
	if len(set) < 2 {
		return nil
	}
	rev := make([][]int, len(set)) // rev[j] = 依赖 j 的对象
	for _, e := range edges {
		if e.From >= 0 && e.From < len(set) && e.To >= 0 && e.To < len(set) {
			rev[e.To] = append(rev[e.To], e.From)
		}
	}
	rootKey := strings.ToLower(set[0].Kind) + "|" + strings.ToUpper(strings.TrimSpace(set[0].Name))
	hit := make([]bool, len(set))
	hit[0] = true
	queue := []int{0}
	for len(queue) > 0 {
		j := queue[0]
		queue = queue[1:]
		for _, i := range rev[j] {
			if !hit[i] {
				hit[i] = true
				queue = append(queue, i)
			}
		}
	}
	var out []InvalidObject
	for i := 1; i < len(set); i++ {
		o := set[i]
		if hit[i] && strings.ToLower(o.Kind)+"|"+strings.ToUpper(strings.TrimSpace(o.Name)) != rootKey {
			out = append(out, o)
		}
	}
	return out
}

// oracleLookupOwner 解析要查的 schema。空表示"当前登录用户",而 all_objects 表达不了
// 这个意思,所以在这里一次性问清楚(与 RealCompileObject 里那段同一个理由)。
func oracleLookupOwner(ctx context.Context, db *sql.DB, owner string) (string, error) {
	o := strings.ToUpper(strings.TrimSpace(owner))
	if o != "" {
		return o, nil
	}
	if err := db.QueryRowContext(ctx, `SELECT USER FROM dual`).Scan(&o); err != nil {
		return "", apimsg.Errorf("无法确定当前 schema: %w", err)
	}
	return o, nil
}

// RecompileTargets 逐个重编译给定的对象,多轮直到不再有进展。
//
// 执行仍然复用 RealCompileObject —— 批量不是一条新的下发通道(见文件注释)。
// onDone 在**每个对象最终定案后**回调一次,让上层去记审计:审计要的是"这个对象这次
// 被编成了什么",而不是中间每一轮的过程(单个编译一次记一行,这里保持一致)。
func RecompileTargets(conn *model.Connection, targets []InvalidObject,
	onDone func(obj InvalidObject, rep *CompileReport, err error)) *RecompileReport {
	// 依赖图读不到(视图上没权限、查询超时)不是放弃的理由:退回"全部第 1 层、按清单
	// 顺序编 + 多轮",并在报告里说明。
	edges, derr := RealInvalidDependencies(conn, targets)
	rep := recompileWith(targets, edges, func(o InvalidObject) (*CompileReport, error) {
		return RealCompileObject(conn, o.Owner, o.Kind, o.Name)
	}, onDone)
	if derr != nil {
		rep.DepsNote = strings.TrimSpace(derr.Error())
	}
	return rep
}

// recompileWith 是上面那段的可测形态:依赖边与编译动作作为参数传进来。
//
// 分层与多轮的规则("低层先编"、"这一轮一个都没修好就停"、"最多三轮")是这个功能里最
// 容易悄悄退化的一段 —— 退化之后它照样返回一份看起来正常的报告,只是有些本来能修好
// 的对象留在了 INVALID。没有真实 Oracle 可跑集成测试,把这两样抽出来是唯一能钉住它
// 的办法。
func recompileWith(targets []InvalidObject, edges []DepEdge, compile func(InvalidObject) (*CompileReport, error),
	onDone func(obj InvalidObject, rep *CompileReport, err error)) *RecompileReport {

	rep := &RecompileReport{Passes: 0}
	if len(targets) > 0 {
		rep.Owner = targets[0].Owner
	}
	start := time.Now()

	// 每个对象的最终结果,按清单顺序保留。
	items := make([]RecompileItem, len(targets))
	lastRep := make([]*CompileReport, len(targets))
	lastErr := make([]error, len(targets))
	for i, o := range targets {
		items[i] = RecompileItem{Owner: o.Owner, Name: o.Name, Kind: o.Kind, Status: "INVALID", Level: 1}
	}

	// 处理顺序 = 逐层往上;同一层内保持清单顺序。环里的那些标出来。
	levels, cyclic := levelize(len(targets), edges)
	rep.Levels, rep.Cyclic = len(levels), cyclic
	pending := make([]int, 0, len(targets))
	for li, lv := range levels {
		for _, i := range lv {
			items[i].Level = li + 1
			items[i].Cyclic = cyclic > 0 && li == len(levels)-1
			pending = append(pending, i)
		}
	}

	for pass := 1; pass <= maxRecompilePasses && len(pending) > 0; pass++ {
		rep.Passes = pass
		next := make([]int, 0, len(pending))
		fixedThisPass := 0
		for _, i := range pending {
			o := targets[i]
			r, err := compile(o)
			items[i].Passes = pass
			lastRep[i], lastErr[i] = r, err
			switch {
			case err != nil:
				// 编译这一个失败(名字非法、对象没了、权限不够……)不该让整批停下:
				// 剩下的对象和它无关。
				items[i].Status = "ERROR"
				items[i].Err = strings.TrimSpace(err.Error())
				items[i].Errors = nil
			case r.OK():
				items[i].Status = "VALID"
				items[i].Err = ""
				items[i].Errors = nil
				fixedThisPass++
			default:
				items[i].Status = "INVALID"
				items[i].Err = ""
				items[i].Errors = r.Errors
				next = append(next, i)
			}
		}
		// 这一轮一个都没修好,再来一轮也不会有变化 —— 剩下的是真编不过,不是顺序问题。
		if fixedThisPass == 0 {
			break
		}
		pending = next
	}

	for i, o := range targets {
		if onDone != nil {
			onDone(o, lastRep[i], lastErr[i])
		}
		if items[i].Status == "VALID" {
			rep.Fixed++
		} else {
			rep.Failed++
		}
	}
	rep.Total = len(targets)
	rep.Items = items
	rep.Ms = int(time.Since(start).Milliseconds())
	return rep
}
