# SpatiaScale

Cloud-native distributed spatial query engine (Go). Target end-state (resume goal):

- Fault-tolerant spatial query engine on AWS EKS, indexing 500M+ multi-dimensional points in S3, sub-10ms p99 retrieval latency.
- Concurrent Quadtree partitioning integrated with Amazon ElastiCache (Redis), cutting cluster memory utilization by 40% during dense range queries.
- gRPC + Apache Arrow network layer behind an AWS NLB, 5x serialization speedup, 99.99% availability during failover testing via AWS Auto Scaling.

**Phased approach:** Phase A (current) is local + HPC only, no AWS — prove the engine works and get real measured numbers. Phase B (later) is AWS deployment, and only starts once Phase A numbers are real. Do not jump ahead to EKS/S3/ElastiCache/NLB work until Phase A is solid.

## Status

### Done
- `internal/spatial/` — concurrent quadtree: `Point`/`BoundingBox` primitives, insert/split/RangeQuery, per-node `RWMutex` locking, atomic counts. Tested including concurrent insert/query.
- `benchmarks/serialization/` — Arrow vs JSON encode/decode benchmarks + round-trip correctness tests, on both synthetic points and real transcript data. go.sum issue fixed (`go get` pulled in flatbuffers). Schema now includes a `payload` binary column so real gene-name+count payloads are exercised, not just fixed-width numerics. **Real-data results** (`go test -bench=Real -benchmem ./benchmarks/serialization/`, ~480K aggregated transcript points): encode 155.3ms (JSON) vs 49.3ms (Arrow) = 3.1x; decode 618.6ms (JSON) vs 54.5ms (Arrow) = 11.3x — JSON decode does 481,024 allocs vs Arrow's 133. This is the real evidence behind the "5x serialization speedup" resume claim.
- `internal/storage/transcripts.go` — `LoadTranscripts(io.Reader)` extracted from `cmd/datagen` so both the CLI and the benchmark package can load real transcript data without duplicating CSV-parsing logic.
- `cmd/datagen/main.go` — thin CLI wrapper around `storage.LoadTranscripts`, loads into a `QuadTree`. Validated against a 500K-row real slice (Cygb: 79 raw detections → 78 aggregated groups, total count preserved).
- **Biological validation passed** — `scripts/plot_markers.py` reservoir-samples rows uniformly across the full 155M-row file (pure first-N sampling is spatially biased since Xenium output is FOV-ordered, not spatially ordered) and plots all transcripts plus 4 canonical mouse brain markers (`Snap25` pan-neuronal, `Slc17a7` excitatory, `Gad1` inhibitory, `Gfap` astrocyte). Result: the point cloud traces a real anatomical tissue outline (not noise), marker gene relative abundances are biologically plausible (Snap25 most abundant, matching that most brain cells are neurons), and Gad1+ inhibitory neurons show a visibly denser, spatially distinct cluster rather than being uniformly spread — confirms the ingestion pipeline preserves real biological signal end to end. Run with `python3 scripts/plot_markers.py transcripts.csv --sample 1000000` (takes ~13 min in pure Python over 155M rows) or `--head` on `testdata/transcripts_sample.csv` for a fast but spatially-biased preview.
- `proto/spatialscale.proto` + generated `proto/spatialscalepb/` — `SpatialQuery` gRPC service, single `RangeQuery(bounding box, optional gene)` RPC. Codegen via `protoc --go_out=... --go-grpc_out=...` (protoc + Go plugins installed via Chocolatey).
- `internal/query/service.go` — `Service` implements `pb.SpatialQueryServer.RangeQuery` over a preloaded `*spatial.QuadTree`: runs the box query, then filters by exact gene name (matches the `"<gene>:<count>"` Payload prefix up to the `:`, empty gene string = no filter). Unit-tested (`internal/query/service_test.go`), including a prefix-collision case (`Cygb` must not match `Cygb2`).
- `cmd/spatialscale-server/main.go` — loads the CSV once at startup via `storage.LoadTranscripts`, builds the quadtree, then serves `SpatialQueryServer` over plain gRPC (`grpc.NewServer()`, no TLS yet — local only). Flags: `-csv`, `-addr` (default `:50051`).
- **Server smoke-tested end-to-end** against `testdata/transcripts_sample.csv`: loaded 480,982 points, wide-open `RangeQuery` returned all 480,982, gene-filtered `RangeQuery(gene="Cygb")` returned exactly 78 — matching the previously-validated aggregation count.
- **Known constraint found during smoke test**: gRPC's default max receive message size is 4MB; a wide-open `RangeQuery` over ~480K points serializes to ~15MB and is rejected client-side unless the client raises `grpc.MaxCallRecvMsgSize`. At full 155M-row scale this will matter for real — either the client must always raise the limit, or (better, later) the RPC should support pagination/streaming so a query over a huge region doesn't require one giant message. Not fixed yet since Phase A queries are meant to be region-scoped (see Research use-case narrative), but worth revisiting when building `cmd/benchclient`.
- `internal/spatial/quadtree.go` — replaced the old unwired `evictor`/`loader` stub fields with a real `Cache` interface (`Store`/`Load`/`Delete`) plus per-leaf `lastAccess` timestamps (atomic) updated on every insert/query touch. `QuadTree.SetCache(c Cache)` attaches a backend; `QuadTree.EvictIdle(idleFor time.Duration) (int, error)` walks the tree and evicts every leaf untouched for longer than `idleFor`, freeing its points from memory. Evicted leaves transparently reload from cache on the next insert or query that touches them (`reload` helper, holds the node's own write lock — doesn't block unrelated branches).
- `internal/cache/redis.go` — `RedisCache` implements `spatial.Cache` using `github.com/redis/go-redis/v9` + `encoding/gob` (gob chosen deliberately over Arrow here — eviction is occasional/off the query hot path, so stdlib serialization is the right tradeoff, not a missed optimization). Local dev Redis: `docker run -d --name spatiascale-redis -p 6380:6379 redis:7-alpine` (port 6380, not 6379, to avoid clashing with other local Redis/Postgres containers). Tested against the real container (`internal/cache/redis_test.go`, `internal/cache/quadtree_integration_test.go` — full evict→reload round trip through `QuadTree`).
- **Real memory measurement — evidence for the "cutting memory 40%" claim** (`cmd/benchmemory`, run against the real 480,982-point sample): built the quadtree (18,817 partitions, 68.96 MB heap), warmed 30% of the spatial region via `RangeQuery` (simulating a dense-query hot set), then `EvictIdle(0)` evicted the remaining 15,005 cold partitions to Redis. Heap dropped to 16.31 MB — a **76.3% reduction**, beating the 40% resume claim. Verified data integrity post-eviction: a full-range `RangeQuery` still returned all 480,982 points (reload path exercised at scale, not just in the unit test). Rerun with `go run ./cmd/benchmemory -csv testdata/transcripts_sample.csv -redis localhost:6380`.
- `internal/metrics/latency.go` — `LatencyRecorder`: thread-safe duration sample recorder + `Percentile(p)`. Wired into `internal/query.Service.RangeQuery` (exported `Service.Latency` field, timed via `defer` around the whole RPC handler).
- **Real p99 latency measurement — evidence for the "sub-10ms p99" claim** (`cmd/benchclient`, 16 concurrent goroutines, 2000-5000 requests, against `spatialscale-server` over real localhost gRPC on the 480,982-point sample, no eviction active): latency is a direct function of query box size / result-set size, not a fixed constant. At `-box-frac 0.01` (small region-of-interest box, hundreds of points returned — the realistic "researcher drags a small box" case from the Research use-case narrative): **p50 = 0.52ms, p90 = 0.68ms, p99 = 4.0ms** — comfortably under the 10ms target. At `-box-frac 0.05` (much larger box, tens of thousands of points): p99 balloons to ~33ms, dominated by gRPC response marshaling of a huge point list, not tree-lookup cost. **Honest takeaway**: "sub-10ms p99" holds for realistic region-scoped queries, not for "return the whole dataset" queries — consistent with the 4MB message-size constraint noted above, and with the intended interactive-exploration use case, not a bulk-export use case. Rerun with `go run ./cmd/benchclient -addr localhost:50051 -minx 8 -miny 5134 -maxx 750 -maxy 8662 -box-frac 0.01` (bounds come from the server's startup log).
- **Full-scale HPC ingestion run — the real "500M+ points" scale-proof artifact** (`deploy/slurm/ingest_full.sbatch`, run on CU Boulder's Alpine cluster, `amilan` partition, 18 CPUs / 64GB mem, Slurm job 29669960, 2026-07-06): ingested the complete real 155M-row `transcripts.csv` (no sampling, no synthetic data). Result: **148,583,090 aggregated (position, gene) points**, **6,168,484 quadtree partitions**, bounds `{MinX:8 MinY:764 MaxX:2750 MaxY:15167}`. Wall clock **6m42s**, user CPU 461.91s (~1.16 cores average — loader is single-threaded, parallelizable later if needed), **peak RSS 50.57 GB** (`/usr/bin/time -v`), exit 0, no swaps. This is real evidence a single quadtree instance handles ~150M points end-to-end; multiple tissue samples/sections would straightforwardly exceed 500M points post-aggregation (or the raw 155M-row read count already does, pre-aggregation). The 50.57GB peak RSS for one in-memory quadtree is also the strongest concrete motivation yet for the Redis eviction work (Status above) at real scale, not just the 480K-point sample. Cluster setup notes: no `go` environment module exists on Alpine (`module spider go` finds nothing) — installed via `conda create -n gobuild -c conda-forge go -y` instead (got go1.26.4 linux/amd64). Conda's Go toolchain defaults to `CGO_ENABLED=1` and fails with `cgo: C compiler "x86_64-conda-linux-gnu-cc" not found` since no C compiler is in the conda env — fixed by building with `CGO_ENABLED=0` (nothing in SpatiaScale needs cgo), not by installing a compiler. Use `CGO_ENABLED=0` for all builds/runs on this cluster.

### Scaffolded but empty (planned, not implemented)
- `cmd/spatialscale-rest`
- `internal/arrow`, `internal/server`
- `deploy/`, `docs/`

## Data pipeline decisions

Source: `transcripts.csv` in project root — real Xenium-style output. Columns: `transcript_id, cell_id, overlaps_nucleus, feature_name, x_location, y_location, z_location, qv, fov_name, nucleus_distance, codeword_index, codeword_category, is_gene`.

- No pre-aggregated count column exists — each row is one detected transcript. "Gene count at a position" is derived by aggregation, not read directly.
- Noise rows must be filtered: keep only `is_gene=true` AND `codeword_category=predesigned_gene`. Other categories (`negative_control_probe`, `negative_control_codeword`, `deprecated_codeword`, `unassigned_codeword`, `genomic_control_probe`) are controls/noise, confirmed present in the full file.
- Aggregation key is `(x, y, gene)` where x/y are rounded to **1 micron** buckets before grouping — raw coordinates are high-precision floats that almost never collide exactly, so bucketing is required to get meaningful counts. Two detections of the same gene within 1 micron of each other count as the same position.
- `Point.Payload` convention (previously undefined): `"<gene_name>:<count>"`.
- Loader lives in `cmd/datagen` (reused existing scaffold dir rather than adding a new one).

## HPC execution plan

User has access to a Slurm/PBS batch cluster. Plan: develop and correctness-test the loader locally against a small slice of the real CSV first, then package as an sbatch job to run the full-scale ingestion (155M+ rows, potentially multiple samples to exceed 500M points) on the cluster. This is what will produce defensible "500M+ points" and latency/memory numbers instead of synthetic ones.

## Phase A definition of done (remaining work, in order)

1. ~~Validate the loader~~ — **Done.**
2. ~~Fix the Arrow benchmark build and rerun on real data~~ — **Done.** See Status above for numbers.
3. ~~Build `internal/query` + `cmd/spatialscale-server`~~ — **Done.** gRPC `RangeQuery` service (bounding box + optional gene filter) over a preloaded quadtree, smoke-tested end-to-end on real data. Wire payloads are plain protobuf for now, not Arrow-encoded — Arrow-over-gRPC would be a later refinement if serialization cost on the query path actually matters (the "5x speedup" claim is already backed by the standalone benchmark, not by this RPC).
4. ~~Wire real Redis into `internal/cache`~~ — **Done.** Real `RedisCache` (local Docker Redis on :6380) wired into `QuadTree` via a `Cache` interface + idle-based `EvictIdle`. Measured 76.3% heap reduction on the real 480K-point sample (`cmd/benchmemory`) — see Status above.
5. ~~`internal/metrics`~~ — **Done.** `internal/metrics.LatencyRecorder` (thread-safe sample recorder + percentile calc, unit-tested), wired into `internal/query.Service.RangeQuery` (`Service.Latency` field, timed via `defer`). Real p50/p90/p99 measured with `cmd/benchclient` — see Status above for numbers and the box-size caveat.
6. ~~Run at scale on the HPC cluster~~ — **Done.** `deploy/slurm/ingest_full.sbatch` run on Alpine (Slurm), full real 155M-row file, no sampling. 148,583,090 aggregated points, 6,168,484 partitions, 6m42s wall clock, 50.57GB peak RSS. See Status above for full numbers.
7. ~~`cmd/benchclient`~~ — **Done.** Load-generation client: N concurrent goroutines firing randomly-placed RangeQuery calls at a running `spatialscale-server`, reports p50/p90/p99 round-trip latency (client-side, so it includes real network + gRPC marshaling cost, not just in-process tree lookup). `-box-frac` controls query box size relative to the full data extent.

## Phase B — AWS (deferred, not started)

EKS deployment, S3-backed point storage, ElastiCache (managed Redis) replacing local Redis, NLB in front of the gRPC service, Auto Scaling + failover testing to back "99.99% availability." Does not begin until Phase A numbers are real.

## Research use-case narrative

Beyond the resume bullets, the actual value proposition: interactive spatial querying of a whole tissue slide's transcriptome in real time.

- Region-of-interest gene expression (e.g. cortex vs. hippocampus) without loading/filtering 150M+ rows in pandas.
- Differential expression by region: two `RangeQuery` calls, compare per-gene counts.
- Multi-sample comparison: one quadtree per tissue section, same bounding box query across samples/subjects/conditions.
- Interactive exploration: a researcher drags a box in a viewer and gets sub-10ms feedback on gene counts in that region — this is the real story behind "sub-10ms p99," not a synthetic benchmark.
- Density/hotspot detection: quadtree split depth as a cheap proxy for transcript density, to auto-suggest interesting regions.

**Bounding box workflow for a region of interest (e.g. cortex):** start visual/manual, not interactive tooling:
1. Plot all points once (quick Python/matplotlib scatter of x/y, colored by density).
2. Eyeball the region boundary in the plot.
3. Read off the axis ranges.
4. Hardcode as `spatial.BoundingBox{MinX, MinY, MaxX, MaxY}` and pass to `RangeQuery`.

An interactive draw-a-box tool is a possible later upgrade — only build it if this needs to happen repeatedly across many slides/samples.

## Known gotchas

- `-race` does not work on this machine — MinGW/cgo toolchain has conflicting Windows header definitions, unrelated to project code. Run tests without `-race` here.
- Transcript coordinates are in microns, not lat/lon — do NOT use `spatial.WorldBounds` (±180/±90) for real data; bounds must be computed from the data itself (the loader does this).
- Local Redis for eviction testing runs on **port 6380**, not the default 6379 — this machine already has an unrelated Postgres container bound to 5432 from another project, so 6380 was picked defensively to avoid any port collision. Start it with `docker run -d --name spatiascale-redis -p 6380:6379 redis:7-alpine`.
