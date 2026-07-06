// Package metrics records operation latencies and reports percentiles,
// used to turn "sub-10ms p99" into a measured number instead of a guess.
package metrics

import (
	"sort"
	"sync"
	"time"
)

// LatencyRecorder collects a stream of durations and computes percentiles.
// Safe for concurrent use.
type LatencyRecorder struct {
	mu      sync.Mutex
	samples []time.Duration
}

func NewLatencyRecorder() *LatencyRecorder {
	return &LatencyRecorder{}
}

// Record adds one observed duration.
func (r *LatencyRecorder) Record(d time.Duration) {
	r.mu.Lock()
	r.samples = append(r.samples, d)
	r.mu.Unlock()
}

// Percentile returns the p-th percentile duration (e.g. p=99 for p99).
// Returns 0 if no samples have been recorded.
func (r *LatencyRecorder) Percentile(p float64) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), r.samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	idx := int(p / 100 * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Count returns the number of recorded samples.
func (r *LatencyRecorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.samples)
}
