package service

import (
	"log/slog"
	"sync"
	"time"

	"aegisdb/internal/gateway"
	"aegisdb/internal/model"
)

// 业务方数据查询走从库。
//
// 配了从库的数据源,数据查询的全部流量(执行、导出、判定用的库名、库表下拉)都打到从库;
// 终端、审批执行、发布不走这里,仍在主库上 —— 它们要写,或要读刚写下的数据。
//
// 从库连不上时回落主库(用户的选择:可用性优先),但只在"连不上"时回落:连上了、查询本身
// 报错或超时,照实报错。把一条在从库上超时的慢查询再拿去主库跑一遍,恰好是配从库要避免的事。
//
// 探测结果短暂缓存:从库挂了的时候,不能让每一条查询都先等满探测时限再回落。

const (
	replicaOKTTL   = 10 * time.Second // 探通之后多久内不再探
	replicaDownTTL = 30 * time.Second // 探不通之后多久内直接回落,不再探
)

// 本次数据查询由谁提供。
const (
	servedPrimary  = ""                // 没配从库:与从前一样,不标注
	servedReplica  = "replica"         // 从库
	servedFallback = "primaryFallback" // 配了从库但连不上,回落主库
	servedReadonly = "readonlyAccount" // 与主库同地址的只读账号(TiDB 等没有从库的架构)
)

type replicaHealth struct {
	mu    sync.Mutex
	state map[string]replicaState // key:从库地址 + 账号,换了配置自然失效
}

type replicaState struct {
	err error
	at  time.Time
}

func replicaKey(r *model.Connection) string {
	return r.Host + "\x00" + model.Itoa(r.Port) + "\x00" + r.Username
}

// probe 返回从库此刻是否可用,带缓存。
func (h *replicaHealth) probe(r *model.Connection) error {
	k := replicaKey(r)
	h.mu.Lock()
	if h.state == nil {
		h.state = map[string]replicaState{}
	}
	st, ok := h.state[k]
	h.mu.Unlock()
	if ok {
		ttl := replicaOKTTL
		if st.err != nil {
			ttl = replicaDownTTL
		}
		if time.Since(st.at) < ttl {
			return st.err
		}
	}
	err := gateway.ProbeReplica(r)
	h.mu.Lock()
	h.state[k] = replicaState{err: err, at: time.Now()}
	h.mu.Unlock()
	return err
}

// bizTarget 选这次数据查询打到哪台库。conn 是主库(已套上目标库);返回要连的那一份、
// 由谁提供(served*),以及回落时从库连不上的原因。
func (s *Services) bizTarget(conn *model.Connection) (*model.Connection, string, string) {
	r := gateway.ReplicaOf(conn)
	if r == nil {
		return conn, servedPrimary, ""
	}
	// 同地址的只读账号:不探、不回落 —— 回落用的是主库账号。连不上就让查询照实报错。
	if gateway.ReplicaIsReadOnlyAccount(conn) {
		return r, servedReadonly, ""
	}
	if err := s.replicas.probe(r); err != nil {
		slog.Warn("从库不可用,数据查询回落主库", "conn", conn.Name, "replica", r.Host, "err", err)
		return conn, servedFallback, err.Error()
	}
	return r, servedReplica, ""
}

// servedTag 是写进审计 operator 的那一小段:这次由谁提供。没配从库时为空,与从前一样。
func servedTag(served string) string {
	switch served {
	case servedReplica:
		return "从库"
	case servedFallback:
		return "从库不可用,由主库提供"
	case servedReadonly:
		return "只读账号"
	}
	return ""
}
