package osc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	gosqlmysql "github.com/go-sql-driver/mysql"
)

// colMap 把原表的一行对到影子表上(工单 08)。影子表是"原表 + 这次的 ALTER",它的真实结构
// 由 MySQL 执行出来,这里只做对照:
//
//   - 拷贝 / 重放写的列 = 影子表上可写、且能在原表找到来源的列(按列名;CHANGE / RENAME COLUMN
//     按改名映射)。原表有、影子表没有的列(被删了)不写;影子表新加的列由默认值 / 自增 / 生成表达式取值。
//   - 定位行的键 = 原表的一个唯一非空键,且影子表上有一个**同样列集合的唯一索引**。重放按它更新、删除
//     影子表里的行;按它判断"这一行已经在影子表里了"。没有这样的键就无从定位,拒绝。
type colMap struct {
	src, dst *tableMeta
	srcIdx   []int  // 写入列在原表的下标
	dstIdx   []int  // 同一列在影子表的下标
	dkey     []int  // 定位键各列在影子表的下标(与 src.keyIdx 一一对应)
	locate   string // 影子表上用来定位行的唯一索引名
	// own:撞了它就说明"这一行已经在影子表里了"的唯一索引 —— 列集合包含全部定位键列的那些。
	// 两行在这样的索引上相等,定位键也相等,而定位键唯一,所以是同一行。只认 locate 不够:
	// 换主键为 (id, a) 时,同一行先撞的是 PRIMARY,不是按 id 定位的那个唯一键。
	own map[string]bool
}

func (cm *colMap) isOwn(index string) bool { return cm.own[strings.ToLower(index)] }

func newColMap(src, dst *tableMeta, renames map[string]string) (*colMap, []Blocker) {
	var bs []Blocker
	deny := func(code, format string, a ...any) {
		bs = append(bs, Blocker{Code: code, Reason: fmt.Sprintf(format, a...)})
	}
	mapName := func(n string) string {
		for from, to := range renames {
			if strings.EqualFold(from, n) {
				return to
			}
		}
		return n
	}
	cm := &colMap{src: src, dst: dst}
	fed := map[int]bool{} // 有来源的影子表列
	for s, sc := range src.Columns {
		d := dst.colIndex(mapName(sc.Name))
		if d < 0 {
			continue // 这一列被删了
		}
		fed[d] = true
		dc := dst.Columns[d]
		switch {
		case sc.Generated && !dc.Generated:
			// 原表的生成列变成了普通列:它的值得从原表抄过来,而虚拟生成列不在行镜像里,重放给不出
			deny("generated_to_regular", "列 %s 原来是生成列,变更后成了普通列:它的值无法从 binlog 重放。请走原生 DDL。", sc.Name)
		case sc.Generated, dc.Generated:
			// 两边都是生成列,或者普通列变成了生成列:由影子表自己算
		default:
			cm.srcIdx = append(cm.srcIdx, s)
			cm.dstIdx = append(cm.dstIdx, d)
		}
	}
	for d, dc := range dst.Columns {
		if !fed[d] && !dc.Generated && !dc.Nullable && !dc.HasDefault && !dc.AutoInc {
			// 原生 ALTER 会给存量行填类型的隐式默认值(0、'');在线变更是逐行 INSERT,严格模式下
			// 缺值直接报错。与其悄悄替人选一个值,不如让人写明 DEFAULT。
			deny("new_column_no_default", "新加的列 %s 是 NOT NULL 但没有 DEFAULT:请写明默认值。", dc.Name)
		}
	}
	if !cm.pickKey(mapName) {
		deny("no_shared_key", "变更后影子表上没有与原表相同列的唯一键:拷贝与重放无法定位行(删了 / 改了主键时,请保留一个与原来相同的唯一非空键)。")
	}
	return cm, bs
}

// pickKey 挑定位键:原表的主键优先,再到唯一非空键;要求影子表上有同列集合、非前缀的唯一索引。
// 选中后改写 src 的分块键 —— 拷贝的范围也按它切。
func (cm *colMap) pickKey(mapName func(string) string) bool {
	cands := cm.src.uniqueNotNull()
	if cm.src.hasPK() && !cm.src.Indexes[0].Partial {
		cands = append([]index{cm.src.Indexes[0]}, cands...)
	}
	set := func(cols []string) string {
		s := make([]string, len(cols))
		for i, c := range cols {
			s[i] = strings.ToLower(c)
		}
		sort.Strings(s)
		return strings.Join(s, "\x00")
	}
	for _, ix := range cands {
		sidx, didx := make([]int, 0, len(ix.Cols)), make([]int, 0, len(ix.Cols))
		mapped := make([]string, 0, len(ix.Cols))
		ok := true
		for _, c := range ix.Cols {
			s := cm.src.colIndex(c)
			d := cm.dst.colIndex(mapName(c))
			if s < 0 || d < 0 || !chunkKeyTypeOK(cm.src.Columns[s].DataType) {
				ok = false
				break
			}
			sidx, didx = append(sidx, s), append(didx, d)
			mapped = append(mapped, cm.dst.Columns[d].Name)
		}
		if !ok {
			continue
		}
		want := set(mapped)
		for _, dx := range cm.dst.Indexes {
			if dx.Unique && !dx.Partial && set(dx.Cols) == want {
				cm.src.ChunkKey, cm.src.keyIdx = ix.Name, sidx
				cm.dkey, cm.locate = didx, dx.Name
				cm.own = map[string]bool{}
				for _, ox := range cm.dst.Indexes {
					if ox.Unique && !ox.Partial && containsAll(ox.Cols, mapped) {
						cm.own[strings.ToLower(ox.Name)] = true
					}
				}
				return true
			}
		}
	}
	return false
}

