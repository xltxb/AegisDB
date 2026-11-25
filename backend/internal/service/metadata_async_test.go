package service

import (
	"errors"
	"testing"
)

// 同一台实例同一时刻只跑一个同步;跑完记下致命错误,下一次开跑时清掉。
func TestMetaRun_OnePerConnection(t *testing.T) {
	s := &Services{}
	if !s.beginMetaRun(1) {
		t.Fatal("第一次应当占到")
	}
	if s.beginMetaRun(1) {
		t.Fatal("在跑时不能再起一个")
	}
	if !s.beginMetaRun(2) {
		t.Fatal("别的实例不受影响")
	}
	if r := s.metaRunOf(1); !r.running {
		t.Fatal("应当显示在跑")
	}
	s.endMetaRun(1, errors.New("dial tcp: connection refused"))
	r := s.metaRunOf(1)
	if r.running || r.failed == "" {
		t.Fatalf("跑完应当记下失败原因: %+v", r)
	}
	if !s.beginMetaRun(1) {
		t.Fatal("跑完后可以再起")
	}
	if r := s.metaRunOf(1); r.failed != "" {
		t.Fatal("重新开跑应当清掉上次的失败")
	}
	s.endMetaRun(1, nil)
	if r := s.metaRunOf(1); r.running || r.failed != "" {
		t.Fatalf("成功结束: %+v", r)
	}
}
