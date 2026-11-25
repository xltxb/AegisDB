// Package osc implements gh-ost-style online schema change for MySQL.
//
// 为什么要它:MySQL 5.6+ 加二级索引本来就是 INPLACE、不阻塞读写。gh-ost 这套的价值
// 不在"在线",而在另外三件原生 DDL 给不了的事:
//
//   - **可限流、可暂停、可中止**。原生 ALTER 一旦下去就只能等,几小时里没有任何旋钮。
//   - **不拖垮从库**。原生 DDL 在从库上是一条语句串行重放,主库跑 3 小时,从库就延迟
//     3 小时;影子表方案的 DML 是普通写入,从库跟得上。
//   - **不引发 MDL 排队雪崩**。原生 ALTER 结束时要拿元数据锁,一个长事务就能让它卡住,
//     而后面所有访问该表的请求会排在它身后 —— 表面上是"加个索引",实际是全站不可用。
//
// 代价是这套东西自己会动数据。所以本包的第一道关不是拷贝,是**拒绝**:凡是有一丝
// 不确定的场景一律不做,让人去用原生 DDL。宁可拒绝一个本可以做的,不可放过一个做不
// 干净的 —— 前者是麻烦,后者是数据不一致。
package osc

import (
	"fmt"
	"strings"
)

// Facts is everything preflight needs to decide, gathered from the target
// instance in one place (see gather.go) so the decision itself stays pure and
// exhaustively testable without a live MySQL.
type Facts struct {
	Engine        string // 连接的引擎,必须是 mysql
	Version       string // e.g. "8.0.36"
	VersionMajor  int
	VersionMinor  int
	VersionPatch  int
	LogBin        bool   // log_bin
	BinlogFormat  string // ROW | STATEMENT | MIXED
	BinlogRowImg  string // FULL | MINIMAL | NOBLOB
	HasReplSlave  bool   // REPLICATION SLAVE 权限
	HasReplClient bool   // REPLICATION CLIENT 权限

	Schema string
	Table  string

	TableExists   bool
	TableEngine   string   // InnoDB | MyISAM | ...
	HasPK         bool     // 有主键
	UniqueNotNull []string // 可代替主键做分块的唯一非空键
	// 实际选中的分块键(PRIMARY 或某个唯一非空索引)。有主键/唯一键却选不出来,是因为
	// 键列类型不适合做范围切分(见 chunkKeyTypeOK)。
	ChunkKey        string
	Columns         []string
	Indexes         []string // 含 PRIMARY
	UnsupportedCols []string // "col (type)":重放时无法保证原样写回的列类型
	ForeignKeysOut  int      // 本表指向别人的外键
	ForeignKeysIn   int      // 别人指向本表的外键
	Triggers        int      // 本表上的触发器
	GeneratedCols   int      // 生成列/虚拟列

	EstimatedRows int64 // information_schema 的估算值,不精确
	DataBytes     int64 // 数据 + 索引字节数
	FreeDiskBytes int64 // 目标数据目录所在盘的剩余空间;0 = 拿不到

	LeftoverGhost bool // 上一次没清干净的 _gho 表
	LeftoverDel   bool // 上一次没清干净的 _del 表

	// 最长的活跃事务已经跑了多久(秒)。它不阻止开始,但会让 cut-over 那一下拿不到锁。
	LongestTrxSeconds int
}

// Blocker is one reason this migration must not run. Reason 面向的是人,不是日志:
// 每一条都要说清"接下来该做什么",因为它们要人做的事完全不同 —— 换个方式、改表结构、
// 改实例参数、还是先清残留。
type Blocker struct {
	Code   string
	Reason string
}

// Warning does not stop the migration but changes what the operator should
// expect. 它和 Blocker 分开,是因为把"会很慢"和"会丢数据"混在一列里,人就会两个都不看。
type Warning struct {
	Code   string
	Reason string
}

