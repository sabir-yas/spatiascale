// Command datagen streams a Xenium-style transcripts.csv file, filters out
// non-gene control/noise rows, aggregates transcript counts by (rounded
// position, gene), and loads the result into a QuadTree.
//
// Usage:
//
//	go run ./cmd/datagen -csv transcripts.csv
package main

import (
	"flag"
	"log"
	"os"

	"github.com/yaseer/spatialscale/internal/spatial"
	"github.com/yaseer/spatialscale/internal/storage"
)

func main() {
	csvPath := flag.String("csv", "transcripts.csv", "path to the transcripts CSV file")
	flag.Parse()

	f, err := os.Open(*csvPath)
	if err != nil {
		log.Fatalf("open csv: %v", err)
	}
	defer f.Close()

	points, bounds, err := storage.LoadTranscripts(f)
	if err != nil {
		log.Fatalf("load transcripts: %v", err)
	}
	log.Printf("aggregated %d unique (position, gene) points, bounds=%+v", len(points), bounds)

	qt := spatial.NewQuadTree(bounds)
	for _, p := range points {
		if err := qt.Insert(p); err != nil {
			log.Fatalf("insert point %+v: %v", p, err)
		}
	}

	log.Printf("loaded %d points into quadtree (%d partitions)", qt.Count(), qt.PartitionCount())
}