func containsAll(cols, want []string) bool {
	for _, w := range want {
		found := false
		for _, c := range cols {
			if strings.EqualFold(c, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// newUniques 是变更新增的唯一约束(影子表上有、原表上没有同列集合的唯一索引),且各列都能在
// 原表找到来源 —— 这些在开始拷贝前要先查重。
func (cm *colMap) newUniques() [][]int {
	srcSets := map[string]bool{}
	for _, ix := range cm.src.Indexes {
		if ix.Unique && !ix.Partial {
			cols := append([]string(nil), ix.Cols...)
			for i := range cols {
				cols[i] = strings.ToLower(cols[i])
			}
			sort.Strings(cols)
			srcSets[strings.Join(cols, "\x00")] = true
		}
	}
	inv := map[int]int{} // 影子表列 → 原表列
	for i, d := range cm.dstIdx {
		inv[d] = cm.srcIdx[i]
	}
	var out [][]int
	for _, dx := range cm.dst.Indexes {
		if !dx.Unique || dx.Partial {
			continue
		}
		var srcCols []int
		names := make([]string, 0, len(dx.Cols))
		ok := true
		for _, c := range dx.Cols {
			s, has := inv[cm.dst.colIndex(c)]
			if !has {
				ok = false // 新列参与的唯一约束:值由默认值 / 自增给出,拷贝时撞了会直接报错
				break
			}
			srcCols = append(srcCols, s)
			names = append(names, strings.ToLower(cm.src.Columns[s].Name))
		}
		if !ok {
			continue
		}
		sort.Strings(names)
		if !srcSets[strings.Join(names, "\x00")] {
			out = append(out, srcCols)
		}
	}
	return out
}

// findDuplicate 在原表上查这组列有没有重复值(NULL 不算冲突)。全表扫描,只读。
func findDuplicate(ctx context.Context, q queryer, table string, src *tableMeta, cols []int) (string, error) {
	names := make([]string, len(cols))
	conds := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(src.Columns[c].Name)
		conds[i] = names[i] + " IS NOT NULL"
	}
	list := strings.Join(names, ", ")
	var sample string
	err := q.QueryRowContext(ctx, fmt.Sprintf("SELECT CAST(CONCAT_WS(', ', %s) AS CHAR) FROM %s WHERE %s GROUP BY %s HAVING COUNT(*) > 1 LIMIT 1",
		list, table, strings.Join(conds, " AND "), list)).Scan(&sample)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sample, err
}

// ---- 重复键错误 ----

// dupKey 认出 MySQL 1062(Duplicate entry),返回撞上的索引名。
func dupKey(err error) (index string, ok bool) {
	var me *gosqlmysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1062 {
		return "", false
	}
	return dupKeyIndex(me.Message), true
}

// dupKeyIndex 从 "Duplicate entry 'x' for key '_t_gho.PRIMARY'" 里取出索引名(8.0.19 起带表名)。
func dupKeyIndex(msg string) string {
	i := strings.LastIndex(msg, "for key '")
	if i < 0 {
		return ""
	}
	name := strings.TrimSuffix(msg[i+len("for key '"):], "'")
	if j := strings.LastIndexByte(name, '.'); j >= 0 {
		name = name[j+1:]
	}
	return name
}

// ErrUniqueConflict:变更新加的唯一约束在迁移期间被违反。不重试 —— 原生 ALTER 遇到同样的数据
// 也会失败,而在线变更若吞掉它就是静默丢行。
type ErrUniqueConflict struct {
	Index  string
	Detail string
}

func (e *ErrUniqueConflict) Error() string {
	return fmt.Sprintf("影子表的唯一键 %s 上出现重复值(%s):这次变更新增的唯一约束与数据冲突,迁移中止,原表未动", e.Index, e.Detail)
}

func uniqueConflict(index string, err error) error {
	var me *gosqlmysql.MySQLError
	detail := err.Error()
	if errors.As(err, &me) {
		detail = me.Message
	}
	return &ErrUniqueConflict{Index: index, Detail: detail}
}
