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
//  2. **多轮**。编译顺序会影响结果:先编了依赖别人的那个,它照样失败;等被依赖的
//     编好了,它才能过。所以按"上一轮修好了至少一个就再来一轮"重试,最多三轮 ——
//     再多就不是依赖顺序问题,而是真的编不过。
//  3. **上限**。一次请求最多处理 maxRecompileTargets 个对象,超出的**明确报告为跳过**,
//     而不是悄悄只做一半 —— 悄悄做一半的后果是有人以为清干净了。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

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
}

// RecompileReport 是一次批量重编译的全部结果。
type RecompileReport struct {
	Owner   string          `json:"owner"`
	Total   int             `json:"total"`   // 本次真正处理的对象数
	Fixed   int             `json:"fixed"`   // 编完变 VALID 的
	Failed  int             `json:"failed"`  // 编完仍然 INVALID 或出错的
	Skipped int             `json:"skipped"` // 超出上限、本次没碰的
	Passes  int             `json:"passes"`
	Ms      int             `json:"ms"`
	Items   []RecompileItem `json:"items"`
}

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
		return nil, fmt.Errorf("无效对象清点是 Oracle 专有能力,当前实例引擎为 %s", conn.Engine)
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

// oracleLookupOwner 解析要查的 schema。空表示"当前登录用户",而 all_objects 表达不了
// 这个意思,所以在这里一次性问清楚(与 RealCompileObject 里那段同一个理由)。
func oracleLookupOwner(ctx context.Context, db *sql.DB, owner string) (string, error) {
	o := strings.ToUpper(strings.TrimSpace(owner))
	if o != "" {
		return o, nil
	}
	if err := db.QueryRowContext(ctx, `SELECT USER FROM dual`).Scan(&o); err != nil {
		return "", fmt.Errorf("无法确定当前 schema: %w", err)
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
	return recompileWith(targets, func(o InvalidObject) (*CompileReport, error) {
		return RealCompileObject(conn, o.Owner, o.Kind, o.Name)
	}, onDone)
}

// recompileWith 是上面那段的可测形态:编译动作作为参数传进来。
//
// 多轮的规则("这一轮一个都没修好就停"、"最多三轮")是这个功能里最容易悄悄退化的
// 一段 —— 退化之后它照样返回一份看起来正常的报告,只是有些本来能修好的对象留在了
// INVALID。没有真实 Oracle 可跑集成测试,把编译动作抽出来是唯一能钉住它的办法。
func recompileWith(targets []InvalidObject, compile func(InvalidObject) (*CompileReport, error),
	onDone func(obj InvalidObject, rep *CompileReport, err error)) *RecompileReport {

	rep := &RecompileReport{Passes: 0}
	if len(targets) > 0 {
		rep.Owner = targets[0].Owner
	}
	start := time.Now()

	// 每个对象的最终结果,按处理顺序保留。
	items := make([]RecompileItem, len(targets))
	lastRep := make([]*CompileReport, len(targets))
	lastErr := make([]error, len(targets))
	for i, o := range targets {
		items[i] = RecompileItem{Owner: o.Owner, Name: o.Name, Kind: o.Kind, Status: "INVALID"}
	}

	pending := make([]int, 0, len(targets))
	for i := range targets {
		pending = append(pending, i)
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
