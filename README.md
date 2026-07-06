# SpatiaScale

A cloud-native distributed spatial query engine in Go, built to serve real-time
region-of-interest queries over large spatial-omics datasets (150M+ points).

Indexes transcript-level spatial transcriptomics data (Xenium-style output) in
a concurrent quadtree, serves range queries over gRPC, evicts cold partitions
to Redis under memory pressure, and runs on AWS EKS behind an NLB.

## Why

Interactive spatial querying of a whole tissue slide's transcriptome in real
time — e.g. "give me gene counts in this region of cortex" — without loading
and filtering 150M+ rows in pandas every time. See the Research use-case
narrative in [CLAUDE.md](CLAUDE.md) for the full motivation and query
patterns (region comparison, multi-sample comparison, density/hotspot
detection).

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
data-flow diagrams, and key design decisions.

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
file) and real infrastructure — no synthetic benchmarks. Full methodology
and raw output for each is in [CLAUDE.md](CLAUDE.md).

| Claim | Result |
|---|---|
| Serialization speedup (Arrow vs JSON) | 3.1x encode, 11.3x decode, on ~480K real points |
| Scale | 148,583,090 aggregated points / 6,168,484 partitions, full 155M-row file, one quadtree instance |
| Memory reduction (Redis eviction) | 76.3% heap reduction (68.96MB → 16.31MB) on the 480K-point sample |
| p99 latency, local | 4.0ms (region-scoped query, realistic box size) |
| p99 latency, real AWS EKS (in-cluster) | 7.77ms, 0 errors, 10,266 req/s |
| Failover (pod killed mid-load-test) | 0 errors across 2,000,000 requests, replacement pod ready in 11s |

## Running locally

```
docker run -d --name spatiascale-redis -p 6380:6379 redis:7-alpine

go run ./cmd/spatialscale-server -csv testdata/transcripts_sample.csv -addr :50051

go run ./cmd/benchclient -addr localhost:50051 \
  -minx 8 -miny 5134 -maxx 750 -maxy 8662 -box-frac 0.01
```

Run tests with `go test ./...` (skip `-race` — see Known gotchas in
[CLAUDE.md](CLAUDE.md), it doesn't work on this project's dev machine).

## Status

Phase A (local + HPC correctness/scale proof) and the core of Phase B (AWS
EKS deployment, real NLB, failover test) are complete. See
[CLAUDE.md](CLAUDE.md) for the full phased plan, current status, and
in-progress work.
