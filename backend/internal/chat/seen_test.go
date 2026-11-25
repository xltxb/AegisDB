package chat

import (
	"testing"
	"time"
)

func TestSeen(t *testing.T) {
	now := time.Unix(1000, 0)
	s := NewSeen(3, time.Minute)
	s.now = func() time.Time { return now }
	if s.Again("a") || !s.Again("a") {
		t.Error("第二次才算重复")
	}
	now = now.Add(2 * time.Minute)
	if s.Again("a") {
		t.Error("过期之后不算")
	}
	for _, id := range []string{"b", "c", "d"} {
		s.Again(id)
	}
	if len(s.at) != 3 || len(s.order) != 3 {
		t.Errorf("应有界:%d %d", len(s.at), len(s.order))
	}
	if !s.Again("d") || !s.Again("b") || s.Again("a") {
		t.Error("满了先忘最早的")
	}
}
