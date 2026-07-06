package query

import (
	"context"
	"testing"

	"github.com/yaseer/spatialscale/internal/spatial"
	pb "github.com/yaseer/spatialscale/proto/spatialscalepb"
)

func TestRangeQueryGeneFilter(t *testing.T) {
	tree := spatial.NewQuadTree(spatial.BoundingBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100})
	points := []spatial.Point{
		{ID: 1, X: 1, Y: 1, Payload: []byte("Cygb:1")},
		{ID: 2, X: 2, Y: 2, Payload: []byte("Snap25:3")},
		{ID: 3, X: 3, Y: 3, Payload: []byte("Cygb2:1")}, // must not match "Cygb" prefix
	}
	for _, p := range points {
		if err := tree.Insert(p); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	svc := NewService(tree)
	box := &pb.BoundingBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}

	all, err := svc.RangeQuery(context.Background(), &pb.RangeQueryRequest{Box: box})
	if err != nil {
		t.Fatalf("RangeQuery (all): %v", err)
	}
	if len(all.Points) != 3 {
		t.Fatalf("got %d points, want 3", len(all.Points))
	}

	filtered, err := svc.RangeQuery(context.Background(), &pb.RangeQueryRequest{Box: box, Gene: "Cygb"})
	if err != nil {
		t.Fatalf("RangeQuery (Cygb): %v", err)
	}
	if len(filtered.Points) != 1 || filtered.Points[0].Id != 1 {
		t.Fatalf("gene filter mismatch: got %+v", filtered.Points)
	}
}