// 变更的种类。最早只做加 / 删索引;工单 08 起是任意 ALTER 子句(ActionAlter):类型转换、
// 默认值回填这些语义问题不靠我们去推,而是让 MySQL 在影子表上真执行,再按变更后的真实结构
// 对列(colMap);拷贝 / 重放里任何"被吞掉的"冲突与截断都让迁移失败,而不是静默改数据。
const (
	ActionAlter     = "alter" // 工单 08:任意 ALTER 子句
	ActionAddIndex  = "add_index"
	ActionDropIndex = "drop_index"
)

// Preflight decides whether a gh-ost-style migration may run at all.
//
// 返回的是**全部**问题而不是第一个:一次告诉人三件要修的事,比让他修一件、重试、
// 再发现第二件好 —— 每一次重试在生产上都是一个变更窗口。
func Preflight(f Facts, c Change) ([]Blocker, []Warning) {
	var bs []Blocker
	var ws []Warning
	deny := func(code, format string, a ...any) {
		bs = append(bs, Blocker{Code: code, Reason: fmt.Sprintf(format, a...)})
	}
	warn := func(code, format string, a ...any) {
		ws = append(ws, Warning{Code: code, Reason: fmt.Sprintf(format, a...)})
	}

	// ---- 适用范围 ----
	if f.Engine != "mysql" {
		deny("not_mysql", "在线变更只支持 MySQL,当前实例是 %s。", f.Engine)
		return bs, ws // 引擎不对,后面每一条检查都没有意义
	}
	// cut-over 靠 8.0.13 起才允许的"LOCK TABLES 之下 RENAME TABLE":同一个会话持锁、
	// 追平、改名,锁连接一断什么都没发生。更早的版本只能走 gh-ost 那套哨兵表的五步
	// 舞,多出来的每一步都是一个可能卡在半路的地方 —— 5.7 已经 EOL,不值得。
	if f.VersionMajor < 8 || (f.VersionMajor == 8 && f.VersionMinor == 0 && f.VersionPatch < 13) {
		deny("version_too_old", "MySQL %s 太旧:在线变更的原子切换需要 8.0.13 及以上。", f.Version)
	}

	// ---- 目标表 ----
	if !f.TableExists {
		deny("no_table", "表 %s.%s 不存在。", f.Schema, f.Table)
		return bs, ws // 表都没有,后面的表级检查全是噪声
	}
	checkChange(f, c, deny)
	if len(f.Table) > maxTableNameForShadow {
		deny("table_name_too_long", "表名超过 %d 个字符:影子表名 _%s_gho 会超出 MySQL 的 64 字符上限。", maxTableNameForShadow, f.Table)
	}
	// 拷贝靠 FOR SHARE 锁住读到的行,才能保证"读到旧值、写进影子表"与"删掉这一行"
	// 不会交错出一行幽灵数据;非事务引擎没有行锁,这个保证不成立。
	if f.TableEngine != "" && !strings.EqualFold(f.TableEngine, "InnoDB") {
		deny("not_innodb", "表引擎是 %s:在线变更依赖 InnoDB 的行锁保证拷贝与删除不交错。", f.TableEngine)
	}
	// 分块拷贝靠一个稳定、唯一、非空的键来切范围并保证可重入。没有它,拷贝既
	// 无法分块,也无法在中断后知道自己走到哪 —— 这是 gh-ost 的硬前提,不是偏好。
	if !f.HasPK && len(f.UniqueNotNull) == 0 {
		deny("no_unique_key", "表没有主键,也没有唯一非空索引:分块拷贝无法切分,也无法在中断后续跑。请先加主键。")
	} else if f.ChunkKey == "" {
		// 浮点、ENUM、JSON 之类做键时,"按键排序"与"按键比大小"不是同一个顺序
		// (ENUM 排序按下标、比较按字面),范围切分会漏行。
		deny("chunk_key_type", "主键 / 唯一非空索引的列类型不适合做范围切分(只支持整数、定点数、字符串、二进制与日期时间)。")
	}
	if len(f.UnsupportedCols) > 0 {
		deny("column_types", "表中有重放时无法保证原样写回的列类型:%s。请走原生 DDL。", strings.Join(f.UnsupportedCols, ", "))
	}
	// 外键指向影子表时会跟着 rename 走,指向原表的约束在 cut-over 后指向被弃置的
	// 旧表 —— 两个方向都会把约束悄悄挪到错误的对象上。gh-ost 同样直接拒绝。
	if f.ForeignKeysOut > 0 || f.ForeignKeysIn > 0 {
		deny("foreign_keys", "表涉及外键(出 %d 条、入 %d 条):cut-over 改名会把约束挪到错误的表上。请走原生 DDL。",
			f.ForeignKeysOut, f.ForeignKeysIn)
	}
	// 触发器会在影子表上再触发一次,同一个动作被执行两遍。
	if f.Triggers > 0 {
		deny("triggers", "表上有 %d 个触发器:拷贝到影子表时会被再触发一次,同一动作执行两遍。请走原生 DDL。", f.Triggers)
	}

	// ---- 实例参数 ----
	// 这三条是同一件事的三个面:没有 ROW 格式的完整行镜像,就无法把一条 UPDATE 准确
	// 地重放到影子表上 —— 只能拿到"改了哪些列",拿不到"这一行现在是什么"。
	if !f.LogBin {
		deny("binlog_off", "实例未开启 binlog(log_bin=OFF):在线变更靠它追平变更期间的写入。")
	}
	if f.BinlogFormat != "" && f.BinlogFormat != "ROW" {
		deny("binlog_format", "binlog_format=%s,必须是 ROW:其它格式记的是语句而不是行,无法准确重放到影子表。", f.BinlogFormat)
	}
	if f.BinlogRowImg != "" && f.BinlogRowImg != "FULL" {
		deny("binlog_row_image", "binlog_row_image=%s,必须是 FULL:非完整行镜像下 UPDATE 只带部分列,重放会写出错误的行。", f.BinlogRowImg)
	}
	if !f.HasReplSlave {
		deny("no_repl_slave", "网关账号缺少 REPLICATION SLAVE 权限,无法读取 binlog。")
	}
	if !f.HasReplClient {
		deny("no_repl_client", "网关账号缺少 REPLICATION CLIENT 权限,无法读取 binlog 位点与主从状态。")
	}

	// ---- 残留 ----
	// 上一次跑挂了留下的表。直接复用会把两次迁移的数据混在一起,而直接删掉又可能
	// 删掉别人正在看的东西 —— 所以停下来让人确认,这是少数几个"人来决定"更好的地方。
	if f.LeftoverGhost {
		deny("leftover_ghost", "存在上次未清理的影子表 _%s_gho:请先确认它可以删除,再重新发起。", f.Table)
	}
	if f.LeftoverDel {
		deny("leftover_del", "存在上次未清理的旧表 _%s_del:它是上一次 cut-over 换下来的原表,确认无用后再重新发起。", f.Table)
	}

	// ---- 容量 ----
	// 影子表是原表的一份完整副本,期间磁盘上同时存在两份。留 20% 余量是因为拷贝
	// 期间还有正常写入进来,而磁盘写满的后果是整个实例不可用,不只是这次变更失败。
	if f.FreeDiskBytes > 0 && f.DataBytes > 0 {
		need := f.DataBytes + f.DataBytes/5
		if f.FreeDiskBytes < need {
			deny("disk_space", "磁盘余量不足:表占 %s,影子表期间需要约 %s,当前可用 %s。",
				humanBytes(f.DataBytes), humanBytes(need), humanBytes(f.FreeDiskBytes))
		}
	}

	// ---- 提示(不拦) ----
	if f.GeneratedCols > 0 {
		warn("generated_columns", "表有 %d 个生成列:拷贝时会由数据库自行重算,值应当一致,但请在校验阶段确认。", f.GeneratedCols)
	}
	if f.LongestTrxSeconds >= 60 {
		warn("long_transaction", "当前有已运行 %d 秒的事务:cut-over 需要短暂持锁,长事务会让它反复重试甚至超时。",
			f.LongestTrxSeconds)
	}
	if f.EstimatedRows > 100_000_000 {
		warn("very_large_table", "表估算 %d 行:拷贝会持续很久,请确认限流阈值与执行窗口。", f.EstimatedRows)
	}
	return bs, ws
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
