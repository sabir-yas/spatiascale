// Package query implements the SpatialQuery gRPC service, answering range
// queries against a QuadTree loaded once at startup.
package query

import (
	"bytes"
	"context"

	"github.com/yaseer/spatialscale/internal/spatial"
	pb "github.com/yaseer/spatialscale/proto/spatialscalepb"
)

// Service implements pb.SpatialQueryServer over a preloaded QuadTree.
type Service struct {
	pb.UnimplementedSpatialQueryServer
	tree *spatial.QuadTree
}

func NewService(tree *spatial.QuadTree) *Service {
	return &Service{tree: tree}
}

func (s *Service) RangeQuery(ctx context.Context, req *pb.RangeQueryRequest) (*pb.RangeQueryResponse, error) {
	box := spatial.BoundingBox{
		MinX: req.Box.MinX, MinY: req.Box.MinY,
		MaxX: req.Box.MaxX, MaxY: req.Box.MaxY,
	}

	points, err := s.tree.RangeQuery(box)
	if err != nil {
		return nil, err
	}

	resp := &pb.RangeQueryResponse{Points: make([]*pb.Point, 0, len(points))}
	geneFilter := []byte(req.Gene)
	for _, p := range points {
		if req.Gene != "" && !matchesGene(p.Payload, geneFilter) {
			continue
		}
		resp.Points = append(resp.Points, &pb.Point{
			Id:      p.ID,
			X:       p.X,
			Y:       p.Y,
			Payload: p.Payload,
		})
	}
	return resp, nil
}

// matchesGene checks whether payload (format "<gene>:<count>") starts with
// "<gene>:" for the requested gene name.
func matchesGene(payload, gene []byte) bool {
	return bytes.HasPrefix(payload, gene) &&
		len(payload) > len(gene) && payload[len(gene)] == ':'
}
