"""Summarise and plot tools/fpscore's SourceAFIS scores: genuine vs impostor,
rolled<->rolled and latent<->rolled, with the rate at SourceAFIS's default
threshold (40, which its docs put at FMR 0.01% on real prints).

    uv run plot_scores.py
"""

import argparse
import collections
import csv
import json

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np

THRESHOLD = 40
KINDS = ["genuine_rr", "impostor_rr", "genuine_lr", "impostor_lr"]


def finger(name):
    return int(name[1:4])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scores", default="../fpscore/scores.csv")
    ap.add_argument("--manifest", default="out/impressions/manifest.json")
    ap.add_argument("--out", default="samples/fp_scores.png")
    args = ap.parse_args()

    rows = list(csv.DictReader(open(args.scores)))
    man = json.load(open(args.manifest))
    cls = {r["finger"]: r["class"] for r in man["fingers"]}
    by = collections.defaultdict(list)
    for r in rows:
        by[r["kind"]].append(float(r["score"]))

    for k in KINDS:
        s = np.array(by[k])
        q = np.percentile(s, [1, 50, 99, 99.9])
        print(f"{k:12s} n={len(s):6d}  p1={q[0]:6.1f}  median={q[1]:6.1f}  p99={q[2]:6.1f}  "
              f"p99.9={q[3]:6.1f}  max={s.max():6.1f}  >={THRESHOLD}: {np.mean(s >= THRESHOLD):.2%}")

    imp = [(cls[finger(r["probe"])] == cls[finger(r["candidate"])], float(r["score"]))
           for r in rows if r["kind"] == "impostor_rr"]
    for label, want in (("same class", True), ("other class", False)):
        s = np.array([v for same, v in imp if same == want])
        print(f"impostor_rr {label:11s} n={len(s):5d}  p99={np.percentile(s, 99):5.1f}  >={THRESHOLD}: {np.mean(s >= THRESHOLD):.2%}")

    # latent rank-1: the true finger's rolled 0 outscores all 99 other rolled 0s
    genuine = {r["probe"]: float(r["score"]) for r in rows
               if r["kind"] == "genuine_lr" and r["candidate"].endswith("_r0.png")}
    best = collections.defaultdict(float)
    for r in rows:
        if r["kind"] == "impostor_lr":
            best[r["probe"]] = max(best[r["probe"]], float(r["score"]))
    rank1 = np.mean([genuine[l] > best[l] for l in genuine])
    print(f"latent rank-1 against {len(cls)} rolled: {rank1:.1%} of {len(genuine)}")

    fig, axs = plt.subplots(1, 2, figsize=(13, 4.5))
    for ax, (g, i, title) in zip(axs, [("genuine_rr", "impostor_rr", "rolled vs rolled"),
                                       ("genuine_lr", "impostor_lr", "latent vs rolled")]):
        top = max(max(by[g]), max(by[i]))
        bins = np.logspace(-1, np.log10(top + 1), 60)
        # share of pairs per bin on a log axis: density over log-spaced bins
        # lets the spike of zero scores swamp the plot and hides the genuine
        for k, name in ((i, "impostor"), (g, "genuine")):
            s = np.array(by[k])
            ax.hist(s + 0.1, bins=bins, alpha=0.6, weights=np.full(len(s), 1 / len(s)), label=f"{name} (n={len(s)})")
        ax.set_yscale("log")
        ax.set_ylabel("share of pairs")
        ax.axvline(THRESHOLD, color="k", ls="--", lw=1, label=f"threshold {THRESHOLD}")
        ax.set_xscale("log")
        ax.set_xlabel("SourceAFIS similarity (+0.1, log scale)")
        ax.set_title(title)
        ax.legend()
    fig.suptitle(f"SourceAFIS 3.18.1 on {len(cls)} synthetic fingers")
    fig.tight_layout()
    fig.savefig(args.out, dpi=90)
    print(f"wrote {args.out}")


if __name__ == "__main__":
    main()
