"""Tile the face pool into one labelled sheet, for eyeballing a whole run.

The pool itself stays in the gitignored spike_out/; this writes the one image
meant to be looked at and reviewed, into samples/.

    uv run python make_contact_sheet.py
"""
import argparse, json
from pathlib import Path

from PIL import Image, ImageDraw

MANIFEST = Path("pool_manifest.json")
OUT = Path("samples/pool_contact_sheet.png")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cols", type=int, default=6)
    ap.add_argument("--thumb", type=int, default=256)
    ap.add_argument("--out", type=Path, default=OUT)
    ap.add_argument("--group-by", default=None,
                    help="attribute to group into labelled sections, e.g. ethnicity")
    ap.add_argument("--only", default=None,
                    help="attr=value filter, e.g. ethnicity=preta")
    args = ap.parse_args()

    faces = json.loads(MANIFEST.read_text())["faces"]
    faces = [f for f in faces if Path(f["file"]).exists()]
    if args.only:
        k, v = args.only.split("=", 1)
        faces = [f for f in faces if str(f.get(k)) == v]
    if not faces:
        raise SystemExit("no generated faces found; run gen_pool.py first")

    # Grouping is what makes this sheet an evaluation rather than a gallery:
    # the question it has to answer is whether the faces the prompt asked to be
    # dark-skinned rendered dark-skinned, and that is only visible when they sit
    # together under their own heading instead of scattered through the pool.
    if args.group_by:
        faces.sort(key=lambda f: (str(f.get(args.group_by)), f["id"]))
        groups, seen = [], None
        for f in faces:
            g = str(f.get(args.group_by))
            if g != seen:
                groups.append((g, []))
                seen = g
            groups[-1][1].append(f)
    else:
        groups = [(None, faces)]

    cols = min(args.cols, max(len(g) for _, g in groups))
    label_h, pad, head_h = 22, 4, 0 if not args.group_by else 26
    cell_w, cell_h = args.thumb + pad, args.thumb + label_h + pad
    rows_total = sum(-(-len(g) // cols) for _, g in groups)
    height = rows_total * cell_h + len(groups) * head_h + pad
    sheet = Image.new("RGB", (cols * cell_w + pad, height), "white")
    draw = ImageDraw.Draw(sheet)

    y0 = pad
    for gname, group in groups:
        if gname is not None:
            draw.text((pad, y0 + 6), f"{args.group_by} = {gname}   (n={len(group)})", fill="black")
            y0 += head_h
        for i, f in enumerate(group):
            x, y = pad + (i % cols) * cell_w, y0 + (i // cols) * cell_h
            img = Image.open(f["file"]).convert("RGB").resize((args.thumb, args.thumb), Image.LANCZOS)
            sheet.paste(img, (x, y))
            sex = "F" if f["sex"] == "female" else "M"
            # The asked-for tone is on the label so the sheet compares the
            # request against the render, not just shows the render.
            draw.text((x + 2, y + args.thumb + 4),
                      f"{f['id'].replace('face_', '')} {sex}{f['age']} {f.get('skin_tone', '')}"[:38],
                      fill="black")
        y0 += -(-len(group) // cols) * cell_h

    args.out.parent.mkdir(parents=True, exist_ok=True)
    sheet.save(args.out)
    secs = [f["seconds"] for f in faces]
    flat = [f["id"] for f in faces if f["luma_range"][1] - f["luma_range"][0] < 60]
    print(f"{len(faces)} faces -> {args.out} ({sheet.width}x{sheet.height})")
    print(f"mean {sum(secs) / len(secs):.1f}s, total {sum(secs) / 60:.1f} min")
    print(f"suspect flat images: {flat or 'none'}")


if __name__ == "__main__":
    main()
