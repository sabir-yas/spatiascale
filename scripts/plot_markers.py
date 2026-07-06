#!/usr/bin/env python3
"""Biological sanity check: scatter-plot transcripts and overlay marker genes.

Real tissue should look like tissue (recognizable structure, not noise), and
marker genes for distinct cell types should occupy distinct, spatially
coherent regions rather than being uniformly interleaved.

Xenium output is ordered by detection/FOV, not spatial position, so taking
the first N rows only covers one small region of the tissue. Use --sample to
reservoir-sample uniformly across the whole file instead.

Usage:
    python scripts/plot_markers.py testdata/transcripts_sample.csv
    python scripts/plot_markers.py transcripts.csv --sample 2000000
"""
import argparse
import csv
import random
import sys

import matplotlib.pyplot as plt
import pandas as pd

# Canonical mouse brain cell-type markers, chosen so each should occupy a
# visually distinct region if the data is biologically sound:
#   Snap25   - pan-neuronal
#   Slc17a7  - excitatory (glutamatergic) neurons
#   Gad1     - inhibitory (GABAergic) neurons
#   Gfap     - astrocytes
MARKER_GENES = ["Snap25", "Slc17a7", "Gad1", "Gfap"]


USECOLS = ["feature_name", "x_location", "y_location", "is_gene", "codeword_category"]


def reservoir_sample(csv_path, k, seed=0):
    """Uniformly sample k rows from the whole file in one streaming pass."""
    rng = random.Random(seed)
    reservoir = []
    with open(csv_path, newline="") as f:
        reader = csv.DictReader(f)
        for i, row in enumerate(reader):
            if len(reservoir) < k:
                reservoir.append(row)
            else:
                j = rng.randint(0, i)
                if j < k:
                    reservoir[j] = row
    return pd.DataFrame(reservoir, columns=USECOLS)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("csv_path")
    ap.add_argument("--sample", type=int, default=None,
                     help="randomly sample N rows across the whole file (recommended for large files)")
    ap.add_argument("--head", action="store_true",
                     help="read only the first N rows instead of random-sampling (fast but spatially biased)")
    args = ap.parse_args()

    if args.sample and not args.head:
        df = reservoir_sample(args.csv_path, args.sample)
        df["is_gene"] = df["is_gene"].map({"True": True, "true": True}).fillna(False)
    else:
        df = pd.read_csv(args.csv_path, usecols=USECOLS, nrows=args.sample)

    df = df[(df["is_gene"] == True) & (df["codeword_category"] == "predesigned_gene")]  # noqa: E712
    df["x_location"] = df["x_location"].astype(float)
    df["y_location"] = df["y_location"].astype(float)

    if df.empty:
        sys.exit("no gene rows after filtering — check column names/values")

    fig, axes = plt.subplots(1, 2, figsize=(16, 8))

    # Left: overall density, all genes — should look like tissue, not noise.
    axes[0].scatter(df["x_location"], df["y_location"], s=0.5, alpha=0.15, color="black")
    axes[0].set_title(f"All transcripts (n={len(df):,})")
    axes[0].set_xlabel("x_location (microns)")
    axes[0].set_ylabel("y_location (microns)")
    axes[0].set_aspect("equal")

    # Right: marker genes overlaid in distinct colors — should form distinct,
    # spatially coherent regions, not a uniform interleaved mix.
    colors = ["tab:red", "tab:blue", "tab:green", "tab:orange"]
    for gene, color in zip(MARKER_GENES, colors):
        sub = df[df["feature_name"] == gene]
        axes[1].scatter(sub["x_location"], sub["y_location"], s=2, alpha=0.6, label=f"{gene} (n={len(sub):,})", color=color)
    axes[1].set_title("Marker genes")
    axes[1].set_xlabel("x_location (microns)")
    axes[1].legend(markerscale=5)
    axes[1].set_aspect("equal")

    for ax in axes:
        ax.invert_yaxis()  # image-style coordinates, matches typical tissue scan orientation

    plt.tight_layout()
    out_path = "marker_plot.png"
    plt.savefig(out_path, dpi=150)
    print(f"wrote {out_path}")


if __name__ == "__main__":
    main()
