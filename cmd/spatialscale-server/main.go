// Command spatialscale-server loads a transcripts CSV into a QuadTree once
// at startup and serves it over the SpatialQuery gRPC service.
//
// Usage:
//
//	go run ./cmd/spatialscale-server -csv transcripts.csv -addr :50051
package main

import (
	"flag"
	"log"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/yaseer/spatialscale/internal/query"
	"github.com/yaseer/spatialscale/internal/spatial"
	"github.com/yaseer/spatialscale/internal/storage"
	pb "github.com/yaseer/spatialscale/proto/spatialscalepb"
)

func main() {
	csvPath := flag.String("csv", "transcripts.csv", "path to the transcripts CSV file")
	addr := flag.String("addr", ":50051", "gRPC listen address")
	flag.Parse()

	f, err := os.Open(*csvPath)
	if err != nil {
		log.Fatalf("open csv: %v", err)
	}
	points, bounds, err := storage.LoadTranscripts(f)
	f.Close()
	if err != nil {
		log.Fatalf("load transcripts: %v", err)
	}
	log.Printf("aggregated %d unique (position, gene) points, bounds=%+v", len(points), bounds)

	tree := spatial.NewQuadTree(bounds)
	for _, p := range points {
		if err := tree.Insert(p); err != nil {
			log.Fatalf("insert point %+v: %v", p, err)
		}
	}
	log.Printf("loaded %d points into quadtree (%d partitions)", tree.Count(), tree.PartitionCount())

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *addr, err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterSpatialQueryServer(grpcServer, query.NewService(tree))

	log.Printf("spatialscale-server listening on %s", *addr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
