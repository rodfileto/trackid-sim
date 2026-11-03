"""Join the face pool and the variant identity check into one committed index.

The simulator's world reads this file to bind pool faces to humans: a human
enrolled with a photo needs a base face, and a human captured on camera needs a
face whose variant survived the identity check. That check's verdicts live in
../facescore/scores.json, which is gitignored with the rest of the scoring
output, so this writes the part the world depends on to a committed file.

    uv run python pool_index.py
"""
import argparse, hashlib, json
from pathlib import Path

POOL = Path("pool_manifest.json")
SCORES = Path("../facescore/scores.json")
OUT = Path("pool_index.json")


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scores", type=Path, default=SCORES)
    ap.add_argument("--out", type=Path, default=OUT)
    args = ap.parse_args()

    faces = json.loads(POOL.read_text())["faces"]
    scores = json.loads(args.scores.read_text())
    manifest = {v["id"]: v for v in
                json.loads(Path("variant_manifest.json").read_text())["variants"]}
    kept = {}
    for v in scores["variants"]:
        if not v.get("kept"):
            continue
        if v["base_id"] in kept:
            raise SystemExit(f"{v['base_id']} has two kept variants; the world expects one")
        # The variant's sha256 comes from the generation manifest, not the
        # score file: the score file records what was scored, the manifest
        # what was made, and they must agree before either is trusted.
        m = manifest[v["id"]]
        # face_box is YuNet's detection in the variant's own pixels (x1, y1, x2, y2):
        # the simulator scales and places the figure by it and hands it on,
        # transformed, as the trace's box on the evidence frame.
        kept[v["base_id"]] = {"id": v["id"], "kind": v["kind"], "file": m["file"],
                              "sha256": m["sha256"], "score": round(v["score"], 4),
                              "face_box": [round(c, 1) for c in v["face_box"]]}

    out = {
        "note": "Base faces with their kept variant (null when the variant failed the "
                "identity check). Ages are what each face's prompt asked for.",
        "sources": {"pool_manifest.json": sha256(POOL),
                    "variant_manifest.json": sha256(Path("variant_manifest.json")),
                    "kept_variants": len(kept)},
        "faces": [{"id": f["id"], "sex": f["sex"], "age": f["age"], "file": f["file"],
                   "sha256": f["sha256"], "variant": kept.get(f["id"])} for f in faces],
    }
    args.out.write_text(json.dumps(out, indent=1) + "\n")
    print(f"wrote {args.out}: {len(faces)} faces, {len(kept)} with a kept variant")


if __name__ == "__main__":
    main()
