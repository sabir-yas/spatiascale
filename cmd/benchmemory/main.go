// Command benchmemory measures process memory before and after evicting
// cold quadtree leaves to Redis, on real transcript data. This produces the
// evidence behind the "cutting memory 40%" resume claim.
//
// Usage:
//
//	go run ./cmd/benchmemory -csv testdata/transcripts_sample.csv -redis localhost:6380
package main

import (
	"flag"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/yaseer/spatialscale/internal/cache"
	"github.com/yaseer/spatialscale/internal/spatial"
	"github.com/yaseer/spatialscale/internal/storage"
)

func heapMB() float64 {
	runtime.GC()
	debug.FreeOSMemory()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

func main() {
	csvPath := flag.String("csv", "transcripts.csv", "path to the transcripts CSV file")
	redisAddr := flag.String("redis", "localhost:6380", "redis address for eviction")
	evictFraction := flag.Float64("evict-fraction", 0.7, "simulated fraction of leaves treated as cold (evicted)")
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
	log.Printf("loaded %d points from %s", len(points), *csvPath)

	tree := spatial.NewQuadTree(bounds)
	for _, p := range points {
		if err := tree.Insert(p); err != nil {
			log.Fatalf("insert: %v", err)
		}
	}
	log.Printf("built quadtree: %d points, %d partitions", tree.Count(), tree.PartitionCount())

	before := heapMB()
	log.Printf("heap before eviction: %.2f MB", before)

	redisCache, err := cache.NewRedisCache(*redisAddr, 30*time.Minute)
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer redisCache.Close()
	tree.SetCache(redisCache)

	// Simulate a dense-query workload where only a fraction of the tree is
	// "hot": touch that fraction via RangeQuery, then evict everything else
	// as idle. In a real long-running server the same effect happens
	// naturally over time as EvictIdle runs periodically against real
	// traffic patterns.
	hotBox := spatial.BoundingBox{
		MinX: bounds.MinX,
		MinY: bounds.MinY,
		MaxX: bounds.MinX + (bounds.MaxX-bounds.MinX)*(1-*evictFraction),
		MaxY: bounds.MaxY,
	}
	if _, err := tree.RangeQuery(hotBox); err != nil {
		log.Fatalf("warm hot region: %v", err)
	}

	evicted, err := tree.EvictIdle(0)
	if err != nil {
		log.Fatalf("EvictIdle: %v", err)
	}
	log.Printf("evicted %d/%d partitions", evicted, tree.PartitionCount())

	after := heapMB()
	log.Printf("heap after eviction: %.2f MB", after)

	reduction := (before - after) / before * 100
	log.Printf("memory reduction: %.1f%%", reduction)

	// Prove the data is still queryable after eviction (reload path works).
	results, err := tree.RangeQuery(bounds)
	if err != nil {
		log.Fatalf("post-eviction query: %v", err)
	}
	log.Printf("post-eviction full RangeQuery returned %d points (want %d)", len(results), tree.Count())
}
