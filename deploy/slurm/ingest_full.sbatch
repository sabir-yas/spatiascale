#!/bin/bash
#SBATCH --account=amc-general
#SBATCH --time=02:00:00
#SBATCH --qos=normal
#SBATCH --partition=amilan
#SBATCH --nodes=1
#SBATCH --cpus-per-task=16
#SBATCH --mem=64G
#SBATCH --job-name=spatiascale-ingest
#SBATCH --output=logs/%x.%j.out
#SBATCH --error=logs/%x.%j.err
#SBATCH --mail-type=BEGIN,END,FAIL
#SBATCH --mail-user=yaseer.a.sabir@cuanschutz.edu

# Full-scale ingestion run: loads the real 155M-row transcripts.csv,
# aggregates to (x,y,gene) points, builds the quadtree. This is the
# scale-proof artifact for the "500M+ points" resume claim.
#
# Run from the spatiascale repo root: sbatch deploy/slurm/ingest_full.sbatch

set -euo pipefail
mkdir -p logs

module load anaconda
conda activate gobuild

export CGO_ENABLED=0
BIN=/tmp/spatiascale-datagen-$SLURM_JOB_ID
go build -o "$BIN" ./cmd/datagen

echo "=== job start: $(date) ==="
echo "=== node: $(hostname), cpus: $SLURM_CPUS_PER_TASK, mem: ${SLURM_MEM_PER_NODE}MB ==="

/usr/bin/time -v "$BIN" -csv ../transcripts.csv

echo "=== job end: $(date) ==="

rm -f "$BIN"
