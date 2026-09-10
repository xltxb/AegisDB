package repository

// 元数据缓存的读写。
//
// 写只有一种形态:**整台实例先删后插**,一次事务。
//
// 增量更新(比对差异、只改变化的行)在这里是错的方向:远端的表会被改名、被删,而
// 一次同步拿到的是一张完整的照片 —— 用它整个替换,天然就把消失的表带走了。增量则
// 要额外回答"哪些行该删",答错的表现是缓存里留着一张早已不存在的表,而它看起来和
// 真表一模一样。

import (
	"time"

	"gorm.io/gorm"

	"velagateway/internal/model"
)

// metaInsertBatch 一次插多少行。
//
// 一台几千张表的实例有几万列,一条 INSERT 塞不下 —— MySQL 有 max_allowed_packet,
// 而 GORM 会把整批拼成一条语句。分批既避开这个上限,也让单条语句的锁持有时间可控。
const metaInsertBatch = 500

// ReplaceMetadata 用这一次探查的结果整个替换一台实例的元数据缓存。
//
// 在事务里做:中途失败时,缓存要么是旧的那份、要么是新的那份,不会是"删了一半"。
// 一个只剩一半表的缓存比一个过期的缓存糟得多 —— 过期的至少还是自洽的。
func (r *Repo) ReplaceMetadata(connID int64, tables []model.MetaTable, cols []model.MetaColumn) error {
	now := time.Now()
	for i := range tables {
		tables[i].ConnectionID, tables[i].SyncedAt = connID, now
	}
	for i := range cols {
		cols[i].ConnectionID, cols[i].SyncedAt = connID, now
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("connection_id = ?", connID).Delete(&model.MetaTable{}).Error; err != nil {
			return err
		}
		if err := tx.Where("connection_id = ?", connID).Delete(&model.MetaColumn{}).Error; err != nil {
			return err
		}
		if len(tables) > 0 {
			if err := tx.CreateInBatches(&tables, metaInsertBatch).Error; err != nil {
				return err
			}
		}
		if len(cols) > 0 {
			if err := tx.CreateInBatches(&cols, metaInsertBatch).Error; err != nil {
				return err
			}
		}
		return nil
	})
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
