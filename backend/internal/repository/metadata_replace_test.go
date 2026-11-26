package repository

import (
	"slices"
	"sort"
	"strconv"
	"testing"
	"time"

	"aegisdb/internal/model"
)

// 2026-10-28 元数据同步改为按照片比对:终态必须与"先删后插"逐行相同,
// 没变的行不再被重写,别的实例的缓存不受影响。

func newMetaRepo(t *testing.T) *Repo {
	t.Helper()
	return New(openStore(t))
}

func metaTbl(db, name, comment string) model.MetaTable {
	return model.MetaTable{DBName: db, Name: name, Kind: "table", Comment: comment}
}

func metaCol(db, table, name, typ string, ord int) model.MetaColumn {
	return model.MetaColumn{DBName: db, TableName_: table, Name: name, DataType: typ, Ordinal: ord, Nullable: true}
}

func allMeta(t *testing.T, r *Repo, connID int64) ([]model.MetaTable, []model.MetaColumn) {
	t.Helper()
	var ts []model.MetaTable
	var cs []model.MetaColumn
	if err := r.db.Where("connection_id = ?", connID).Order("db_name, table_name").Find(&ts).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Where("connection_id = ?", connID).Order("db_name, table_name, column_name").Find(&cs).Error; err != nil {
		t.Fatal(err)
	}
	return ts, cs
}

func colSig(cs []model.MetaColumn) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.DBName+"."+c.TableName_+"."+c.Name+":"+c.DataType)
	}
	sort.Strings(out)
	return out
}

func TestReplaceMetadata_DiffEndsIdenticalToSnapshot(t *testing.T) {
	r := newMetaRepo(t)
	if err := r.ReplaceMetadata(1,
		[]model.MetaTable{metaTbl("shop", "orders", ""), metaTbl("shop", "gone", ""), metaTbl("shop", "renamed", "old")},
		[]model.MetaColumn{
			metaCol("shop", "orders", "id", "bigint", 1),
			metaCol("shop", "orders", "amount", "int", 2),
			metaCol("shop", "orders", "dropped", "int", 3),
			metaCol("shop", "gone", "id", "int", 1),
		}); err != nil {
		t.Fatal(err)
	}
	ts0, cs0 := allMeta(t, r, 1)
	idOf := map[string]int64{}
	for _, c := range cs0 {
		idOf[c.TableName_+"."+c.Name] = c.ID
	}
	tableID := map[string]int64{}
	for _, tb := range ts0 {
		tableID[tb.Name] = tb.ID
	}

	time.Sleep(5 * time.Millisecond)
	// 存储库的时间列只到毫秒(timestamptz(3) / DATETIME(3)),入库会舍入:比较的下限按毫秒截断。
	before := time.Now().Truncate(time.Millisecond)
	// gone 表消失;orders.dropped 列消失;amount 改类型;新增 note 列和 fresh 表;renamed 的注释变了
	if err := r.ReplaceMetadata(1,
		[]model.MetaTable{metaTbl("shop", "orders", ""), metaTbl("shop", "renamed", "new"), metaTbl("shop", "fresh", "")},
		[]model.MetaColumn{
			metaCol("shop", "orders", "id", "bigint", 1),
			metaCol("shop", "orders", "amount", "decimal(10,2)", 2),
			metaCol("shop", "orders", "note", "varchar(64)", 3),
			metaCol("shop", "fresh", "id", "int", 1),
		}); err != nil {
		t.Fatal(err)
	}
	ts, cs := allMeta(t, r, 1)

	var names []string
	for _, tb := range ts {
		names = append(names, tb.Name+":"+tb.Comment)
		// 发布预检拿表的 synced_at 当缓存年龄:没变的表也必须刷成这次的时间
		if tb.SyncedAt.Before(before) {
			t.Errorf("表 %s 的 synced_at 没刷新: %v", tb.Name, tb.SyncedAt)
		}
	}
	if got, want := names, []string{"fresh:", "orders:", "renamed:new"}; !slices.Equal(got, want) {
		t.Fatalf("表 = %v, want %v", got, want)
	}
	if got, want := colSig(cs), []string{
		"shop.fresh.id:int", "shop.orders.amount:decimal(10,2)", "shop.orders.id:bigint", "shop.orders.note:varchar(64)",
	}; !slices.Equal(got, want) {
		t.Fatalf("列 = %v, want %v", got, want)
	}

	// 没变的行原地不动(这正是省下来的写入)
	for _, c := range cs {
		if c.TableName_ == "orders" && c.Name == "id" && c.ID != idOf["orders.id"] {
			t.Errorf("没变的列 orders.id 被重写了: id %d -> %d", idOf["orders.id"], c.ID)
		}
		if c.TableName_ == "orders" && c.Name == "amount" && c.ID == idOf["orders.amount"] {
			t.Errorf("变了类型的列 orders.amount 没有重写")
		}
	}
	for _, tb := range ts {
		if tb.Name == "orders" && tb.ID != tableID["orders"] {
			t.Errorf("没变的表 orders 被重写了")
		}
	}
}

