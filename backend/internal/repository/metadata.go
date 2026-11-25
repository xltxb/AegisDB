package repository

// 元数据缓存的读写。
//
// 写只有一种入口:**拿这一次探查的完整照片,整台实例对齐**。
//
// 最早是"整台先删后插,一次事务"。2026-10 生产 fat-mysql(3659 表 / 14.2 万列)
// 一次手动同步 23 秒,其中读目标库 2 秒、写网关库 22 秒,而这期间别的实例的同步在
// 这张表上等锁(94 号删 5180 行等了 13.8 秒)。于是两处改动:
//
// **按照片比对**:读出现有的行,与新照片逐行对键(库 / schema / 表 [/ 列])——
//
//   照片里没有的            删
//   照片里有、内容变了的    删了重插
//   照片里有、内容没变的    不动
//   缓存里没有的            插
//
// 这不是"增量更新":输入仍然是一张完整的照片,"哪些该删"由照片回答(不在照片里
// 就删),消失的表照样被带走。结束时缓存与照片逐行相同,与先删后插的终态一致。
//
// **按库拆事务**:一个库一个事务,每个事务只锁这个库的行、很快提交,别的实例的
// 同步不再等一整台大实例写完。代价是原子性从"整台"降到"一个库":中途失败时,
// 前面的库是新的、后面的库是旧的 —— 但每个库自己仍是自洽的(表与列同一事务),
// 而表行上的 synced_at 照实说着每个库是哪一次的;失败本身记在 tbl_meta_sync 上。
// 一个只剩一半表的**库**才是不能接受的,而这在单库事务里不会发生。

import (
	"log/slog"
	"slices"
	"time"

	"gorm.io/gorm"

	"aegisdb/internal/model"
)

// metaInsertBatch 一次插多少行。
//
// 一台几千张表的实例有几万列,一条 INSERT 塞不下 —— MySQL 有 max_allowed_packet,
// 而 GORM 会把整批拼成一条语句。分批既避开这个上限,也让单条语句的锁持有时间可控。
const metaInsertBatch = 500

// metaIDBatch 按 id 删 / 改时一条语句带多少个 id(SQLite 的绑定参数上限是 999)。
const metaIDBatch = 500

type metaTableKey struct{ db, schema, table string }

type metaColumnKey struct{ db, schema, table, column string }

// metaWriteStats 一次写缓存的规模与耗时,打进日志 —— 同步慢时据此分清是改动多还是写得慢。
type metaWriteStats struct {
	colsDropped, colsAdded, tablesDropped, tablesAdded int
	load, del, ins                                     time.Duration
}

// ReplaceMetadata 用这一次探查的结果整个替换一台实例的元数据缓存,一个库一个事务。
//
// 照片里没有、缓存里有的库也要走一遍(整库删掉):那个库在远端已经没了。
//
// synced_at:表的每一行都刷成这次的时间(发布预检拿它说"这份缓存是什么时候的",
// 一张没变的表也是这次核对过的);列只在被写入时带上这次的时间 —— 14 万列全刷一遍
// 就又回到了先删后插的代价,而列的 synced_at 没有读的人。
func (r *Repo) ReplaceMetadata(connID int64, tables []model.MetaTable, cols []model.MetaColumn) error {
	now := time.Now()
	tablesOf := map[string][]model.MetaTable{}
	colsOf := map[string][]model.MetaColumn{}
	for i := range tables {
		tables[i].ConnectionID, tables[i].SyncedAt = connID, now
		tablesOf[tables[i].DBName] = append(tablesOf[tables[i].DBName], tables[i])
	}
	for i := range cols {
		cols[i].ConnectionID, cols[i].SyncedAt = connID, now
		colsOf[cols[i].DBName] = append(colsOf[cols[i].DBName], cols[i])
	}

	var oldTableDBs, oldColDBs []string
	if err := r.db.Model(&model.MetaTable{}).Where("connection_id = ?", connID).Distinct().Pluck("db_name", &oldTableDBs).Error; err != nil {
		return err
	}
	if err := r.db.Model(&model.MetaColumn{}).Where("connection_id = ?", connID).Distinct().Pluck("db_name", &oldColDBs).Error; err != nil {
		return err
	}
	dbSet := map[string]bool{}
	for _, list := range [][]string{oldTableDBs, oldColDBs} {
		for _, d := range list {
			dbSet[d] = true
		}
	}
	for d := range tablesOf {
		dbSet[d] = true
	}
	for d := range colsOf {
		dbSet[d] = true
	}
	dbs := make([]string, 0, len(dbSet))
	for d := range dbSet {
		dbs = append(dbs, d)
	}
	slices.Sort(dbs)

	var st metaWriteStats
	for _, db := range dbs {
		if err := r.db.Transaction(func(tx *gorm.DB) error {
			return replaceMetaDB(tx, connID, db, tablesOf[db], colsOf[db], now, &st)
		}); err != nil {
			return err
		}
	}
	// 同步慢时看这一行:改动多(fat 环境频繁建删库)时删 + 插仍然是大头。
	slog.Info("metadata cache write", "conn_id", connID, "databases", len(dbs),
		"cols_kept", len(cols)-st.colsAdded, "cols_dropped", st.colsDropped, "cols_added", st.colsAdded,
		"tables_dropped", st.tablesDropped, "tables_added", st.tablesAdded,
		"load_took", st.load.String(), "delete_took", st.del.String(), "insert_took", st.ins.String())
	return nil
}

