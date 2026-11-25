package chat

import (
	"slices"
	"sync"
	"time"
)

// Seen 记住近期处理过的消息号,认出平台重推的。有界:满了先忘最早的。
type Seen struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	now   func() time.Time
	at    map[string]time.Time
	order []string // 按先后
}

// NewSeen 最多记 max 个,每个记 ttl 这么久。
func NewSeen(max int, ttl time.Duration) *Seen {
	return &Seen{max: max, ttl: ttl, now: time.Now, at: map[string]time.Time{}}
}

// Again 记下一个号,报告它是不是已经见过。
func (s *Seen) Again(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for len(s.order) > 0 && now.Sub(s.at[s.order[0]]) >= s.ttl {
		s.forget()
	}
	if _, ok := s.at[id]; ok {
		return true
	}
	if len(s.order) >= s.max {
		s.forget()
	}
	s.at[id] = now
	s.order = append(s.order, id)
	return false
}

// Forget 忘掉一个号:当时没来得及处理,平台再给时要处理。
func (s *Seen) Forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.at[id]; ok {
		delete(s.at, id)
		s.order = slices.DeleteFunc(s.order, func(x string) bool { return x == id })
	}
}

func (s *Seen) forget() {
	delete(s.at, s.order[0])
	s.order = s.order[1:]
}
