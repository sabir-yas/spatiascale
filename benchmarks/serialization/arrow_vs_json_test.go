package serialization

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/apache/arrow/go/v18/arrow"
	"github.com/apache/arrow/go/v18/arrow/array"
	"github.com/apache/arrow/go/v18/arrow/ipc"
	"github.com/apache/arrow/go/v18/arrow/memory"
	"github.com/yaseer/spatialscale/internal/spatial"
	"github.com/yaseer/spatialscale/internal/storage"
)

// realTranscriptsCSV is the sample slice checked into testdata/, used to
// drive benchmarks on real transcript payloads (variable-length gene name +
// count strings) instead of only synthetic fixed-shape points.
const realTranscriptsCSV = "../../testdata/transcripts_sample.csv"

// loadRealPoints loads aggregated transcript points for benchmarking.
// Skips the calling benchmark if the sample CSV isn't present.
func loadRealPoints(b *testing.B) []spatial.Point {
	b.Helper()
	f, err := os.Open(realTranscriptsCSV)
	if err != nil {
		b.Skipf("real transcript sample not available: %v", err)
	}
	defer f.Close()
	pts, _, err := storage.LoadTranscripts(f)
	if err != nil {
		b.Fatalf("LoadTranscripts: %v", err)
	}
	return pts
}

// pointSchema is the Arrow schema for a batch of Points.
// Columnar means: all IDs stored together, all Xs together, all Ys together.
// This is why Arrow is fast — to read 10,000 X values you access one
// contiguous memory block, not 10,000 scattered struct fields.
var pointSchema = arrow.NewSchema([]arrow.Field{
	{Name: "id", Type: arrow.PrimitiveTypes.Uint64},
	{Name: "x", Type: arrow.PrimitiveTypes.Float64},
	{Name: "y", Type: arrow.PrimitiveTypes.Float64},
	{Name: "payload", Type: arrow.BinaryTypes.Binary},
}, nil)

// makePoints generates n synthetic points for benchmarking.
func makePoints(n int) []spatial.Point {
	pts := make([]spatial.Point, n)
	for i := range pts {
		pts[i] = spatial.Point{
			ID: uint64(i),
			X:  float64(i%360) - 180,
			Y:  float64(i%180) - 90,
		}
	}
	return pts
}

// encodeArrow serializes a []Point into Arrow IPC binary format.
// IPC (Inter-Process Communication) is Arrow's wire format —
// the same bytes can be zero-copy read in Python, Rust, Java, etc.
func encodeArrow(pts []spatial.Point) ([]byte, error) {
	alloc := memory.NewGoAllocator()

	idBuilder := array.NewUint64Builder(alloc)
	xBuilder := array.NewFloat64Builder(alloc)
	yBuilder := array.NewFloat64Builder(alloc)
	payloadBuilder := array.NewBinaryBuilder(alloc, arrow.BinaryTypes.Binary)
	defer idBuilder.Release()
	defer xBuilder.Release()
	defer yBuilder.Release()
	defer payloadBuilder.Release()

	for _, p := range pts {
		idBuilder.Append(p.ID)
		xBuilder.Append(p.X)
		yBuilder.Append(p.Y)
		payloadBuilder.Append(p.Payload)
	}

	idArr := idBuilder.NewArray()
	xArr := xBuilder.NewArray()
	yArr := yBuilder.NewArray()
	payloadArr := payloadBuilder.NewArray()
	defer idArr.Release()
	defer xArr.Release()
	defer yArr.Release()
	defer payloadArr.Release()

	rec := array.NewRecord(pointSchema, []arrow.Array{idArr, xArr, yArr, payloadArr}, int64(len(pts)))
	defer rec.Release()

	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(pointSchema))
	if err := w.Write(rec); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeArrow deserializes Arrow IPC bytes back into []Point.