// replaceMetaDB 在一个事务里把一台实例的一个库对齐到照片。
func replaceMetaDB(tx *gorm.DB, connID int64, db string, tables []model.MetaTable, cols []model.MetaColumn, now time.Time, st *metaWriteStats) error {
	loadStart := time.Now()
	var oldTables []model.MetaTable
	if err := tx.Where("connection_id = ? AND db_name = ?", connID, db).Find(&oldTables).Error; err != nil {
		return err
	}
	have := make(map[metaTableKey]model.MetaTable, len(oldTables))
	for _, t := range oldTables {
		have[metaTableKey{t.DBName, t.SchemaName, t.Name}] = t
	}
	var addTables []model.MetaTable
	var keepTableIDs []int64
	for _, t := range tables {
		k := metaTableKey{t.DBName, t.SchemaName, t.Name}
		if old, ok := have[k]; ok && old.Kind == t.Kind && old.Comment == t.Comment {
			keepTableIDs = append(keepTableIDs, old.ID)
			delete(have, k)
			continue
		}
		addTables = append(addTables, t)
	}
	dropTableIDs := make([]int64, 0, len(have))
	for _, t := range have {
		dropTableIDs = append(dropTableIDs, t.ID)
	}

	var oldCols []model.MetaColumn
	if err := tx.Where("connection_id = ? AND db_name = ?", connID, db).Find(&oldCols).Error; err != nil {
		return err
	}
	haveCols := make(map[metaColumnKey]model.MetaColumn, len(oldCols))
	for _, c := range oldCols {
		haveCols[metaColumnKey{c.DBName, c.SchemaName, c.TableName_, c.Name}] = c
	}
	var addCols []model.MetaColumn
	for _, c := range cols {
		k := metaColumnKey{c.DBName, c.SchemaName, c.TableName_, c.Name}
		if old, ok := haveCols[k]; ok && sameMetaColumn(old, c) {
			delete(haveCols, k)
			continue
		}
		addCols = append(addCols, c)
	}
	dropColIDs := make([]int64, 0, len(haveCols))
	for _, c := range haveCols {
		dropColIDs = append(dropColIDs, c.ID)
	}

	deleteStart := time.Now()
	// 先删后插:"删了重插"的那一行与新行键相同,唯一索引不允许两份同时在。
	for _, ids := range chunkIDs(dropTableIDs) {
		if err := tx.Where("id IN ?", ids).Delete(&model.MetaTable{}).Error; err != nil {
			return err
		}
	}
	for _, ids := range chunkIDs(dropColIDs) {
		if err := tx.Where("id IN ?", ids).Delete(&model.MetaColumn{}).Error; err != nil {
			return err
		}
	}
	for _, ids := range chunkIDs(keepTableIDs) {
		if err := tx.Model(&model.MetaTable{}).Where("id IN ?", ids).Update("synced_at", now).Error; err != nil {
			return err
		}
	}
	insertStart := time.Now()
	if len(addTables) > 0 {
		if err := tx.CreateInBatches(&addTables, metaInsertBatch).Error; err != nil {
			return err
		}
	}
	if len(addCols) > 0 {
		if err := tx.CreateInBatches(&addCols, metaInsertBatch).Error; err != nil {
			return err
		}
	}
	st.colsDropped += len(dropColIDs)
	st.colsAdded += len(addCols)
	st.tablesDropped += len(dropTableIDs)
	st.tablesAdded += len(addTables)
	st.load += deleteStart.Sub(loadStart)
	st.del += insertStart.Sub(deleteStart)
	st.ins += time.Since(insertStart)
	return nil
}

