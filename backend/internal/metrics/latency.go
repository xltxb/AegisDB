// Package metrics collects lightweight, in-process gateway metrics (real request
// latency) so the UI can show live figures instead of hard-coded numbers.
package metrics

import (
	"sort"
	"sync"
	"time"
)

// Recorder keeps a rolling window of recent request latencies (milliseconds).
type Recorder struct {
	mu   sync.Mutex
	buf  []float64
	size int
	pos  int
	full bool
}

// Default is the process-wide latency recorder fed by the HTTP middleware.
var Default = &Recorder{size: 512}

// Record adds one request duration to the rolling window.
func (r *Recorder) Record(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000.0
	r.mu.Lock()
	if r.buf == nil {
		r.buf = make([]float64, r.size)
	}
	r.buf[r.pos] = ms
	r.pos = (r.pos + 1) % r.size
	if r.pos == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

func (r *Recorder) percentile(p float64) float64 {
	r.mu.Lock()
	n := r.pos
	if r.full {
		n = r.size
	}
	if n == 0 {
		r.mu.Unlock()
		return 0
	}
	cp := make([]float64, n)
	copy(cp, r.buf[:n])
	r.mu.Unlock()

	sort.Float64s(cp)
	idx := int(p * float64(n-1))
	return cp[idx]
}

// P50 returns the median latency (ms); 0 when no samples yet.
func (r *Recorder) P50() float64 { return r.percentile(0.50) }

// P95 returns the 95th-percentile latency (ms).
func (r *Recorder) P95() float64 { return r.percentile(0.95) }

// Samples returns how many latencies are in the window.
func (r *Recorder) Samples() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return r.size
	}
	return r.pos
}
