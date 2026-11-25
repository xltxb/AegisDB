package osc

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"

	"velagateway/internal/apimsg"
)

// applier replays binlog row changes onto the shadow table.
//
// 这里的写法是整件事的正确性所在(ADR 0011):binlog 给的永远是"这一行现在的样子",重放就让
// 影子表里按定位键找到的那一行变成这个样子 —— 有就改、没有就插;DELETE 就按键删。拷贝即便随后
// 碰到这一行,也会因为影子表里已有这个键而跳过(见 copier),或者因为删除已提交而读不到。
//
// 工单 08 之前这里用 REPLACE INTO。变更只是加普通索引时它等价,但变更新增了唯一约束时,REPLACE
// 会把**另一行**因为撞了新唯一键而删掉 —— 静默丢数据。现在只把"撞了自己的定位键"当作"这一行
// 已经在了",撞了别的唯一键就让迁移失败(ErrUniqueConflict):原生 ALTER 遇到这样的数据同样会失败。
type applier struct {
	cm     *colMap
	ghost  string
	insert string // INSERT INTO ghost (...) VALUES (...)
	update string // UPDATE ghost SET ... WHERE <定位键>
	delete string // DELETE FROM ghost WHERE <定位键>
}

func newApplier(cm *colMap, ghost string) *applier {
	a := &applier{cm: cm, ghost: ghost}
	cols := make([]string, len(cm.dstIdx))
	vals := make([]string, len(cm.dstIdx))
	sets := make([]string, len(cm.dstIdx))
	for i, d := range cm.dstIdx {
		cols[i] = quoteIdent(cm.dst.Columns[d].Name)
		vals[i] = valueExpr(cm.src.Columns[cm.srcIdx[i]]) // 值按原表列的字符集贴标签,写入时由 MySQL 转换
		sets[i] = cols[i] + " = " + vals[i]
	}
	conds := make([]string, len(cm.dkey))
	for i, d := range cm.dkey {
		conds[i] = quoteIdent(cm.dst.Columns[d].Name) + " = " + ghostKeyExpr(cm.src.Columns[cm.src.keyIdx[i]], cm.dst.Columns[d])
	}
	where := strings.Join(conds, " AND ")
	a.insert = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoteIdent(ghost), strings.Join(cols, ", "), strings.Join(vals, ", "))
	a.update = fmt.Sprintf("UPDATE %s SET %s WHERE %s", quoteIdent(ghost), strings.Join(sets, ", "), where)
	a.delete = fmt.Sprintf("DELETE FROM %s WHERE %s", quoteIdent(ghost), where)
	return a
}

