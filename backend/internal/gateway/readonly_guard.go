package gateway

// 只读执行守卫(2026-10-29):一条"读"语句经自定义函数改了数据。
//
// 判定层按语句文本判读写:`SELECT f() FROM dual` 是读。可 f 里可能有 INSERT —— MySQL / PostgreSQL
// 在普通事务里照写不误,Oracle 的自治事务函数连只读事务都管不住(实测见 bizquery_hidden.go)。
// 于是只读用户在终端里、任何人在导出里,都能借一个函数把写操作夹带进来。
//
// 守卫在下发前把会话设成数据库层面的只读,执行完恢复原样:
//
//   - MySQL 系:SET SESSION transaction_read_only = 1(老版本叫 tx_read_only)。自动提交的单条语句
//     也受它约束,函数里的写报 1792。TiDB 没有存储函数,跳过。
//   - PostgreSQL 系:SET SESSION default_transaction_read_only = on。
//   - Oracle:普通函数在查询里本来就做不了 DML(ORA-14551);剩下的自治事务函数与 UTL_HTTP 这类
//     库外副作用,下发前查一遍依赖(oracleHiddenEffect)。
//
// 只读用户开不了显式事务(BEGIN / START 不在 select 能力里),所以"先开事务、再让守卫落空"走不通。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log/slog"
	"strings"
	"time"

	"velagateway/internal/apimsg"
	"velagateway/internal/model"
)

type readOnlyGuardKey struct{}

// WithReadOnlyGuard 标记 ctx 下的执行要在数据库层面只读(见文件头)。
func WithReadOnlyGuard(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyGuardKey{}, true)
}

func readOnlyGuarded(ctx context.Context) bool {
	v, _ := ctx.Value(readOnlyGuardKey{}).(bool)
	return v
}

// ReadOnlyStatements 报告一段命令拆开后是否每一条都只是读(select 能力:查询、元数据、计划、
// 会话设置、SQL*Plus 客户端命令)。有一条要写 / DDL / 授权,或调用了有副作用的内置函数,就不是。
func ReadOnlyStatements(engine, sqlText string) bool {
	stmts := SplitForJudge(engine, sqlText)
	if len(stmts) == 0 {
		return false
	}
	d := DialectFor(engine)
	for _, st := range stmts {
		if _, _, ok := SQLPlusSet(engine, st); ok {
			continue
		}
		if _, ok := OracleClientCommand(engine, st); ok {
			continue
		}
		// SET STATEMENT … FOR <语句>:读不读看 <语句>。这里说"是读"才会加上守卫,所以外层不能让它落空。
		if inners := setStatementInners(st); len(inners) > 0 {
			for _, inner := range inners {
				if !ReadOnlyStatements(engine, inner) {
					return false
				}
			}
			continue
		}
		if SessionScoped(st) || PlanOnly(st) || (engineFamily(engine) != familyMongo && MetadataRead(st)) {
			continue
		}
		if d.Capability(d.Verb(st)) != "select" || EffectfulCall(st) != "" {
			return false
		}
	}
	return true
}

// ErrReadOnlyRefused 是守卫在下发前拒绝(Oracle 查依赖发现会改数据)。
type ErrReadOnlyRefused struct{ Ref *model.RuleRef }

func (e *ErrReadOnlyRefused) Error() string { return e.msg().S }

// MsgRef 让界面翻译这句拒绝理由(apimsg)。
func (e *ErrReadOnlyRefused) MsgRef() *model.RuleRef { return e.msg().Ref }

func (e *ErrReadOnlyRefused) msg() apimsg.M {
	return apimsg.Sprintf("只读执行不能运行这条语句:%s", hiddenEffect(e.Ref))
}

// hiddenEffectText:依赖检查结论的说法(不带"数据查询"字样 —— 终端与导出也用它)。
func hiddenEffectText(r *model.RuleRef) string { return hiddenEffect(r).S }

func hiddenEffect(r *model.RuleRef) apimsg.M {
	switch r.Code {
	case model.BizAutonomous:
		return apimsg.Sprintf("用到的 %s 带自治事务(PRAGMA AUTONOMOUS_TRANSACTION),在查询里调用也会真的改数据", r.Args["name"])
	case model.BizFunc:
		return apimsg.Sprintf("经 %s 调用了有库外副作用的包", r.Args["name"])
	case model.BizHiddenCheckFailed:
		return apimsg.Sprintf("无法确认语句用到的函数是否会改数据(%s)", r.Args["err"])
	}
	return apimsg.M{S: model.RenderBizReason(r)}
}

