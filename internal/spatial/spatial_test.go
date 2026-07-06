package spatial

import (
	"fmt"
	"sync"
	"testing"
)

// --- BoundingBox tests ---

func TestBoundingBoxContains(t *testing.T) {
	bb := BoundingBox{0, 0, 10, 10}
	cases := []struct {
		p    Point
		want bool
	}{
		{Point{X: 5, Y: 5}, true},    // center
		{Point{X: 0, Y: 0}, true},    // corner (inclusive)
		{Point{X: 10, Y: 10}, true},  // opposite corner
		{Point{X: 11, Y: 5}, false},  // outside right
		{Point{X: 5, Y: -1}, false},  // outside below
	}
	for _, c := range cases {
		got := bb.Contains(c.p)
		if got != c.want {
			t.Errorf("Contains(%v) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestBoundingBoxIntersects(t *testing.T) {
	a := BoundingBox{0, 0, 10, 10}
	cases := []struct {
		b    BoundingBox
		want bool
	}{
		{BoundingBox{5, 5, 15, 15}, true},   // overlapping
		{BoundingBox{10, 10, 20, 20}, true}, // edge touch
		{BoundingBox{11, 0, 20, 10}, false}, // fully to the right
		{BoundingBox{0, 11, 10, 20}, false}, // fully above
	}
	for _, c := range cases {
		got := a.Intersects(c.b)
		if got != c.want {
			t.Errorf("Intersects(%v) = %v, want %v", c.b, got, c.want)
		}
	}
}

func TestBoundingBoxSubdivide(t *testing.T) {
	bb := BoundingBox{0, 0, 10, 10}
	quads := bb.Subdivide()

	// Each quadrant should have 1/4 the area of the parent.
	wantArea := bb.Area() / 4
	for i, q := range quads {
		if got := q.Area(); got != wantArea {
			t.Errorf("quad[%d] area = %v, want %v", i, got, wantArea)
		}
	}

	// The 4 quadrants must cover every corner of the original box exactly once.
	corners := []Point{
		{X: 1, Y: 9},  // NW
		{X: 9, Y: 9},  // NE
		{X: 1, Y: 1},  // SW
		{X: 9, Y: 1},  // SE
	}
	for _, corner := range corners {
		count := 0
		for _, q := range quads {
			if q.Contains(corner) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("corner %v contained in %d quadrants, want 1", corner, count)
		}
	}
}

// --- QuadTree tests ---

func TestQuadTreeInsertAndCount(t *testing.T) {
	qt := NewQuadTree(WorldBounds)
	points := []Point{
		{ID: 1, X: -74.006, Y: 40.713},  // New York
		{ID: 2, X: -87.629, Y: 41.878},  // Chicago
		{ID: 3, X: -118.243, Y: 34.052}, // Los Angeles
	}
	for _, p := range points {
		if err := qt.Insert(p); err != nil {
			t.Fatalf("Insert(%v) error: %v", p, err)
		}
	}
	if got := qt.Count(); got != int64(len(points)) {
		t.Errorf("Count() = %d, want %d", got, len(points))
	}
}

func TestQuadTreeOutOfBounds(t *testing.T) {
	qt := NewQuadTree(WorldBounds)
	err := qt.Insert(Point{ID: 1, X: 200, Y: 0}) // longitude > 180
	if err == nil {
		t.Error("expected error for out-of-bounds point, got nil")
	}
}

func TestQuadTreeRangeQuery(t *testing.T) {
	qt := NewQuadTree(WorldBounds)

	// Insert points in two well-separated regions.
	nyArea := []Point{
		{ID: 1, X: -74.0, Y: 40.7},
		{ID: 2, X: -73.9, Y: 40.8},
		{ID: 3, X: -74.1, Y: 40.6},
	}
	laArea := []Point{
		{ID: 4, X: -118.2, Y: 34.0},
		{ID: 5, X: -118.3, Y: 34.1},
	}
	for _, p := range append(nyArea, laArea...) {
		qt.Insert(p)
	}

	// Query a box around New York only.
	nyBox := BoundingBox{MinX: -74.5, MinY: 40.4, MaxX: -73.5, MaxY: 41.0}
	results, err := qt.RangeQuery(nyBox)
	if err != nil {
		t.Fatalf("RangeQuery error: %v", err)
	}
	if len(results) != len(nyArea) {
		t.Errorf("RangeQuery returned %d points, want %d", len(results), len(nyArea))
	}
}

func TestQuadTreeSplit(t *testing.T) {
	// Use a small capacity to force splits quickly.
	qt := &QuadTree{
		root: &quadNode{
			bounds: WorldBounds,
			state:  stateLeaf,
			points: make([]Point, 0, 4),
		},
		capacity: 4,
		maxDepth: DefaultMaxDepth,
	}

	// Insert enough points to trigger at least one split.
	for i := 0; i < 20; i++ {
		p := Point{
			ID: uint64(i),
			X:  float64(i%10) * 10,
			Y:  float64(i/10) * 10,
		}
		if err := qt.Insert(p); err != nil {
			t.Fatalf("Insert error: %v", err)
		}
	}

	if qt.Count() != 20 {
		t.Errorf("Count = %d, want 20", qt.Count())
	}
	if qt.PartitionCount() < 2 {
		t.Errorf("expected tree to have split into multiple partitions")
	}
}

// TestQuadTreeConcurrentInsert verifies that concurrent inserts don't
// corrupt the tree or cause data races (run with -race).
func TestQuadTreeConcurrentInsert(t *testing.T) {
	qt := NewQuadTree(WorldBounds)
	const goroutines = 8
	const pointsEach = 1000

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < pointsEach; i++ {
				id := uint64(offset*pointsEach + i)
				p := Point{
					ID: id,
					X:  float64(id%360) - 180,
					Y:  float64(id%180) - 90,
				}
				if err := qt.Insert(p); err != nil {
					t.Errorf("Insert error: %v", err)
				}
			}
		}(g)
	}
	wg.Wait()

	want := int64(goroutines * pointsEach)
	if got := qt.Count(); got != want {
		t.Errorf("Count() = %d after concurrent inserts, want %d", got, want)
	}
}

// TestQuadTreeConcurrentQueryAndInsert verifies reads and writes can
// safely interleave — the key correctness property for a live query engine.
func TestQuadTreeConcurrentQueryAndInsert(t *testing.T) {
	qt := NewQuadTree(WorldBounds)

	// Pre-load some points.
	for i := 0; i < 500; i++ {
		qt.Insert(Point{ID: uint64(i), X: float64(i%360) - 180, Y: float64(i%180) - 90})
	}

	var wg sync.WaitGroup
	errs := make(chan error, 100)

	// Writers.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				id := uint64(1000 + offset*250 + i)
				qt.Insert(Point{ID: id, X: float64(id%360) - 180, Y: float64(id%180) - 90})
			}
		}(g)
	}

	// Readers.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			bb := BoundingBox{MinX: float64(g*10) - 20, MinY: -10, MaxX: float64(g*10), MaxY: 10}
			for i := 0; i < 100; i++ {
				if _, err := qt.RangeQuery(bb); err != nil {
					errs <- fmt.Errorf("RangeQuery error: %w", err)
				}
			}
		}(g)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
