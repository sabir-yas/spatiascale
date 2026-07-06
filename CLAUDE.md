# SpatiaScale

Cloud-native distributed spatial query engine (Go). Target end-state (resume goal):

- Fault-tolerant spatial query engine on AWS EKS, indexing 500M+ multi-dimensional points in S3, sub-10ms p99 retrieval latency.
- Concurrent Quadtree partitioning integrated with Amazon ElastiCache (Redis), cutting cluster memory utilization by 40% during dense range queries.
- gRPC + Apache Arrow network layer behind an AWS NLB, 5x serialization speedup, 99.99% availability during failover testing via AWS Auto Scaling.

**Phased approach:** Phase A (current) is local + HPC only, no AWS — prove the engine works and get real measured numbers. Phase B (later) is AWS deployment, and only starts once Phase A numbers are real. Do not jump ahead to EKS/S3/ElastiCache/NLB work until Phase A is solid.

## Status

### Done
- `internal/spatial/` — concurrent quadtree: `Point`/`BoundingBox` primitives, insert/split/RangeQuery, per-node `RWMutex` locking, atomic counts, stubbed Redis eviction hooks (`stateEvicted`, `evictor`/`loader` fields — not wired to real Redis yet). Tested including concurrent insert/query.
- `benchmarks/serialization/` — Arrow vs JSON encode/decode benchmarks + round-trip correctness tests, on both synthetic points and real transcript data. go.sum issue fixed (`go get` pulled in flatbuffers). Schema now includes a `payload` binary column so real gene-name+count payloads are exercised, not just fixed-width numerics. **Real-data results** (`go test -bench=Real -benchmem ./benchmarks/serialization/`, ~480K aggregated transcript points): encode 155.3ms (JSON) vs 49.3ms (Arrow) = 3.1x; decode 618.6ms (JSON) vs 54.5ms (Arrow) = 11.3x — JSON decode does 481,024 allocs vs Arrow's 133. This is the real evidence behind the "5x serialization speedup" resume claim.
- `internal/storage/transcripts.go` — `LoadTranscripts(io.Reader)` extracted from `cmd/datagen` so both the CLI and the benchmark package can load real transcript data without duplicating CSV-parsing logic.
- `cmd/datagen/main.go` — thin CLI wrapper around `storage.LoadTranscripts`, loads into a `QuadTree`. Validated against a 500K-row real slice (Cygb: 79 raw detections → 78 aggregated groups, total count preserved).
- **Biological validation passed** — `scripts/plot_markers.py` reservoir-samples rows uniformly across the full 155M-row file (pure first-N sampling is spatially biased since Xenium output is FOV-ordered, not spatially ordered) and plots all transcripts plus 4 canonical mouse brain markers (`Snap25` pan-neuronal, `Slc17a7` excitatory, `Gad1` inhibitory, `Gfap` astrocyte). Result: the point cloud traces a real anatomical tissue outline (not noise), marker gene relative abundances are biologically plausible (Snap25 most abundant, matching that most brain cells are neurons), and Gad1+ inhibitory neurons show a visibly denser, spatially distinct cluster rather than being uniformly spread — confirms the ingestion pipeline preserves real biological signal end to end. Run with `python3 scripts/plot_markers.py transcripts.csv --sample 1000000` (takes ~13 min in pure Python over 155M rows) or `--head` on `testdata/transcripts_sample.csv` for a fast but spatially-biased preview.

### Scaffolded but empty (planned, not implemented)
- `cmd/spatialscale-server`, `cmd/spatialscale-rest`, `cmd/benchclient`
- `internal/arrow`, `internal/cache`, `internal/metrics`, `internal/query`, `internal/server`
- `proto/`, `deploy/`, `docs/`

No git repo yet.

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
3. **Build `internal/query` + `cmd/spatialscale-server`** — gRPC service wrapping the quadtree (`Insert`/`RangeQuery` RPCs, Arrow-encoded wire payloads), done locally first, no NLB yet.
4. **Wire real Redis into `internal/cache`** — replace the `evictor`/`loader` stubs in `internal/spatial/quadtree.go` with an actual Redis client (local Docker Redis is fine), then measure memory before/after eviction under a dense-query workload on real data — evidence for "cutting memory 40%."
5. **`internal/metrics`** — instrument p99 latency for RangeQuery so "sub-10ms p99" is a measured number.
6. **Run at scale on the HPC cluster** — package the above as an sbatch job, run the full dataset (or multiple samples to exceed 500M points), capture logs as the scale-proof artifact.
7. **`cmd/benchclient`** — load-generation client for concurrent RangeQuery traffic, used for latency/throughput numbers and later failover testing.

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
