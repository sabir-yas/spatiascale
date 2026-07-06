# SpatiaScale Architecture

Reference for building a system diagram (e.g. in Figma). Describes components, data flow, and deployment topology as actually built — not aspirational.

## Components

| Component | What it is | Where it lives |
|---|---|---|
| `transcripts.csv` | Raw input data, ~155M rows, real Xenium spatial transcriptomics output | S3 bucket (Phase B) / local disk (Phase A) |
| Loader / aggregator | Parses CSV, filters noise rows, aggregates to `(x, y, gene)` points on a 1-micron grid | `internal/storage`, invoked by `cmd/datagen` and `cmd/spatialscale-server` at startup |
| QuadTree | In-memory spatial index: recursive quadrant splitting, per-node `RWMutex`, atomic counts | `internal/spatial`, embedded in the server process |
| Redis cache | Cold-partition eviction target — idle quadtree leaves are serialized (gob) and evicted to free heap, reloaded on next touch | ElastiCache (Phase B) / local Docker Redis on `:6380` (Phase A) |
| gRPC service | `SpatialQuery.RangeQuery(bounding box, optional gene filter)` — the only RPC | `internal/query` (service impl), `proto/spatialscalepb` (generated stubs) |
| Server process | Loads CSV once at startup, builds quadtree, serves gRPC | `cmd/spatialscale-server`, containerized, runs as EKS pods |
| Load balancer | Routes gRPC traffic to server pods | AWS NLB (Phase B), created via a Kubernetes `type: LoadBalancer` Service, not Terraform |
| Benchmark client | Concurrent load generator, measures p50/p90/p99 round-trip latency | `cmd/benchclient`, run externally against the NLB/server address |
| Metrics recorder | Thread-safe latency sample recorder + percentile calc, wired into the RPC handler | `internal/metrics` |

## Data flow (request path)

```
Researcher / benchclient
        |
        v
   AWS NLB  (Kubernetes LoadBalancer Service)
        |
        v
  spatialscale-server pod(s) on EKS
        |
        +--> gRPC SpatialQuery.RangeQuery(box, gene)
        |
        v
  internal/query.Service
        |
        +--> QuadTree.RangeQuery(box)  -- walks only overlapping branches
        |         |
        |         +--> if leaf evicted: reload from Redis (ElastiCache)
        |
        +--> filter by gene payload prefix ("<gene>:<count>")
        |
        v
   gRPC response (protobuf Points)
```

## Data flow (ingestion path, one-time at server startup / batch job)

```
transcripts.csv (S3 or local)
        |
        v
storage.LoadTranscripts
        |
        +--> filter: is_gene=true AND codeword_category=predesigned_gene
        +--> bucket x,y to 1-micron grid
        +--> aggregate by (x, y, gene) -> count
        |
        v
QuadTree.Insert (one point per aggregated group)
        |
        v
In-memory quadtree, ready to serve RangeQuery
```

## Deployment topology (Phase B — AWS)

```
                         Internet
                             |
                        AWS NLB
                             |
        -----------------------------------------
        |            EKS Cluster                |
        |  VPC 10.42.0.0/16, 2 public subnets    |
        |  (different AZs, no NAT gateway)       |
        |                                        |
        |   Node group: 2x t3.small (spot)       |
        |     +-- spatialscale-server pod        |
        |     +-- spatialscale-server pod        |
        -----------------------------------------
                 |                    |
                 v                    v
        ElastiCache (Redis)      S3 bucket
        cache.t3.micro           spatiascale-points-*
        (cold partition          (transcript data,
         eviction target)         point storage)
```

## Deployment topology (Phase A — local + HPC, already validated)

```
Local machine / HPC node
  |
  +-- cmd/spatialscale-server (plain process, no k8s)
  |       |
  |       +-- QuadTree (in-memory)
  |       +-- Docker Redis on localhost:6380 (eviction target)
  |
  +-- cmd/benchclient (load generator, localhost gRPC)
  |
  +-- cmd/datagen (batch loader, used standalone and via Slurm sbatch
                    for full-scale 155M-row ingestion on Alpine HPC cluster)
```

## Key architectural decisions worth showing on the diagram

- **No pagination/streaming on RangeQuery** — queries are intentionally region-scoped (small bounding boxes), not full-dataset dumps. This is why there's no separate "results paging" component.
- **Eviction is push-based on idle time**, not LRU-capacity-based — `EvictIdle(idleFor)` walks the tree and evicts any leaf untouched longer than a threshold. Reload is transparent (lazy, on next touch).
- **Arrow is not on the network path.** The "5x serialization speedup" claim is proven by a standalone benchmark (`benchmarks/serialization`) comparing Arrow vs JSON encode/decode — the actual gRPC wire format is plain protobuf. Don't draw Arrow as sitting between the server and NLB.
- **NLB is not a Terraform resource** — it's created implicitly by Kubernetes when the server's Service is type `LoadBalancer`. Terraform only provisions the EKS cluster/nodes/networking/Redis/S3 that the Service needs to exist on top of.

## Real measured numbers (for annotating the diagram, Phase A)

- 148,583,090 aggregated points, 6,168,484 quadtree partitions (full 155M-row real dataset, Alpine HPC run)
- 76.3% heap reduction from Redis eviction (480K-point sample: 68.96 MB -> 16.31 MB)
- p50 0.52ms / p90 0.68ms / p99 4.0ms for realistic small region-of-interest queries
- Arrow encode 3.1x faster, decode 11.3x faster than JSON (real transcript data)
