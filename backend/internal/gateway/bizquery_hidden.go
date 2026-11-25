package gateway

// Oracle 上"看着是查询、实际在改"的最后一种:自定义函数(2026-10-29)。
//
// 普通函数在查询里做 DML 会被 Oracle 拒绝(ORA-14551),只读事务也拦得住;但声明了
// PRAGMA AUTONOMOUS_TRANSACTION 的函数在**自己的事务**里执行,SET TRANSACTION READ ONLY 管不到它 ——
// SELECT f() FROM dual 真的往表里写了行,查一个调用了 f 的视图也一样(本地 Oracle 23ai 实测)。
// 函数里调 UTL_HTTP / UTL_FILE 这类包则是库外的副作用(发请求、写服务器文件),事务更管不到。
// MySQL / PostgreSQL 没有这个问题:函数里的写操作同样被只读事务拒绝(MySQL 1792 实测)。
//
// 判定层只看得见语句文本,看不见函数体。所以执行前在同一个会话上查数据字典:从语句里出现的名字
// 出发,沿 ALL_DEPENDENCIES 走完它们依赖的全部对象(视图 → 函数 → 包 → …),源码里有
// AUTONOMOUS_TRANSACTION、或依赖了上述包的,拒绝执行。Oracle 自带的 schema 不往下走:那是 Oracle
// 自己的实现,业务方改不了,也正是 SYS.STANDARD 这种人人都依赖的东西。
//
// 已知看不见的:加密(wrap)过的源码。查不成(字典报错)就不执行 —— 确认不了就不放。

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"aegisdb/internal/model"
)

// BizRefused 是执行层在下发前拒绝(要查过数据字典才知道的那种)。服务层按拒绝处理,不算执行失败。
type BizRefused struct{ Ref *model.RuleRef }

func (e *BizRefused) Error() string { return model.RenderBizReason(e.Ref) }

// oracleSuppliedOwners:Oracle 自带的 schema,依赖链走到这里为止(当前 schema 除外)。
var oracleSuppliedOwners = []string{
	"SYS", "SYSTEM", "AUDSYS", "CTXSYS", "MDSYS", "XDB", "OUTLN", "DBSNMP", "ORDSYS", "ORDDATA", "ORDPLUGINS",
	"OLAPSYS", "WMSYS", "LBACSYS", "DVSYS", "DVF", "GSMADMIN_INTERNAL", "OJVMSYS", "APPQOSSYS", "DBSFWUSER",
	"REMOTE_SCHEDULER_AGENT", "EXFSYS", "ORACLE_OCM", "SI_INFORMTN_SCHEMA", "MDDATA", "FLOWS_FILES", "DIP",
}

// oracleEffectPackages:依赖了就有库外副作用的包。DBMS_SQL / DBMS_LOCK 不在里面:前者在查询里
// 做不了 DML(同样 ORA-14551),后者常见于工具函数(DBMS_LOCK.SLEEP),都不是"改数据"。
var oracleEffectPackages = []string{
	"UTL_HTTP", "UTL_FILE", "UTL_TCP", "UTL_SMTP", "UTL_MAIL", "UTL_INADDR", "DBMS_PIPE", "DBMS_LDAP", "HTTPURITYPE",
}

const maxHiddenNames = 900 // IN 列表上限是 1000

// oracleStmtNames:语句里出现的全部标识符(数据字典写法),以及其中当前缀用的(可能是 schema)。
func oracleStmtNames(stmt string) (names, owners []string) {
	ids := blankStringLiterals(StripComments(stmt))
	seenN, seenO := map[string]bool{}, map[string]bool{}
	for _, loc := range bizIdentRe.FindAllStringIndex(ids, -1) {
		n := oracleDictName(ids[loc[0]:loc[1]])
		if n == "" {
			continue
		}
		if !seenN[n] {
			seenN[n] = true
			names = append(names, n)
		}
		if strings.HasPrefix(strings.TrimLeft(ids[loc[1]:], " \t\r\n"), ".") && !seenO[n] {
			seenO[n] = true
			owners = append(owners, n)
		}
	}
	sort.Strings(names)
	sort.Strings(owners)
	return names, owners
}

func sqlStrList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "'" + quoteLit(x) + "'"
	}
	return strings.Join(q, ",")
}

