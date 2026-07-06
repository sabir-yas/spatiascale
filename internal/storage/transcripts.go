// Package storage loads external data sources into spatial.Point form.
package storage

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/yaseer/spatialscale/internal/spatial"
)

// TranscriptGridMicrons is the bucket size used to decide whether two
// transcripts are "at the same position." Xenium coordinates are in
// microns; 1 micron keeps aggregation close to raw precision while merging
// floating-point noise.
const TranscriptGridMicrons = 1.0

// transcriptKey identifies one (bucketed position, gene) group.
type transcriptKey struct {
	x, y int64
	gene string
}

func roundToGrid(v float64) int64 {
	return int64(math.Round(v / TranscriptGridMicrons))
}

// LoadTranscripts streams a Xenium-style transcripts CSV, keeps only real
// gene detections (is_gene=true, codeword_category=predesigned_gene),
// aggregates counts per (bucketed x, bucketed y, gene), and returns one
// Point per group — Payload encodes "<gene_name>:<count>" — along with the
// bounding box covering all points.
func LoadTranscripts(r io.Reader) ([]spatial.Point, spatial.BoundingBox, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	cr := csv.NewReader(br)
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		return nil, spatial.BoundingBox{}, fmt.Errorf("read header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[name] = i
	}
	for _, want := range []string{"feature_name", "x_location", "y_location", "is_gene", "codeword_category"} {
		if _, ok := col[want]; !ok {
			return nil, spatial.BoundingBox{}, fmt.Errorf("csv missing required column %q", want)
		}
	}
	geneCol, xCol, yCol := col["feature_name"], col["x_location"], col["y_location"]
	isGeneCol, categoryCol := col["is_gene"], col["codeword_category"]

	counts := make(map[transcriptKey]int)
	bounds := spatial.BoundingBox{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, spatial.BoundingBox{}, fmt.Errorf("read row: %w", err)
		}

		if rec[isGeneCol] != "true" || rec[categoryCol] != "predesigned_gene" {
			continue
		}

		x, err := strconv.ParseFloat(rec[xCol], 64)
		if err != nil {
			return nil, spatial.BoundingBox{}, fmt.Errorf("parse x_location %q: %w", rec[xCol], err)
		}
		y, err := strconv.ParseFloat(rec[yCol], 64)
		if err != nil {
			return nil, spatial.BoundingBox{}, fmt.Errorf("parse y_location %q: %w", rec[yCol], err)
		}

		key := transcriptKey{x: roundToGrid(x), y: roundToGrid(y), gene: rec[geneCol]}
		counts[key]++

		px, py := float64(key.x)*TranscriptGridMicrons, float64(key.y)*TranscriptGridMicrons
		bounds.MinX, bounds.MaxX = math.Min(bounds.MinX, px), math.Max(bounds.MaxX, px)
		bounds.MinY, bounds.MaxY = math.Min(bounds.MinY, py), math.Max(bounds.MaxY, py)
	}

	pts := make([]spatial.Point, 0, len(counts))
	var id uint64
	for k, count := range counts {
		id++
		pts = append(pts, spatial.Point{
			ID:      id,
			X:       float64(k.x) * TranscriptGridMicrons,
			Y:       float64(k.y) * TranscriptGridMicrons,
			Payload: []byte(fmt.Sprintf("%s:%d", k.gene, count)),
		})
	}
	return pts, bounds, nil
}
