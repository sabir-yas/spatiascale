# SpatiaScale

A cloud-native distributed spatial query engine in Go, built to serve real-time
region-of-interest queries over large spatial-omics datasets (150M+ points).

Indexes transcript-level spatial transcriptomics data (Xenium-style output) in
a concurrent quadtree, serves range queries over gRPC, evicts cold partitions
to Redis under memory pressure, and runs on AWS EKS behind an NLB.

## Why

Interactive spatial querying of a whole tissue slide's transcriptome in real
time — e.g. "give me gene counts in this region of cortex" — without loading
and filtering 150M+ rows in pandas every time. Other patterns this enables:
differential expression between two regions (two `RangeQuery` calls, compare
per-gene counts), multi-sample comparison (same bounding box across
tissue-section quadtrees), and density/hotspot detection (quadtree split
depth as a cheap proxy for transcript density).

## Architecture

```
                    ┌─────────────┐
   researcher  ──▶  │  NLB (AWS)  │
   (RangeQuery)     └──────┬──────┘
                            │ gRPC
                     ┌──────▼──────┐      ┌───────────────┐
                     │ spatialscale │◀────▶│ Redis          │
                     │   -server    │      │ (ElastiCache)  │
                     │ (quadtree)   │      │ evicted        │
                     └──────┬───────┘      │ partitions     │
                            │                └───────────────┘
                     ┌──────▼───────┐
                     │  S3 / local  │
                     │ transcripts  │
                     │    .csv      │
                     └──────────────┘
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full component breakdown,
data-flow diagrams, and key design decisions. Key components:

- **`internal/spatial`** — concurrent quadtree (per-node `RWMutex`, atomic
  counts), the core index.
- **`internal/storage`** — CSV loader: filters noise rows, aggregates
  `(x, y, gene)` into 1-micron-bucketed points.
- **`internal/query`** — gRPC service wrapping the quadtree with bounding-box
  + optional gene-name filtering.
- **`internal/cache`** — Redis-backed eviction for idle quadtree partitions.
- **`internal/metrics`** — latency percentile recorder (p50/p90/p99), wired
  into every RPC.
- **`cmd/spatialscale-server`** — the gRPC server binary.
- **`cmd/datagen`**, **`cmd/benchclient`**, **`cmd/benchmemory`** — loader
  CLI, load-generation client, and memory/eviction benchmark.
- **`benchmarks/serialization`** — Arrow vs JSON encode/decode benchmarks on
  real transcript data.
- **`deploy/terraform`**, **`deploy/helm`** — AWS footprint (EKS,
  ElastiCache, S3) and Kubernetes manifests.

## Real measured numbers

All numbers below come from real data (a real 155M-row Xenium transcript
file) and real infrastructure — no synthetic benchmarks.

**Serialization (Arrow vs JSON)** — `benchmarks/serialization`, ~480,982
aggregated real transcript points:
- Encode: 155.3ms (JSON) vs 49.3ms (Arrow) = **3.1x**
- Decode: 618.6ms (JSON) vs 54.5ms (Arrow) = **11.3x** (481,024 allocs vs 133)

**Scale** — full-file HPC ingestion run (CU Boulder Alpine cluster, Slurm,
18 CPUs / 64GB mem), the complete real 155M-row `transcripts.csv`, no
sampling:
- **148,583,090** aggregated `(position, gene)` points
- **6,168,484** quadtree partitions
- Wall clock **6m42s**, peak RSS **50.57 GB**, exit 0, no swaps

**Memory reduction (Redis eviction)** — `cmd/benchmemory`, 480,982-point
sample, 18,817 partitions: warmed 30% of the region via `RangeQuery`, then
evicted the remaining 15,005 cold partitions to Redis:
- Heap: 68.96 MB → 16.31 MB = **76.3% reduction**
- Full-range `RangeQuery` after eviction still returned all 480,982 points
  (reload path verified at scale)

**p99 latency, local** — `cmd/benchclient`, 16 goroutines, 2000-5000
requests, localhost gRPC, 480,982-point sample:
- Small region-of-interest box (`-box-frac 0.01`): **p50=0.52ms, p90=0.68ms,
  p99=4.0ms**
- Large box (`-box-frac 0.05`, tens of thousands of points returned): p99
  balloons to ~33ms — dominated by response marshaling, not tree-lookup cost

**p99 latency, real AWS EKS** — same benchclient, run from inside the
cluster against the Service's internal DNS (isolates service latency from
client-to-region internet distance), 2000 requests, 16 concurrency:
- **p50=1.11ms, p90=2.54ms, p99=7.77ms, 0 errors, 10,266 req/s**
- (An external client hitting the public NLB from outside AWS saw
  p50=42.2ms/p99=5.0s — that gap was pure client-to-`us-east-1` internet
  round-trip distance, not a service or infrastructure problem.)

**Failover** — 2-replica Deployment on EKS, sustained load
(2,000,000 requests, 8 concurrency, ~4 min) with one server pod killed
mid-run:
- Kubernetes had a replacement pod `Running` within **11 seconds**
- **0 errors out of 2,000,000 requests, p99=3.19ms** — zero client-visible
  impact from the pod kill

## Running locally

```
docker run -d --name spatiascale-redis -p 6380:6379 redis:7-alpine

go run ./cmd/spatialscale-server -csv testdata/transcripts_sample.csv -addr :50051

go run ./cmd/benchclient -addr localhost:50051 \
  -minx 8 -miny 5134 -maxx 750 -maxy 8662 -box-frac 0.01
```

Run tests with `go test ./...` (skip `-race` — the MinGW/cgo toolchain on
this project's dev machine has conflicting Windows header definitions,
unrelated to project code).

## Status

Phase A (local + HPC correctness/scale proof) and the core of Phase B (AWS
EKS deployment, real NLB, failover test) are complete.
