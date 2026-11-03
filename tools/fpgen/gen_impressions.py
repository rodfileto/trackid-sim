"""Impressions for the matcher check: N fingers, several rolled impressions
and latents of each, written as 500 dpi PNGs for tools/fpscore.

Every finger is its own master print (seeded from --seed and its index), and
every impression draws its own distortion, placement, pressure and noise from
a separate stream, so impressions of one finger differ the way two inkings
of the same finger do. Rotation and shift are wider than the sketch's
defaults on purpose: impressions that differ too little would make the
genuine scores flatter the generator.

    uv run gen_impressions.py --fingers 100 --rolled 4 --latents 2
"""

import argparse
import json
import os
from concurrent.futures import ProcessPoolExecutor

import imageio.v3 as iio
import numpy as np

import sketch_one as fp

CLASSES, SHARES = ["left_loop", "right_loop", "whorl", "arch"], [0.33, 0.30, 0.30, 0.07]


def finger(args):
    i, seed, n_rolled, n_latents, out = args
    fseed = seed * 100_000 + i
    rng = np.random.default_rng([fseed, 0])
    cls = str(rng.choice(CLASSES, p=SHARES))
    mask = fp.fingertip_mask(rng)
    cores, deltas = fp.singular_points(rng, cls)
    theta = fp.orientation_field(cores, deltas, rng)
    period = fp.frequency_map(rng)
    _, _, grown, _ = fp.grow_ridges(theta, period, mask, rng)
    master, _ = fp.add_minutiae_pairs(grown, theta, period, mask, cores + deltas, rng, n_pairs=int(rng.integers(18, 26)))
    master = np.where(mask, master, 0.0)
    _, mins = fp.minutiae(master, mask)

    files = {"rolled": [], "latent": []}
    for k in range(n_rolled):
        img = fp.rolled_impression(master, mask, np.random.default_rng([fseed, 1, k]), rot=15, shift_sd=12, amp=8)
        name = f"f{i:03d}_r{k}.png"
        iio.imwrite(os.path.join(out, name), (img * 255).round().astype(np.uint8))
        files["rolled"].append(name)
    for k in range(n_latents):
        img, region = fp.latent(master, mask, np.random.default_rng([fseed, 2, k]))
        name = f"f{i:03d}_l{k}.png"
        iio.imwrite(os.path.join(out, name), (img * 255).round().astype(np.uint8))
        files["latent"].append({"file": name, "area": round(float(region.sum() / mask.sum()), 3)})
    return {"finger": i, "seed": fseed, "class": cls, "minutiae_master": len(mins), **files}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fingers", type=int, default=100)
    ap.add_argument("--rolled", type=int, default=4)
    ap.add_argument("--latents", type=int, default=2)
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--out", default="out/impressions")
    ap.add_argument("--workers", type=int, default=max(1, os.cpu_count() - 2))
    args = ap.parse_args()

    os.makedirs(args.out, exist_ok=True)
    jobs = [(i, args.seed, args.rolled, args.latents, args.out) for i in range(args.fingers)]
    with ProcessPoolExecutor(args.workers) as ex:
        rows = list(ex.map(finger, jobs))
    manifest = {"dpi": 500, "seed": args.seed, "fingers": rows}
    with open(os.path.join(args.out, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=1)
    by_cls = {c: sum(r["class"] == c for r in rows) for c in CLASSES}
    print(f"{len(rows)} fingers, {len(rows) * args.rolled} rolled, {len(rows) * args.latents} latents -> {args.out}  {by_cls}")


if __name__ == "__main__":
    main()
