// Command benchclient drives concurrent RangeQuery traffic against a running
// spatialscale-server and reports p50/p99 round-trip latency — the real
// evidence behind the "sub-10ms p99" resume claim, measured end-to-end
// (network + serialization + tree lookup), not just tree lookup alone.
//
// Usage:
//
//	go run ./cmd/benchclient -addr localhost:50051 -concurrency 16 -requests 2000 \
//	    -minx 8 -miny 5134 -maxx 750 -maxy 8662 -box-frac 0.05
package main

import (
	"context"
	"flag"
	"log"
	"math/rand"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/yaseer/spatialscale/internal/metrics"
	pb "github.com/yaseer/spatialscale/proto/spatialscalepb"
)

func main() {
	addr := flag.String("addr", "localhost:50051", "spatialscale-server gRPC address")
	concurrency := flag.Int("concurrency", 16, "number of concurrent client goroutines")
	requests := flag.Int("requests", 2000, "total number of RangeQuery calls to make")
	minX := flag.Float64("minx", 0, "data bounds min X (query boxes are randomly placed within these bounds)")
	minY := flag.Float64("miny", 0, "data bounds min Y")
	maxX := flag.Float64("maxx", 100, "data bounds max X")
	maxY := flag.Float64("maxy", 100, "data bounds max Y")
	boxFrac := flag.Float64("box-frac", 0.05, "query box size as a fraction of the full data extent (small = realistic region-of-interest query)")
	gene := flag.String("gene", "", "optional gene name filter, empty = all genes")
	flag.Parse()

	conn, err := grpc.NewClient(*addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<20)),
	)
	if err != nil {
		log.Fatalf("dial %s: %v", *addr, err)
	}
	defer conn.Close()
	client := pb.NewSpatialQueryClient(conn)

	rec := metrics.NewLatencyRecorder()
	width := (*maxX - *minX) * *boxFrac
	height := (*maxY - *minY) * *boxFrac

	var wg sync.WaitGroup
	reqCh := make(chan int, *requests)
	for i := 0; i < *requests; i++ {
		reqCh <- i
	}
	close(reqCh)

	errCh := make(chan error, *requests)
	start := time.Now()
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for range reqCh {
				x := *minX + rng.Float64()*(*maxX-*minX-width)
				y := *minY + rng.Float64()*(*maxY-*minY-height)
				box := &pb.BoundingBox{MinX: x, MinY: y, MaxX: x + width, MaxY: y + height}

				callStart := time.Now()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := client.RangeQuery(ctx, &pb.RangeQueryRequest{Box: box, Gene: *gene})
				cancel()
				rec.Record(time.Since(callStart))
				if err != nil {
					errCh <- err
				}
			}
		}(int64(w))
	}
	wg.Wait()
	close(errCh)
	elapsed := time.Since(start)

	errCount := len(errCh)
	log.Printf("completed %d requests (%d concurrency) in %v — %.1f req/s", *requests, *concurrency, elapsed, float64(*requests)/elapsed.Seconds())
	log.Printf("errors: %d", errCount)
	log.Printf("p50: %v", rec.Percentile(50))
	log.Printf("p90: %v", rec.Percentile(90))
	log.Printf("p99: %v", rec.Percentile(99))
}
