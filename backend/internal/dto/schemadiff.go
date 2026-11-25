package dto

import (
	"time"

	"aegisdb/internal/model"
	"aegisdb/internal/schemadiff"
)

// SchemaDiffSide 是对比的一边:哪个实例的哪个库。Database 留空用实例配置的默认库
// (Oracle 为登录账号自己的 schema)。
type SchemaDiffSide struct {
	ConnectionID int64  `json:"connectionId"`
	Database     string `json:"database"`
	// Schema 只对 PG / DWS 有意义:空 = 库内全部非系统 schema;指定了只比这一个(表名不带前缀)。
	Schema string `json:"schema"`
}

type SchemaDiffReq struct {
	Left    SchemaDiffSide     `json:"left"`
	Right   SchemaDiffSide     `json:"right"`
	Options schemadiff.Options `json:"options"`
}

// SchemaDiffSideView 是一边的说明:报告头上要写清比的是谁、什么时候读的。
type SchemaDiffSideView struct {
	ConnectionID int64     `json:"connectionId"`
	Instance     string    `json:"instance"`
	Env          string    `json:"env"`
	Engine       string    `json:"engine"`
	Database     string    `json:"database"`
	Schema       string    `json:"schema,omitempty"`
	ReadAt       time.Time `json:"readAt"`
	Tables       int       `json:"tables"`
}

// SchemaDiffResp 是一次对比。读取失败(连不上、超时、库太大)不是拒绝:回 0,Error 说明是哪一边、
// 为什么,Diff 为空 —— 不出半份报告。
type SchemaDiffResp struct {
	Left      SchemaDiffSideView `json:"left"`
	Right     SchemaDiffSideView `json:"right"`
	Diff      *schemadiff.Result `json:"diff,omitempty"`
	Error     string             `json:"error,omitempty"`
	ErrorSide string             `json:"errorSide,omitempty"` // left | right
	// ErrorRef 是 Error 的机器可读形式,前端按读者语言显示:code = sdReadFailed(args: err 驱动原文)
	// / sdReadTimeout(args: sec)/ sdTooLarge(args: n);另有 instance、db 两个参数。
	ErrorRef *model.RuleRef `json:"errorRef,omitempty"`
	// EmptySide:这一边一张表都没读到(left / right / both)。不是错误 —— 新环境本来可能是空库 ——
	// 但多半是库名写错了,页面要提示一句。
	EmptySide string `json:"emptySide,omitempty"`
}

// SchemaDiffConn 是可以参与对比的一个实例(调用人能访问、引擎支持)。
type SchemaDiffConn struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Env      string `json:"env"`
	Engine   string `json:"engine"`
	Family   string `json:"family"` // mysql | oracle | postgres | sqlite:只有同一族能互比
	Database string `json:"database"`
}
