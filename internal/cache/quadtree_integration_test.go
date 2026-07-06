package cache

import (
	"testing"
	"time"

	"github.com/yaseer/spatialscale/internal/spatial"
)

// TestQuadTreeEvictAndReload verifies the full round trip: points inserted
// into a QuadTree, evicted to real Redis via EvictIdle, then transparently
// reloaded on the next query that touches that region.
func TestQuadTreeEvictAndReload(t *testing.T) {
	c := newTestCache(t)

	qt := spatial.NewQuadTree(spatial.BoundingBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100})
	qt.SetCache(c)

	points := []spatial.Point{
		{ID: 1, X: 10, Y: 10, Payload: []byte("Snap25:1")},
		{ID: 2, X: 20, Y: 20, Payload: []byte("Gad1:1")},
	}
	for _, p := range points {
		if err := qt.Insert(p); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	// Everything just got inserted, so nothing is idle yet.
	evicted, err := qt.EvictIdle(time.Hour)
	if err != nil {
		t.Fatalf("EvictIdle: %v", err)
	}
	if evicted != 0 {
		t.Fatalf("EvictIdle(1h) evicted %d nodes, want 0 (nothing should be idle yet)", evicted)
	}

	// Everything is idle relative to a zero threshold.
	evicted, err = qt.EvictIdle(0)
	if err != nil {
		t.Fatalf("EvictIdle: %v", err)
	}
	if evicted == 0 {
		t.Fatal("EvictIdle(0) evicted 0 nodes, want at least 1")
	}

	// Count must still reflect all points — eviction moves storage, not data.
	if got := qt.Count(); got != int64(len(points)) {
		t.Errorf("Count() after eviction = %d, want %d", got, len(points))
	}

	// A query touching the evicted region must transparently reload it.
	results, err := qt.RangeQuery(spatial.BoundingBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100})
	if err != nil {
		t.Fatalf("RangeQuery after eviction: %v", err)
	}
	if len(results) != len(points) {
		t.Fatalf("RangeQuery after eviction returned %d points, want %d", len(results), len(points))
	}
}
