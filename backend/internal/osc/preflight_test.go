package osc

import (
	"strings"
	"testing"
)

// 一个各项条件都齐备的场景。每条用例只从它身上改一处,这样"是哪一处导致了拒绝"
// 不用猜。
func okFacts() Facts {
	return Facts{
		Engine: "mysql", Version: "8.0.36", VersionMajor: 8, VersionMinor: 0, VersionPatch: 36,
		LogBin: true, BinlogFormat: "ROW", BinlogRowImg: "FULL",
		HasReplSlave: true, HasReplClient: true,
		Schema: "app", Table: "t_order",
		TableExists: true, TableEngine: "InnoDB", HasPK: true, ChunkKey: "PRIMARY",
		Columns:       []string{"id", "user_id", "created_at"},
		Indexes:       []string{"PRIMARY", "idx_user"},
		EstimatedRows: 5_000_000, DataBytes: 2 << 30, FreeDiskBytes: 100 << 30,
	}
}

// 在 created_at 上加一个普通索引 —— 用户最常做的那一种。
var addIdx = Change{Action: ActionAddIndex, Index: "idx_created", Columns: []IndexColumn{{Name: "created_at"}}}

func codes(bs []Blocker) map[string]bool {
	m := map[string]bool{}
	for _, b := range bs {
		m[b.Code] = true
	}
	return m
}

func TestPreflight_AllGreenPasses(t *testing.T) {
	bs, _ := Preflight(okFacts(), addIdx)
	if len(bs) != 0 {
		t.Fatalf("条件齐备时不该有阻塞项,实际: %+v", bs)
	}
}

// 每一条阻塞项各自独立成立 —— 一次改一处。
func TestPreflight_EachBlockerStandsAlone(t *testing.T) {
	cases := []struct {
		name string
		code string
		mut  func(*Facts)
	}{
		{"非 MySQL", "not_mysql", func(f *Facts) { f.Engine = "postgres" }},
		{"版本太老", "version_too_old", func(f *Facts) { f.VersionMajor, f.VersionMinor = 5, 7 }},
		{"8.0 早于 .13", "version_too_old", func(f *Facts) { f.VersionPatch = 12 }},
		{"非 InnoDB", "not_innodb", func(f *Facts) { f.TableEngine = "MyISAM" }},
		{"键列类型不适合分块", "chunk_key_type", func(f *Facts) { f.ChunkKey = "" }},
		{"有无法重放的列", "column_types", func(f *Facts) { f.UnsupportedCols = []string{"loc (geometry)"} }},
		{"表名太长", "table_name_too_long", func(f *Facts) { f.Table = strings.Repeat("t", 60) }},
		{"表不存在", "no_table", func(f *Facts) { f.TableExists = false }},
		{"没有唯一键", "no_unique_key", func(f *Facts) { f.HasPK = false }},
		{"有出向外键", "foreign_keys", func(f *Facts) { f.ForeignKeysOut = 1 }},
		{"有入向外键", "foreign_keys", func(f *Facts) { f.ForeignKeysIn = 1 }},
		{"有触发器", "triggers", func(f *Facts) { f.Triggers = 2 }},
		{"binlog 关着", "binlog_off", func(f *Facts) { f.LogBin = false }},
		{"binlog 非 ROW", "binlog_format", func(f *Facts) { f.BinlogFormat = "STATEMENT" }},
		{"行镜像非 FULL", "binlog_row_image", func(f *Facts) { f.BinlogRowImg = "MINIMAL" }},
		{"缺 REPLICATION SLAVE", "no_repl_slave", func(f *Facts) { f.HasReplSlave = false }},
		{"缺 REPLICATION CLIENT", "no_repl_client", func(f *Facts) { f.HasReplClient = false }},
		{"影子表残留", "leftover_ghost", func(f *Facts) { f.LeftoverGhost = true }},
		{"旧表残留", "leftover_del", func(f *Facts) { f.LeftoverDel = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := okFacts()
			c.mut(&f)
			bs, _ := Preflight(f, addIdx)
			if !codes(bs)[c.code] {
				t.Errorf("应当因 %s 被拒,实际阻塞项: %+v", c.code, bs)
			}
		})
	}
}

// 唯一非空索引可以代替主键 —— 分块拷贝要的是"稳定、唯一、非空",主键只是最常见
// 的那一种。把它一并拒掉会挡住一批本可以做的表。
func TestPreflight_AUniqueNotNullKeyCanStandInForThePrimaryKey(t *testing.T) {
	f := okFacts()
	f.HasPK = false
	f.UniqueNotNull = []string{"uk_order_no"}
	f.ChunkKey = "uk_order_no"
	bs, _ := Preflight(f, addIdx)
	if codes(bs)["no_unique_key"] {
		t.Error("有唯一非空索引就够了,不该因为缺主键被拒")
	}
}

// 只做索引。其它变更必须明确被挡住 —— 悄悄放过一个 MODIFY COLUMN,拷贝时的类型
// 转换语义没人验证过。
func TestPreflight_OnlyIndexChangesAreInScope(t *testing.T) {
	drop := Change{Action: ActionDropIndex, Index: "idx_user"}
	for _, c := range []Change{addIdx, drop} {
		if bs, _ := Preflight(okFacts(), c); len(bs) != 0 {
			t.Errorf("%s 应当在范围内,实际阻塞项: %+v", c.Action, bs)
		}
	}
	if bs, _ := Preflight(okFacts(), Change{Action: "modify_column", Index: "x"}); !codes(bs)["unsupported_action"] {
		t.Error("索引以外的变更必须被挡住")
	}
}

