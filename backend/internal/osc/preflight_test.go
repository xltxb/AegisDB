package osc

import "testing"

// 一个各项条件都齐备的场景。每条用例只从它身上改一处,这样"是哪一处导致了拒绝"
// 不用猜。
func okFacts() Facts {
	return Facts{
		Engine: "mysql", Version: "8.0.36", VersionMajor: 8, VersionMinor: 0,
		LogBin: true, BinlogFormat: "ROW", BinlogRowImg: "FULL",
		HasReplSlave: true, HasReplClient: true,
		Schema: "app", Table: "t_order",
		TableExists: true, HasPK: true,
		EstimatedRows: 5_000_000, DataBytes: 2 << 30, FreeDiskBytes: 100 << 30,
	}
}

func codes(bs []Blocker) map[string]bool {
	m := map[string]bool{}
	for _, b := range bs {
		m[b.Code] = true
	}
	return m
}

func TestPreflight_AllGreenPasses(t *testing.T) {
	bs, _ := Preflight(okFacts(), ActionAddIndex)
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
		{"版本太老", "version_too_old", func(f *Facts) { f.VersionMajor, f.VersionMinor = 5, 5 }},
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
			bs, _ := Preflight(f, ActionAddIndex)
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
	bs, _ := Preflight(f, ActionAddIndex)
	if codes(bs)["no_unique_key"] {
		t.Error("有唯一非空索引就够了,不该因为缺主键被拒")
	}
}

// 阶段一只做索引。其它变更必须明确被挡住 —— 悄悄放过一个 MODIFY COLUMN,
// 拷贝时的类型转换语义没人验证过。
func TestPreflight_OnlyIndexChangesAreInScope(t *testing.T) {
	for _, act := range []string{ActionAddIndex, ActionDropIndex} {
		if bs, _ := Preflight(okFacts(), act); codes(bs)["unsupported_action"] {
			t.Errorf("%s 应当在范围内", act)
		}
	}
	if bs, _ := Preflight(okFacts(), "modify_column"); !codes(bs)["unsupported_action"] {
		t.Error("索引以外的变更必须被挡住")
	}
}

// 磁盘要按"原表 + 20%"算:影子表是完整副本,拷贝期间两份同时在盘上,而且还有正常
// 写入继续进来。磁盘写满会让整个实例不可用,不只是这次变更失败。
func TestPreflight_DiskMustHoldASecondCopyPlusHeadroom(t *testing.T) {
	f := okFacts()
	f.DataBytes = 100 << 30
	f.FreeDiskBytes = 110 << 30 // 够放一份副本,但不够 20% 余量
	bs, _ := Preflight(f, ActionAddIndex)
	if !codes(bs)["disk_space"] {
		t.Error("余量不足 20% 时应当拒绝 —— 拷贝期间还有写入进来")
	}

	f.FreeDiskBytes = 130 << 30
	bs, _ = Preflight(f, ActionAddIndex)
	if codes(bs)["disk_space"] {
		t.Error("余量充足时不该拒绝")
	}
}

// 拿不到磁盘余量时不能假装它是 0 —— 那会把每一次变更都拒掉。
func TestPreflight_UnknownDiskSpaceDoesNotBlock(t *testing.T) {
	f := okFacts()
	f.FreeDiskBytes = 0 // 0 = 没拿到,不是"没有空间"
	bs, _ := Preflight(f, ActionAddIndex)
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
	bs, _ := Preflight(f, ActionAddIndex)
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
	bs, _ := Preflight(f, ActionAddIndex)
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
	bs, ws := Preflight(f, ActionAddIndex)
	if len(bs) != 0 {
		t.Errorf("这些都是提示,不该阻塞: %+v", bs)
	}
	if len(ws) != 3 {
		t.Errorf("三条提示都该出现,实际: %+v", ws)
	}
}