// oracleHiddenEffectSQL:一条查询答完。结果至多两行:A = 带自治事务的对象,P = 依赖了库外副作用包的对象。
func oracleHiddenEffectSQL(names, owners []string) string {
	cur := `SYS_CONTEXT('USERENV','CURRENT_SCHEMA')`
	ownerList := cur + ",'PUBLIC'"
	if len(owners) > 0 {
		ownerList += "," + sqlStrList(owners)
	}
	nameList, supplied := sqlStrList(names), sqlStrList(oracleSuppliedOwners)
	notSupplied := func(col string) string { return "(" + col + " NOT IN (" + supplied + ") OR " + col + " = " + cur + ")" }
	return `WITH dep AS (` +
		` SELECT d.owner, d.name, d.referenced_owner r_owner, d.referenced_name r_name FROM all_dependencies d` +
		` START WITH d.name IN (` + nameList + `) AND d.owner IN (` + ownerList + `)` +
		` CONNECT BY NOCYCLE PRIOR d.referenced_owner = d.owner AND PRIOR d.referenced_name = d.name AND LEVEL <= 30` +
		` AND ` + notSupplied("d.owner") +
		`), objs AS (` +
		` SELECT o.owner, o.object_name name FROM all_objects o WHERE o.object_name IN (` + nameList + `) AND o.owner IN (` + ownerList + `)` +
		` UNION SELECT r_owner, r_name FROM dep` +
		`)` +
		` SELECT * FROM (SELECT 'A' k, s.owner || '.' || s.name a, CAST(NULL AS VARCHAR2(128)) b FROM all_source s` +
		` JOIN objs x ON s.owner = x.owner AND s.name = x.name` +
		` WHERE ` + notSupplied("x.owner") + ` AND UPPER(s.text) LIKE '%AUTONOMOUS_TRANSACTION%') WHERE ROWNUM = 1` +
		` UNION ALL` +
		` SELECT * FROM (SELECT 'P' k, d.owner || '.' || d.name a, d.r_name b FROM dep d` +
		` WHERE d.r_owner IN ('SYS','PUBLIC') AND d.r_name IN (` + sqlStrList(oracleEffectPackages) + `)) WHERE ROWNUM = 1`
}

type rowsQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// 查过没问题的语句记一分钟:同一个人反复跑同一条查询,不必每次都走一遍依赖链。有问题的不记 ——
// 每次都查,改掉了函数立刻就能用。
var hiddenClean = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

const hiddenCleanTTL = time.Minute

// oracleHiddenEffect 在 q(已切到目标 schema 的会话 / 事务)上查语句会不会经自定义函数改数据。
// 返回非 nil 的拒绝原因表示不能执行;error 表示没查成(调用方按不能执行处理)。
func oracleHiddenEffect(ctx context.Context, q rowsQueryer, conn *model.Connection, stmt string) (*model.RuleRef, error) {
	names, owners := oracleStmtNames(stmt)
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > maxHiddenNames {
		return model.NewRuleRef(model.BizHiddenCheckFailed, "err", fmt.Sprintf("语句里的名字超过 %d 个", maxHiddenNames)), nil
	}
	key := fmt.Sprintf("%d|%s|%d|%s|%s|%s|%s", conn.ID, conn.Host, conn.Port, conn.Username, conn.TargetSchema,
		strings.Join(names, ","), strings.Join(owners, ","))
	hiddenClean.Lock()
	at, ok := hiddenClean.m[key]
	hiddenClean.Unlock()
	if ok && time.Since(at) < hiddenCleanTTL {
		return nil, nil
	}

	rows, err := q.QueryContext(ctx, oracleHiddenEffectSQL(names, owners))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, a string
		var b sql.NullString
		if err := rows.Scan(&k, &a, &b); err != nil {
			return nil, err
		}
		if k == "A" {
			return model.NewRuleRef(model.BizAutonomous, "name", a), nil
		}
		return model.NewRuleRef(model.BizFunc, "name", a+" → "+b.String), nil
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hiddenClean.Lock()
	if len(hiddenClean.m) > 2000 {
		hiddenClean.m = map[string]time.Time{}
	}
	hiddenClean.m[key] = time.Now()
	hiddenClean.Unlock()
	return nil, nil
}
