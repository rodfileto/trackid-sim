"""Plot the genuine/impostor score distributions facescore produced.

Small multiples, one panel per quality level, rather than all six distributions
on one axis: the question is how separation *changes* as the capture degrades,
and stacked panels on a shared x-axis show that directly.

Densities, not counts -- there are 30 genuine pairs against 870 impostor ones
per level, so raw counts would say more about pair arithmetic than about scores.

    uv run python plot_scores.py
"""
import argparse, json
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np

# Categorical slots 1 and 2, validated for CVD separation against a light
# surface (worst adjacent dE 24.7 protan / 33.6 normal). Colour follows the
# entity: genuine is always blue, impostor always orange, in every panel.
GENUINE, IMPOSTOR = "#2a78d6", "#eb6834"
SURFACE, INK, MUTED = "#fcfcfb", "#1a1a19", "#6b6b66"
LEVELS = [("good", "good - 200 px face"),
          ("medium", "medium - 100 px face"),
          ("poor", "poor - 48 px face")]


def meanstd(v):
    return float(np.mean(v)), float(np.std(v))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scores", type=Path, default=Path("../facescore/scores.json"))
    ap.add_argument("--out", type=Path, default=Path("samples/score_distributions.png"))
    args = ap.parse_args()

    data = json.loads(args.scores.read_text())
    gen = {lvl: [p["score"] for p in data["genuine"] if p.get("level") == lvl]
           for lvl, _ in LEVELS}
    imp = {lvl: [p["score"] for p in data["impostor"] if p.get("level") == lvl]
           for lvl, _ in LEVELS}

    bins = np.linspace(-0.15, 1.0, 58)
    fig, axes = plt.subplots(len(LEVELS), 1, figsize=(8.2, 7.4), sharex=True,
                             facecolor=SURFACE)

    for ax, (lvl, title) in zip(axes, LEVELS):
        g, i = np.array(gen[lvl]), np.array(imp[lvl])
        ax.set_facecolor(SURFACE)
        ax.hist(i, bins=bins, density=True, color=IMPOSTOR, alpha=0.85,
                label=f"impostor  n={len(i)}")
        ax.hist(g, bins=bins, density=True, color=GENUINE, alpha=0.85,
                label=f"genuine  n={len(g)}")

        mg, sg = meanstd(g)
        mi, si = meanstd(i)
        d = (mg - mi) / np.sqrt((sg**2 + si**2) / 2)
        overlap = g.min() <= i.max()
        note = (f"d' = {d:.2f}   " +
                ("overlap" if overlap else f"clean gap {g.min() - i.max():.3f}"))
        ax.text(0.985, 0.88, note, transform=ax.transAxes, ha="right", va="top",
                fontsize=10, color=INK if not overlap else "#b3401a")
        ax.text(0.015, 0.88, title, transform=ax.transAxes, ha="left", va="top",
                fontsize=10.5, color=INK, fontweight="medium")

        # Recessive axes: no box, one soft horizontal grid, no y tick labels --
        # the shape carries the message, the exact density does not.
        for side in ("top", "right", "left"):
            ax.spines[side].set_visible(False)
        ax.spines["bottom"].set_color("#d8d8d2")
        ax.grid(axis="y", color="#ededea", linewidth=0.8)
        ax.set_axisbelow(True)
        ax.set_yticks([])
        ax.tick_params(colors=MUTED, length=0)

    axes[0].legend(loc="upper left", bbox_to_anchor=(0.015, 0.82), frameon=False,
                   fontsize=9.5, labelcolor=INK, handlelength=1.1)
    axes[-1].set_xlabel("cosine similarity (AuraFace embedding)", color=MUTED, fontsize=10)
    fig.suptitle("Same person vs different person, by capture quality",
                 x=0.015, ha="left", fontsize=13.5, color=INK)
    fig.text(0.015, 0.935,
             "30 FLUX.1-schnell faces; genuine = base vs degraded copy of the same face. "
             "Impostor pairs are matched to each level.",
             ha="left", fontsize=9.5, color=MUTED)
    fig.tight_layout(rect=[0, 0, 1, 0.915])
    args.out.parent.mkdir(parents=True, exist_ok=True)
    fig.savefig(args.out, dpi=150, facecolor=SURFACE)
    print(f"-> {args.out}")


if __name__ == "__main__":
    main()
