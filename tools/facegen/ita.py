"""Measure the skin tone the generator actually produced.

The prompts ask for a tone; whether FLUX renders it is a separate question, and
the first pool answered it badly -- it rendered lighter than asked. That was
recorded as an eyeballed impression. This measures it instead, so the corpus
ships a number rather than a recollection.

ITA (Individual Typology Angle, Chardon et al. 1991) is the standard tone
metric in dermatology and in face-recognition bias work:

    ITA = arctan((L* - 50) / b*) in degrees

computed in CIELab. High ITA is light, low is dark. The usual bands:

    very light > 55 >= light > 41 >= intermediate > 28 >= tan > 10 >= brown
    > -30 >= dark

Two caveats this cannot remove, both of which belong in any write-up:

  * ITA is sensitive to illumination, and these are synthetic images with
    flattering, uniform studio lighting. Absolute values are not comparable to
    measurements on real photographs under other light.
  * ITA describes a pixel, not a person. It is a check on whether the generator
    followed the prompt, never a demographic measurement of the pool -- PLAN.md
    scopes demographic claims out entirely, and this changes nothing about that.

    uv run python ita.py
"""
import argparse, json
from collections import defaultdict
from pathlib import Path

import numpy as np
from PIL import Image

MANIFEST = Path("pool_manifest.json")

BANDS = [(55, "very light"), (41, "light"), (28, "intermediate"),
         (10, "tan"), (-30, "brown"), (-1e9, "dark")]


def band(ita):
    for lo, name in BANDS:
        if ita > lo:
            return name
    return "dark"


def srgb_to_lab(rgb):
    """sRGB bytes -> CIELab (D65). Vectorised over an (N, 3) array."""
    c = rgb.astype(np.float64) / 255.0
    lin = np.where(c <= 0.04045, c / 12.92, ((c + 0.055) / 1.055) ** 2.4)
    m = np.array([[0.4124564, 0.3575761, 0.1804375],
                  [0.2126729, 0.7151522, 0.0721750],
                  [0.0193339, 0.1191920, 0.9503041]])
    xyz = lin @ m.T
    white = np.array([0.95047, 1.0, 1.08883])
    t = xyz / white
    d = 6.0 / 29.0
    f = np.where(t > d ** 3, np.cbrt(t), t / (3 * d ** 2) + 4.0 / 29.0)
    L = 116 * f[:, 1] - 16
    a = 500 * (f[:, 0] - f[:, 1])
    b = 200 * (f[:, 1] - f[:, 2])
    return L, a, b


def skin_pixels(img):
    """Central crop, then a chrominance skin mask.

    The mask is the classic YCbCr rule (Cb 77-127, Cr 133-173). It keys on
    chrominance rather than brightness precisely so it does not itself select
    for light skin -- a luminance threshold would bias the very measurement
    this script exists to make. The central crop drops background, hair and
    clothing before the mask ever runs.
    """
    w, h = img.size
    crop = img.crop((int(0.30 * w), int(0.32 * h), int(0.70 * w), int(0.68 * h)))
    a = np.asarray(crop.convert("RGB")).reshape(-1, 3).astype(np.float64)
    r, g, b = a[:, 0], a[:, 1], a[:, 2]
    cb = 128 - 0.168736 * r - 0.331264 * g + 0.5 * b
    cr = 128 + 0.5 * r - 0.418688 * g - 0.081312 * b
    mask = (cb >= 77) & (cb <= 127) & (cr >= 133) & (cr <= 173)
    # Drop specular highlights and deep shadow, which distort L* without
    # telling us anything about the subject's tone.
    y = 0.299 * r + 0.587 * g + 0.114 * b
    mask &= (y > 20) & (y < 250)
    return a[mask]


def ita_of(path):
    px = skin_pixels(Image.open(path))
    if len(px) < 200:
        return None, 0
    L, _, b = srgb_to_lab(px)
    # Median over pixels, then one ITA, rather than a mean of per-pixel ITAs:
    # the arctan is non-linear, so averaging after it would skew the result.
    return float(np.degrees(np.arctan2(np.median(L) - 50, np.median(b)))), len(px)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--group-by", default="ethnicity")
    ap.add_argument("--out", type=Path, default=Path("ita_report.json"))
    args = ap.parse_args()

    faces = [f for f in json.loads(MANIFEST.read_text())["faces"]
             if Path(f["file"]).exists()]
    rows, groups = [], defaultdict(list)
    for f in faces:
        ita, n = ita_of(f["file"])
        if ita is None:
            print(f"  {f['id']}: too few skin pixels ({n}), skipped")
            continue
        rows.append({"id": f["id"], "ita": round(ita, 1), "band": band(ita),
                     "asked_group": f.get(args.group_by),
                     "asked_tone": f.get("skin_tone"), "pixels": n})
        groups[f.get(args.group_by)].append(ita)

    print(f"ITA over {len(rows)} faces, grouped by {args.group_by}\n")
    print(f"{'group':10s} {'n':>4s} {'median':>7s} {'p10':>7s} {'p90':>7s}  band of the median")
    summary = {}
    for g, v in sorted(groups.items(), key=lambda kv: -np.median(kv[1])):
        v = np.array(v)
        med = float(np.median(v))
        summary[g] = {"n": len(v), "median": round(med, 1),
                      "p10": round(float(np.percentile(v, 10)), 1),
                      "p90": round(float(np.percentile(v, 90)), 1),
                      "band": band(med)}
        print(f"{g:10s} {len(v):4d} {med:7.1f} {np.percentile(v, 10):7.1f} "
              f"{np.percentile(v, 90):7.1f}  {band(med)}")

    # Overlap is the question the eyeball could not answer: if the groups the
    # prompt separates do not separate here, the attribute did no work.
    print("\nband counts per group:")
    for g in summary:
        c = defaultdict(int)
        for r in rows:
            if r["asked_group"] == g:
                c[r["band"]] += 1
        print(f"  {g:10s} " + "  ".join(f"{k}:{v}" for k, v in
                                        sorted(c.items(), key=lambda kv: -kv[1])))
    args.out.write_text(json.dumps({"summary": summary, "faces": rows}, indent=1) + "\n")
    print(f"\n-> {args.out}")


if __name__ == "__main__":
    main()
