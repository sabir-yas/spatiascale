// Command spatialscale-server loads a transcripts CSV into a QuadTree once
// at startup and serves it over the SpatialQuery gRPC service.
//
// Usage:
//
//	go run ./cmd/spatialscale-server -csv transcripts.csv -addr :50051
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/grpc"

	"github.com/yaseer/spatialscale/internal/query"
	"github.com/yaseer/spatialscale/internal/spatial"
	"github.com/yaseer/spatialscale/internal/storage"
	pb "github.com/yaseer/spatialscale/proto/spatialscalepb"
)

// openCSV opens a local file path or, if csvPath starts with "s3://", streams
// the object from S3 (bucket/key credentials come from the pod's IAM role).
func openCSV(ctx context.Context, csvPath string) (io.ReadCloser, error) {
	rest, ok := strings.CutPrefix(csvPath, "s3://")
	if !ok {
		return os.Open(csvPath)
	}
	bucket, key, _ := strings.Cut(rest, "/")

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s3.NewFromConfig(cfg).GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func main() {
	csvPath := flag.String("csv", "transcripts.csv", "path to the transcripts CSV file, or s3://bucket/key")
	addr := flag.String("addr", ":50051", "gRPC listen address")
	flag.Parse()

	f, err := openCSV(context.Background(), *csvPath)
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