// apply writes one row change. ex 可以是事务,也可以是 cut-over 那条持着表锁的连接 ——
// 后者不能开事务(START TRANSACTION 会隐式释放 LOCK TABLES)。
func (a *applier) apply(ctx context.Context, ex execer, rc RowChange) error {
	n := len(a.cm.src.Columns)
	for _, img := range [][]any{rc.Before, rc.After} {
		if img != nil && len(img) != n {
			// 列数对不上说明表结构已经不是迁移开始时的样子,按下标写就是写错列。
			return apimsg.Errorf("行镜像有 %d 列,表有 %d 列:表结构在迁移期间被改动", len(img), n)
		}
	}
	switch rc.Kind {
	case RowInsert:
		return a.put(ctx, ex, rc.After)
	case RowDelete:
		return a.del(ctx, ex, rc.Before)
	case RowUpdate:
		if !a.sameKey(rc.Before, rc.After) {
			// 键变了:旧键那一行要先删,否则会留在影子表里,变成原表里已经不存在的数据
			if err := a.del(ctx, ex, rc.Before); err != nil {
				return err
			}
			return a.put(ctx, ex, rc.After)
		}
		res, err := ex.ExecContext(ctx, a.update, append(a.values(rc.After), a.keyArgs(rc.After)...)...)
		if err != nil {
			return a.conflict(err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		// 没改到:要么这一行还没被拷贝过来,要么值本来就一样 —— 插一次,撞了自己的键就是后者
		return a.put(ctx, ex, rc.After)
	}
	return apimsg.Errorf("未知的行变更类型 %d", rc.Kind)
}

// put 让影子表里按定位键的那一行变成 row:先插,撞了自己的定位键就改。
func (a *applier) put(ctx context.Context, ex execer, row []any) error {
	_, err := ex.ExecContext(ctx, a.insert, a.values(row)...)
	if err == nil {
		return nil
	}
	if idx, dup := dupKey(err); !dup || !a.cm.isOwn(idx) {
		return a.conflict(err)
	}
	if _, err := ex.ExecContext(ctx, a.update, append(a.values(row), a.keyArgs(row)...)...); err != nil {
		return a.conflict(err)
	}
	return nil
}

// conflict 把"撞了别的唯一键"翻成 ErrUniqueConflict,其它错误原样返回。
func (a *applier) conflict(err error) error {
	if idx, dup := dupKey(err); dup {
		return uniqueConflict(idx, err)
	}
	return err
}

func (a *applier) del(ctx context.Context, ex execer, row []any) error {
	_, err := ex.ExecContext(ctx, a.delete, a.keyArgs(row)...)
	return err
}

func (a *applier) values(row []any) []any {
	args := make([]any, len(a.cm.srcIdx))
	for i, s := range a.cm.srcIdx {
		args[i] = normalize(a.cm.src.Columns[s], row[s])
	}
	return args
}

func (a *applier) keyArgs(row []any) []any {
	m := a.cm.src
	args := make([]any, len(m.keyIdx))
	for i, j := range m.keyIdx {
		args[i] = normalize(m.Columns[j], row[j])
	}
	return args
}

func (a *applier) sameKey(before, after []any) bool {
	m := a.cm.src
	for _, j := range m.keyIdx {
		x, y := normalize(m.Columns[j], before[j]), normalize(m.Columns[j], after[j])
		if bx, ok := x.([]byte); ok {
			if by, ok := y.([]byte); !ok || !bytes.Equal(bx, by) {
				return false
			}
			continue
		}
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

// ghostKeyExpr is the placeholder for matching a source key value against the
// shadow table's column d. 值是原表列 s 的原始字节 / 数值;比较按影子表列自己的类型与排序
// 规则(才走得上索引)。字符集变了的(CONVERT TO CHARACTER SET)先按原字符集贴标签再转过去。
func ghostKeyExpr(s, d column) string {
	switch {
	case isText(d.DataType):
		src := "?"
		if isText(s.DataType) {
			src = "CONVERT(? USING " + s.Charset + ")"
		}
		if isText(s.DataType) && strings.EqualFold(s.Charset, d.Charset) {
			return src + " COLLATE " + d.Collation
		}
		return fmt.Sprintf("CONVERT(%s USING %s) COLLATE %s", src, d.Charset, d.Collation)
	}
	return keyExpr(d)
}

// normalize turns a decoded binlog value into a driver argument.
//
//   - 无符号整数:binlog 里只有位模式,解码库按有符号返回,4000000000 会变成
//     -294967296。按列宽补回去。MEDIUMINT 是 24 位,解码成 int32 时已做了符号扩展。
//   - 字符串:统一成 []byte,保证是列字符集的原始字节,由 CONVERT(? USING cs) 贴标签。
func normalize(c column, v any) any {
	if v == nil {
		return nil
	}
	if c.Unsigned && isInteger(c.DataType) {
		var x int64
		switch n := v.(type) {
		case int8:
			x = int64(n)
		case int16:
			x = int64(n)
		case int32:
			x = int64(n)
		case int64:
			x = n
		case int:
			x = int64(n)
		default:
			return v
		}
		if x >= 0 {
			return uint64(x)
		}
		bits := map[string]uint{"tinyint": 8, "smallint": 16, "mediumint": 24, "int": 32, "integer": 32}[c.DataType]
		if bits == 0 { // bigint
			return uint64(x)
		}
		return uint64(x + 1<<bits)
	}
	if s, ok := v.(string); ok {
		return []byte(s)
	}
	return v
}