// sameMetaColumn 键之外的每个字段都相同。新增字段时这里要跟着加,否则那个字段的
// 变化永远同步不进来。
func sameMetaColumn(a, b model.MetaColumn) bool {
	return a.Ordinal == b.Ordinal && a.DataType == b.DataType && a.Nullable == b.Nullable &&
		a.ColDefault == b.ColDefault && a.Comment == b.Comment && a.IsPK == b.IsPK
}

// chunkIDs 按 id 升序切批。id 是插入顺序,同一张表的列挨在一起;排好序的一批落在
// 相邻的主键页上,而 map 遍历出来的乱序一批会散落在整棵主键树上。
func chunkIDs(ids []int64) [][]int64 {
	slices.Sort(ids)
	var out [][]int64
	for len(ids) > metaIDBatch {
		out = append(out, ids[:metaIDBatch])
		ids = ids[metaIDBatch:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

// SaveMetaSync 记下这台实例最近一次同步的结果。
//
// 成功时把 err 清空 —— 留着上次的错误会让一台已经好了的实例永远显示成坏的。
func (r *Repo) SaveMetaSync(s *model.MetaSync) error {
	return r.db.Save(s).Error
}

// MetaSyncStates 全部实例的同步状态,给界面回答"这台为什么没有数据"。
func (r *Repo) MetaSyncStates() ([]model.MetaSync, error) {
	var rows []model.MetaSync
	err := r.db.Find(&rows).Error
	return rows, err
}

// MetaTablesOf 一台实例缓存下来的表清单;db 非空时只要那个库的。
func (r *Repo) MetaTablesOf(connID int64, db string) ([]model.MetaTable, error) {
	q := r.db.Where("connection_id = ?", connID)
	if db != "" {
		q = q.Where("db_name = ?", db)
	}
	var rows []model.MetaTable
	err := q.Order("db_name, schema_name, table_name").Find(&rows).Error
	return rows, err
}

// MetaColumnsOf 一张表缓存下来的列,按序号。
func (r *Repo) MetaColumnsOf(connID int64, db, schema, table string) ([]model.MetaColumn, error) {
	q := r.db.Where("connection_id = ? AND db_name = ? AND table_name = ?", connID, db, table)
	if schema != "" {
		q = q.Where("schema_name = ?", schema)
	}
	var rows []model.MetaColumn
	err := q.Order("ordinal").Find(&rows).Error
	return rows, err
}

// SearchMetaTables 在**够得到的那些实例**里按表名找。
//
// connIDs 由调用方按标签授权算好并传进来 —— 权限不在这一层判,但它必须收在 SQL 里:
// 取回来再在应用层筛会让分页的总数和实际行数对不上,而那种列表会对着第三页的结果说
// "没有"(与 approvalScope 同一条理由)。空清单表示这个人够不到任何实例,直接不查。
func (r *Repo) SearchMetaTables(connIDs []int64, q string, limit int) ([]model.MetaTable, error) {
	if len(connIDs) == 0 || q == "" {
		return []model.MetaTable{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []model.MetaTable
	err := r.db.Where("connection_id IN ?", connIDs).
		Where("table_name LIKE ?", "%"+q+"%").
		Order("db_name, table_name").Limit(limit).Find(&rows).Error
	return rows, err
}

// SearchMetaColumns 按**列名**找 —— 这是缓存真正换来的能力。
//
// 实时探查做不到这件事:它意味着把每台实例的每张表都问一遍列,而那是一轮对生产的
// 全量扫描。有了本地副本,"哪些表里有 id_card 这一列"才成为一次普通查询。
func (r *Repo) SearchMetaColumns(connIDs []int64, q string, limit int) ([]model.MetaColumn, error) {
	if len(connIDs) == 0 || q == "" {
		return []model.MetaColumn{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []model.MetaColumn
	err := r.db.Where("connection_id IN ?", connIDs).
		Where("column_name LIKE ?", "%"+q+"%").
		Order("db_name, table_name, ordinal").Limit(limit).Find(&rows).Error
	return rows, err
}