type pinner interface {
	Conn(ctx context.Context) (*sql.Conn, error)
}

// guardReadOnly 在 runner 上为下一条 query 打开只读守卫。返回的 runner 可能换成了一条钉住的连接
// (会话设置要和语句落在同一个物理会话上);restore 在语句执行完之后调用,把会话恢复原样。
func guardReadOnly(ctx context.Context, db pinner, runner sqlRunner, conn *model.Connection, query string) (sqlRunner, func(), error) {
	noop := func() {}
	fam := engineFamily(conn.Engine)
	switch {
	case fam == familyOracle:
		switch ParseVerb(query) {
		case "SELECT", "WITH":
		default:
			return runner, noop, nil // ALTER SESSION、DESC、EXPLAIN PLAN …:不经函数
		}
		ref, err := oracleHiddenEffect(ctx, runner, conn, query)
		if err != nil {
			if ctx.Err() != nil {
				return runner, noop, err
			}
			ref = model.NewRuleRef(model.BizHiddenCheckFailed, "err", err.Error())
		}
		if ref != nil {
			return runner, noop, &ErrReadOnlyRefused{Ref: ref}
		}
		return runner, noop, nil
	case fam == familyMySQL && isTiDBEngine(conn.Engine):
		return runner, noop, nil // 没有存储函数,也没有会话级只读
	case fam != familyMySQL && fam != familyPostgres:
		return runner, noop, nil
	}

	sc, pinned := runner.(*sql.Conn)
	release := noop
	if !pinned {
		c, err := db.Conn(ctx)
		if err != nil {
			return runner, noop, err
		}
		sc, release = c, func() { _ = c.Close() }
	}
	vars := []string{"transaction_read_only", "tx_read_only"}
	if fam == familyPostgres {
		vars = []string{"default_transaction_read_only"}
	}
	for _, name := range vars {
		var orig string
		q := "SELECT @@SESSION." + name
		if fam == familyPostgres {
			q = "SHOW " + name
		}
		if err := sc.QueryRowContext(ctx, q).Scan(&orig); err != nil {
			continue
		}
		on, off := "1", orig
		if fam == familyPostgres {
			on = "on"
		}
		if _, err := sc.ExecContext(ctx, "SET SESSION "+name+" = "+on); err != nil {
			// 引擎标签写成 MySQL 的 TiDB 报 1235(READ ONLY 只有空实现),它也没有存储函数 —— 跳过。
			// 别的设置失败同样照常执行、留日志:守卫是加固,不能让导出和只读用户的查询因此全部失败。
			if !tidbNoopReadOnly(err) {
				slog.Warn("只读守卫:设置只读会话失败,照常执行", "instance", conn.Name, "engine", conn.Engine, "err", err)
			}
			release()
			if ctx.Err() != nil {
				return runner, noop, ctx.Err()
			}
			return runner, noop, nil
		}
		return sc, func() {
			rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := sc.ExecContext(rctx, "SET SESSION "+name+" = "+quoteSetting(fam, off)); err != nil {
				// 恢复不了:这条连接不能带着只读回到池里 / 留在终端标签上,丢掉
				_ = sc.Raw(func(any) error { return driver.ErrBadConn })
			}
			release()
		}, nil
	}
	// 老到没有这两个变量的 MySQL 兼容库(多半也没有存储函数):照常执行,留一条日志
	slog.Warn("只读守卫:目标库不支持会话级只读,照常执行", "instance", conn.Name, "engine", conn.Engine)
	release()
	return runner, noop, nil
}

func quoteSetting(fam, v string) string {
	if fam == familyPostgres {
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	if v == "" {
		return "0"
	}
	return v
}

// readOnlyRejected 认出数据库因只读会话拒绝了写(MySQL 1792、PG 25006)。
func readOnlyRejected(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "1792") || strings.Contains(m, "25006") ||
		strings.Contains(strings.ToLower(m), "read-only transaction") || strings.Contains(m, "READ ONLY transaction")
}

// explainReadOnly 给"被只读会话拒绝"的报错补一句为什么。
func explainReadOnly(err error) error {
	var refused *ErrReadOnlyRefused
	if errors.As(err, &refused) || !readOnlyRejected(err) {
		return err
	}
	return apimsg.Errorf("%w(只读执行:语句经函数等方式改数据,被数据库拒绝)", err)
}
