package metrics

import (
	"testing"
	"time"
)

func TestLatencyRecorderPercentile(t *testing.T) {
	r := NewLatencyRecorder()
	for i := 1; i <= 100; i++ {
		r.Record(time.Duration(i) * time.Millisecond)
	}
	if got := r.Percentile(99); got != 100*time.Millisecond {
		t.Errorf("p99 = %v, want 100ms", got)
	}
	if got := r.Percentile(50); got != 51*time.Millisecond {
		t.Errorf("p50 = %v, want 51ms", got)
	}
	if got := r.Count(); got != 100 {
		t.Errorf("Count() = %d, want 100", got)
	}
}

func TestLatencyRecorderEmpty(t *testing.T) {
	r := NewLatencyRecorder()
	if got := r.Percentile(99); got != 0 {
		t.Errorf("Percentile on empty recorder = %v, want 0", got)
	}
}