func TestReplaceMetadata_EmptySnapshotClearsAndOtherConnUntouched(t *testing.T) {
	r := newMetaRepo(t)
	for _, conn := range []int64{1, 2} {
		if err := r.ReplaceMetadata(conn,
			[]model.MetaTable{metaTbl("shop", "orders", "")},
			[]model.MetaColumn{metaCol("shop", "orders", "id", "bigint", 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ReplaceMetadata(1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ts, cs := allMeta(t, r, 1); len(ts) != 0 || len(cs) != 0 {
		t.Fatalf("空照片应清空实例 1: tables=%d cols=%d", len(ts), len(cs))
	}
	if ts, cs := allMeta(t, r, 2); len(ts) != 1 || len(cs) != 1 {
		t.Fatalf("实例 2 不该受影响: tables=%d cols=%d", len(ts), len(cs))
	}
}

// 超过一批(metaIDBatch)的删除与插入都要走完,不能只处理第一批。
func TestReplaceMetadata_ManyRowsAcrossBatches(t *testing.T) {
	r := newMetaRepo(t)
	n := metaIDBatch*2 + 37
	mk := func(typ string) []model.MetaColumn {
		cs := make([]model.MetaColumn, 0, n)
		for i := 0; i < n; i++ {
			cs = append(cs, metaCol("shop", "wide", "c"+strconv.Itoa(i), typ, i+1))
		}
		return cs
	}
	tbls := []model.MetaTable{metaTbl("shop", "wide", "")}
	if err := r.ReplaceMetadata(1, tbls, mk("int")); err != nil {
		t.Fatal(err)
	}
	if err := r.ReplaceMetadata(1, []model.MetaTable{metaTbl("shop", "wide", "")}, mk("bigint")); err != nil {
		t.Fatal(err)
	}
	_, cs := allMeta(t, r, 1)
	if len(cs) != n {
		t.Fatalf("列数 = %d, want %d", len(cs), n)
	}
	for _, c := range cs {
		if c.DataType != "bigint" {
			t.Fatalf("列 %s 仍是旧类型 %s", c.Name, c.DataType)
		}
	}
	if err := r.ReplaceMetadata(1, []model.MetaTable{metaTbl("shop", "wide", "")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, cs := allMeta(t, r, 1); len(cs) != 0 {
		t.Fatalf("应全部删除,剩 %d", len(cs))
	}
}

// 按库拆事务:照片里消失的库整库清掉(它在远端已经没了),没变的库原地不动。
func TestReplaceMetadata_PerDatabaseDropsVanishedDB(t *testing.T) {
	r := newMetaRepo(t)
	if err := r.ReplaceMetadata(1,
		[]model.MetaTable{metaTbl("shop", "orders", ""), metaTbl("old_db", "t1", "")},
		[]model.MetaColumn{metaCol("shop", "orders", "id", "bigint", 1), metaCol("old_db", "t1", "id", "int", 1)}); err != nil {
		t.Fatal(err)
	}
	ts0, cs0 := allMeta(t, r, 1)
	if len(ts0) != 2 || len(cs0) != 2 {
		t.Fatalf("初始 tables=%d cols=%d", len(ts0), len(cs0))
	}
	var shopColID int64
	for _, c := range cs0 {
		if c.DBName == "shop" {
			shopColID = c.ID
		}
	}
	if err := r.ReplaceMetadata(1,
		[]model.MetaTable{metaTbl("shop", "orders", ""), metaTbl("new_db", "t2", "")},
		[]model.MetaColumn{metaCol("shop", "orders", "id", "bigint", 1), metaCol("new_db", "t2", "id", "int", 1)}); err != nil {
		t.Fatal(err)
	}
	ts, cs := allMeta(t, r, 1)
	if got, want := colSig(cs), []string{"new_db.t2.id:int", "shop.orders.id:bigint"}; !slices.Equal(got, want) {
		t.Fatalf("列 = %v, want %v", got, want)
	}
	if len(ts) != 2 {
		t.Fatalf("表数 = %d, want 2", len(ts))
	}
	for _, tb := range ts {
		if tb.DBName == "old_db" {
			t.Fatalf("消失的库 old_db 还留在缓存里")
		}
	}
	for _, c := range cs {
		if c.DBName == "shop" && c.ID != shopColID {
			t.Errorf("没变的库 shop 的列被重写了")
		}
	}
}

// 2026-11:Nullable 曾带 GORM 标签 default:true,建行时 false 被换成 true —— NOT NULL 列
// 落库就是"可空",下一轮照片里的 false 永远对不上,每一轮都被删了重插。
// 键之外每个字段都取零值,同一张照片写两遍,第二遍必须一行不动、且读回来与照片相同。
func TestReplaceMetadata_ZeroValuedFieldsRoundTrip(t *testing.T) {
	r := newMetaRepo(t)
	snap := func() []model.MetaColumn {
		return []model.MetaColumn{
			{DBName: "shop", TableName_: "orders", Name: "id", DataType: "bigint", Ordinal: 1, Nullable: false, IsPK: true},
			{DBName: "shop", TableName_: "orders", Name: "sn", DataType: "varchar(32)", Ordinal: 2, Nullable: false, ColDefault: "''"},
			{DBName: "shop", TableName_: "orders", Name: "zero", DataType: "int", Ordinal: 0, Nullable: false},
			{DBName: "shop", TableName_: "orders", Name: "note", DataType: "text", Ordinal: 3, Nullable: true, Comment: "备注"},
		}
	}
	tables := func() []model.MetaTable { return []model.MetaTable{metaTbl("shop", "orders", "")} }
	if err := r.ReplaceMetadata(1, tables(), snap()); err != nil {
		t.Fatal(err)
	}
	_, cs0 := allMeta(t, r, 1)
	want := map[string]model.MetaColumn{}
	for _, c := range snap() {
		want[c.Name] = c
	}
	for _, c := range cs0 {
		w := want[c.Name]
		if c.Nullable != w.Nullable || c.IsPK != w.IsPK || c.Ordinal != w.Ordinal || c.ColDefault != w.ColDefault {
			t.Errorf("%s 读回来与照片不同: 照片 %+v, 缓存 %+v", c.Name, w, c)
		}
	}
	if err := r.ReplaceMetadata(1, tables(), snap()); err != nil {
		t.Fatal(err)
	}
	_, cs1 := allMeta(t, r, 1)
	if len(cs1) != len(cs0) {
		t.Fatalf("列数变了: %d → %d", len(cs0), len(cs1))
	}
	for i := range cs0 {
		if cs0[i].ID != cs1[i].ID {
			t.Errorf("%s 没变却被重写(id %d → %d)", cs0[i].Name, cs0[i].ID, cs1[i].ID)
		}
	}
}