// 每一种"变更本身不成立"的情况各自被拒。
func TestPreflight_TheChangeItselfMustMakeSense(t *testing.T) {
	cases := []struct {
		name string
		code string
		c    Change
	}{
		{"没给索引名", "no_index_name", Change{Action: ActionAddIndex, Columns: addIdx.Columns}},
		{"索引名已存在", "index_exists", Change{Action: ActionAddIndex, Index: "idx_user", Columns: addIdx.Columns}},
		{"没给列", "no_columns", Change{Action: ActionAddIndex, Index: "idx_x"}},
		{"列不存在", "no_such_column", Change{Action: ActionAddIndex, Index: "idx_x", Columns: []IndexColumn{{Name: "nope"}}}},
		{"删不存在的索引", "no_such_index", Change{Action: ActionDropIndex, Index: "idx_nope"}},
		{"删主键", "drop_primary", Change{Action: ActionDropIndex, Index: "PRIMARY"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if bs, _ := Preflight(okFacts(), c.c); !codes(bs)[c.code] {
				t.Errorf("应当因 %s 被拒,实际: %+v", c.code, bs)
			}
		})
	}
}

// 加唯一索引看上去只多一个关键字,实际两条写入路径都会静默丢行:INSERT IGNORE 丢掉
// 重复的那一行,REPLACE INTO 删掉与之冲突的另一行。没有任何报错 —— 必须拒绝。
func TestPreflight_AUniqueIndexIsRefusedBecauseItSilentlyDropsRows(t *testing.T) {
	c := addIdx
	c.Unique = true
	if bs, _ := Preflight(okFacts(), c); !codes(bs)["unique_index"] {
		t.Error("在线加唯一索引必须被拒")
	}
}

// 没有主键、靠某个唯一非空索引分块的表,那个索引不能被删 —— 删了就没有东西能定位行。
func TestPreflight_TheChunkKeyCannotBeDropped(t *testing.T) {
	f := okFacts()
	f.HasPK = false
	f.UniqueNotNull = []string{"uk_order_no"}
	f.ChunkKey = "uk_order_no"
	f.Indexes = []string{"uk_order_no", "idx_user"}
	if bs, _ := Preflight(f, Change{Action: ActionDropIndex, Index: "uk_order_no"}); !codes(bs)["drop_chunk_key"] {
		t.Error("分块键不能被删")
	}
}

// 磁盘要按"原表 + 20%"算:影子表是完整副本,拷贝期间两份同时在盘上,而且还有正常
// 写入继续进来。磁盘写满会让整个实例不可用,不只是这次变更失败。
func TestPreflight_DiskMustHoldASecondCopyPlusHeadroom(t *testing.T) {
	f := okFacts()
	f.DataBytes = 100 << 30
	f.FreeDiskBytes = 110 << 30 // 够放一份副本,但不够 20% 余量
	bs, _ := Preflight(f, addIdx)
	if !codes(bs)["disk_space"] {
		t.Error("余量不足 20% 时应当拒绝 —— 拷贝期间还有写入进来")
	}

	f.FreeDiskBytes = 130 << 30
	bs, _ = Preflight(f, addIdx)
	if codes(bs)["disk_space"] {
		t.Error("余量充足时不该拒绝")
	}
}

// 拿不到磁盘余量时不能假装它是 0 —— 那会把每一次变更都拒掉。
func TestPreflight_UnknownDiskSpaceDoesNotBlock(t *testing.T) {
	f := okFacts()
	f.FreeDiskBytes = 0 // 0 = 没拿到,不是"没有空间"
	bs, _ := Preflight(f, addIdx)
	if codes(bs)["disk_space"] {
		t.Error("拿不到磁盘余量时不该拦 —— 把未知当成 0 会拒掉全部变更")
	}
}

// 一次把问题全说完。让人改一条、重试、再发现第二条,在生产上就是三个变更窗口。
func TestPreflight_ReportsEveryProblemAtOnce(t *testing.T) {
	f := okFacts()
	f.HasPK = false
	f.LogBin = false
	f.Triggers = 1
	bs, _ := Preflight(f, addIdx)
	got := codes(bs)
	for _, want := range []string{"no_unique_key", "binlog_off", "triggers"} {
		if !got[want] {
			t.Errorf("缺少 %s;一次要把问题说完,实际: %+v", want, bs)
		}
	}
}

// 引擎不对时不该再吐一堆 MySQL 参数的抱怨 —— 那些话对着一个 PostgreSQL 实例毫无意义。
func TestPreflight_AWrongEngineStopsTheRestOfTheChecks(t *testing.T) {
	f := Facts{Engine: "postgres"} // 其余字段全空,若继续检查会连环报错
	bs, _ := Preflight(f, addIdx)
	if len(bs) != 1 || bs[0].Code != "not_mysql" {
		t.Errorf("引擎不对时只该说这一件事,实际: %+v", bs)
	}
}

// 提示与阻塞必须分开。把"会很慢"和"会丢数据"混在一列里,人就两个都不看了。
func TestPreflight_WarningsDoNotBlock(t *testing.T) {
	f := okFacts()
	f.GeneratedCols = 2
	f.LongestTrxSeconds = 900
	f.EstimatedRows = 200_000_000
	bs, ws := Preflight(f, addIdx)
	if len(bs) != 0 {
		t.Errorf("这些都是提示,不该阻塞: %+v", bs)
	}
	if len(ws) != 3 {
		t.Errorf("三条提示都该出现,实际: %+v", ws)
	}
}
