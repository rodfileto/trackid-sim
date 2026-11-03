"""C0 spike, step 3: turn base faces into CCTV-like captures.

A base face is a clean 512x512 studio portrait. A real questioned image is a
small face inside a 1920x1080 frame, soft, noisy and JPEG-crushed. This applies
that, deterministically, and records every parameter it used.

The pair (base, degraded) is the same identity, so this also supplies the
*genuine* side of the score check -- the impostor side is any two different
base faces. Identity preservation across a Qwen edit is a separate question.

Ordering matters and follows the real capture chain: resize (optics/sensor),
blur (focus), noise (sensor gain), then JPEG (encoder), last.

ROADMAP C3 moves this into Go so `world.json` owns the parameters. This is the
spike version; keep the parameter names aligned so the port is mechanical.

    uv run python degrade.py
"""
import argparse, hashlib, io, json, random
from pathlib import Path

import numpy as np
from PIL import Image, ImageFilter

MANIFEST = Path("pool_manifest.json")
OUT = Path("spike_out/degraded")
FRAME = (1920, 1080)

# face_px is the face height in the final frame: the single biggest driver of
# recognition difficulty. ~200 px is a cooperative gate camera, ~40 px is a
# ceiling camera across a room.
LEVELS = {
    "good":   dict(face_px=200, blur=0.6, noise=3.0, jpeg=85),
    "medium": dict(face_px=100, blur=1.2, noise=6.0, jpeg=60),
    "poor":   dict(face_px=48,  blur=1.8, noise=10.0, jpeg=35),
}


def degrade(img, *, face_px, blur, noise, jpeg, rng):
    """Clean portrait -> one frame-sized capture. Pure given rng."""
    # The portrait is ~2.2x the face itself, so scale the whole crop to put the
    # face at roughly face_px.
    side = max(8, int(round(face_px * 2.2)))
    small = img.convert("RGB").resize((side, side), Image.LANCZOS)
    if blur:
        small = small.filter(ImageFilter.GaussianBlur(blur))
    if noise:
        arr = np.asarray(small).astype(np.float32)
        arr += rng.normal(0.0, noise, arr.shape)
        small = Image.fromarray(np.clip(arr, 0, 255).astype(np.uint8))

    frame = Image.new("RGB", FRAME, (28, 30, 33))
    # Jitter placement so the face is not always frame-centre.
    x = int(rng.integers(0, FRAME[0] - side)) if FRAME[0] > side else 0
    y = int(rng.integers(0, FRAME[1] - side)) if FRAME[1] > side else 0
    frame.paste(small, (x, y))

    buf = io.BytesIO()
    frame.save(buf, format="JPEG", quality=jpeg)
    buf.seek(0)
    return Image.open(buf).convert("RGB"), {"x": x, "y": y, "side": side}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seed", type=int, default=20260920)
    ap.add_argument("--levels", nargs="*", default=list(LEVELS))
    args = ap.parse_args()

    faces = [f for f in json.loads(MANIFEST.read_text())["faces"] if Path(f["file"]).exists()]
    if not faces:
        raise SystemExit("no generated faces found; run gen_pool.py first")
    OUT.mkdir(parents=True, exist_ok=True)

    rows = []
    for f in faces:
        img = Image.open(f["file"])
        for level in args.levels:
            params = LEVELS[level]
            # Seed per (face, level): reproducible, and independent of order.
            key = int(hashlib.sha256(f"{args.seed}:{f['id']}:{level}".encode()).hexdigest()[:8], 16)
            rng = np.random.default_rng(key)
            out, placed = degrade(img, rng=rng, **params)
            path = OUT / f"{f['id']}_{level}.jpg"
            out.save(path, quality=95)  # lossless-ish container; crush already applied
            rows.append({"id": f["id"], "level": level, "file": str(path),
                         "source": f["file"], "params": params, "placement": placed,
                         "sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
        print(f"{f['id']}: {', '.join(args.levels)}", flush=True)

    out_manifest = Path("degraded_manifest.json")
    out_manifest.write_text(json.dumps(
        {"seed": args.seed, "frame": FRAME, "levels": LEVELS, "captures": rows}, indent=1))
    print(f"{len(rows)} captures -> {OUT}/  manifest -> {out_manifest}")


if __name__ == "__main__":
    main()