func decodeArrow(data []byte) ([]spatial.Point, error) {
	r, err := ipc.NewReader(bytes.NewReader(data), ipc.WithSchema(pointSchema))
	if err != nil {
		return nil, err
	}
	defer r.Release()

	var pts []spatial.Point
	for r.Next() {
		rec := r.Record()
		ids := rec.Column(0).(*array.Uint64)
		xs := rec.Column(1).(*array.Float64)
		ys := rec.Column(2).(*array.Float64)
		payloads := rec.Column(3).(*array.Binary)
		for i := 0; i < int(rec.NumRows()); i++ {
			pts = append(pts, spatial.Point{
				ID:      ids.Value(i),
				X:       xs.Value(i),
				Y:       ys.Value(i),
				Payload: payloads.Value(i),
			})
		}
	}
	return pts, nil
}

// encodeJSON serializes []Point with the standard library JSON encoder.
func encodeJSON(pts []spatial.Point) ([]byte, error) {
	return json.Marshal(pts)
}

// decodeJSON deserializes JSON bytes back into []Point.
func decodeJSON(data []byte) ([]spatial.Point, error) {
	var pts []spatial.Point
	return pts, json.Unmarshal(data, &pts)
}

// --- Correctness tests (run with go test, not go test -bench) ---

func TestArrowRoundTrip(t *testing.T) {
	original := makePoints(100)
	encoded, err := encodeArrow(original)
	if err != nil {
		t.Fatalf("encodeArrow: %v", err)
	}
	decoded, err := decodeArrow(encoded)
	if err != nil {
		t.Fatalf("decodeArrow: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("got %d points, want %d", len(decoded), len(original))
	}
	for i := range original {
		if original[i].ID != decoded[i].ID || original[i].X != decoded[i].X || original[i].Y != decoded[i].Y ||
			!bytes.Equal(original[i].Payload, decoded[i].Payload) {
			t.Errorf("point[%d]: got %+v, want %+v", i, decoded[i], original[i])
		}
	}
}

func TestJSONRoundTrip(t *testing.T) {
	original := makePoints(100)
	encoded, err := encodeJSON(original)
	if err != nil {
		t.Fatalf("encodeJSON: %v", err)
	}
	decoded, err := decodeJSON(encoded)
	if err != nil {
		t.Fatalf("decodeJSON: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("got %d points, want %d", len(decoded), len(original))
	}
}

// --- Benchmarks ---
// Run with: go test -bench=. -benchmem ./benchmarks/serialization/
// The -benchmem flag shows memory allocations per operation —
// Arrow wins not just on speed but on allocations too.

func BenchmarkArrowEncode1K(b *testing.B) {
	pts := makePoints(1_000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := encodeArrow(pts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkArrowEncode10K(b *testing.B) {
	pts := makePoints(10_000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := encodeArrow(pts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONEncode1K(b *testing.B) {
	pts := makePoints(1_000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := encodeJSON(pts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONEncode10K(b *testing.B) {
	pts := makePoints(10_000)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := encodeJSON(pts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkArrowDecode1K(b *testing.B) {
	pts := makePoints(1_000)
	data, _ := encodeArrow(pts)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := decodeArrow(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkArrowDecode10K(b *testing.B) {
	pts := makePoints(10_000)
	data, _ := encodeArrow(pts)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := decodeArrow(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONDecode1K(b *testing.B) {
	pts := makePoints(1_000)
	data, _ := encodeJSON(pts)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := decodeJSON(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONDecode10K(b *testing.B) {
	pts := makePoints(10_000)
	data, _ := encodeJSON(pts)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := decodeJSON(data); err != nil {
			b.Fatal(err)
		}
	}
}

// --- Real transcript data benchmarks ---
// Same encode/decode paths, but driven by aggregated real transcript points
// (gene name + count payload) instead of synthetic fixed-shape points.
// Run with: go test -bench=Real -benchmem ./benchmarks/serialization/

func BenchmarkArrowEncodeReal(b *testing.B) {
	pts := loadRealPoints(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := encodeArrow(pts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONEncodeReal(b *testing.B) {
	pts := loadRealPoints(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := encodeJSON(pts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkArrowDecodeReal(b *testing.B) {
	pts := loadRealPoints(b)
	data, err := encodeArrow(pts)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := decodeArrow(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONDecodeReal(b *testing.B) {
	pts := loadRealPoints(b)
	data, err := encodeJSON(pts)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := decodeJSON(data); err != nil {
			b.Fatal(err)
		}
	}
}
