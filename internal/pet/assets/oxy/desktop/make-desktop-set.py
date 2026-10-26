#!/usr/bin/env python3
"""Build Oxy's high-resolution desktop frame set from a hi-res sprite sheet.

Input: any sprite sheet + sheet.json in the pixel set's layout (one animation
per row, cells of `cell` x `cellHeight` px, each row's `frames` and `ms`),
with the same animations and frame counts as ../pixel/sheet.json. The cell
size is read from the manifest; nothing here assumes one.

Output, in <outdir> (normally this folder): one palette PNG strip per
animation (<anim>.png, frames left to right) and manifest.json, which
`ox pet enable` embeds and slices into hooks/hires/<anim>/<i>.png plus the
`hires` field of hooks/frames.ts:

    python3 make-desktop-set.py <sheet.png> <sheet.json> <outdir>

Each animation is cropped to the union of its frames' visible pixels, so the
figure stays anchored across frames; the crop box is recorded in uncropped
cell coordinates so every animation lands on the same ground line. "figure"
is the union of blink, wag and wag-big (Oxy alone, no props); "scale" is
hires cell width over the pixel set's cell width.

Each strip gets its own palette. The default, pngquant at 128 colors without
dithering, beat Pillow's 255-color octree with Floyd-Steinberg on the trial
sheet (39.9 vs 38.5 dB PSNR at 5.9 vs 5.1 MB) and, undithered, does not
shimmer from frame to frame. `--quantizer octree` needs only Pillow.

Needs Pillow (python3, not python3 -I: it lives in user site-packages) and,
for the default, the pngquant CLI (brew install pngquant).
"""
import argparse
import json
import os
import subprocess
import sys
import tempfile

from PIL import Image

Image.MAX_IMAGE_PIXELS = None  # a hi-res sheet runs to ~850 Mpx

HERE = os.path.dirname(os.path.abspath(__file__))
FIGURE_ANIMS = ("blink", "wag", "wag-big")
# Alpha at or below this is invisible on any background; ignoring it keeps
# a faint render halo from widening the crop.
VISIBLE_ALPHA = 8


def bbox(img):
    return img.getchannel("A").point(lambda a: 255 if a > VISIBLE_ALPHA else 0).getbbox()


def union(boxes):
    boxes = [b for b in boxes if b]
    if not boxes:
        sys.exit("an animation has no visible pixels")
    return (min(b[0] for b in boxes), min(b[1] for b in boxes),
            max(b[2] for b in boxes), max(b[3] for b in boxes))


def quantize(strip, method, colors, path):
    if method == "octree":
        strip.quantize(colors=min(colors, 255), method=Image.Quantize.FASTOCTREE,
                       dither=Image.Dither.FLOYDSTEINBERG).save(path, optimize=True)
        return
    with tempfile.TemporaryDirectory() as tmp:
        src = os.path.join(tmp, "in.png")
        strip.save(src, compress_level=1)
        # --quality 0-100 so pngquant never refuses a strip; --speed 1 is its best search.
        subprocess.run(["pngquant", str(colors), "--nofs", "--speed", "1", "--quality", "0-100",
                        "--strip", "--force", "--output", path, src], check=True)


def main():
    p = argparse.ArgumentParser(description="Build Oxy's hi-res desktop frame set.")
    p.add_argument("sheet_png")
    p.add_argument("sheet_json")
    p.add_argument("outdir")
    p.add_argument("--quantizer", choices=("pngquant", "octree"), default="pngquant")
    p.add_argument("--colors", type=int, default=128)
    p.add_argument("--pixel-json", default=os.path.join(HERE, "..", "pixel", "sheet.json"),
                   help="the terminal's sheet, for frame counts and scale")
    args = p.parse_args()

    meta = json.load(open(args.sheet_json))
    pixel = json.load(open(args.pixel_json))
    cw, ch = meta["cell"], meta.get("cellHeight") or meta["cell"]
    want = {a["name"]: a["frames"] for a in pixel["animations"]}
    got = {a["name"]: a["frames"] for a in meta["animations"]}
    if want != got:
        sys.exit(f"frame counts differ from {args.pixel_json}: "
                 f"{ {n: (want.get(n), got.get(n)) for n in set(want) | set(got) if want.get(n) != got.get(n)} }")

    os.makedirs(args.outdir, exist_ok=True)
    sheet = Image.open(args.sheet_png).convert("RGBA")
    anims, boxes = {}, {}
    for a in meta["animations"]:
        y = a["row"] * ch
        frames = [sheet.crop((i * cw, y, (i + 1) * cw, y + ch)) for i in range(a["frames"])]
        box = union([bbox(f) for f in frames])
        boxes[a["name"]] = box
        w, h = box[2] - box[0], box[3] - box[1]
        strip = Image.new("RGBA", (w * len(frames), h))
        for i, f in enumerate(frames):
            strip.paste(f.crop(box), (i * w, 0))
        quantize(strip, args.quantizer, args.colors, os.path.join(args.outdir, a["name"] + ".png"))
        anims[a["name"]] = {"frames": a["frames"], "x": box[0], "y": box[1], "width": w, "height": h}
        print(f"{a['name']}: {a['frames']} frames, crop {w}x{h}", file=sys.stderr)

    fig = union([boxes[n] for n in FIGURE_ANIMS])
    scale = cw / pixel["cell"]
    manifest = {
        "cell": {"width": cw, "height": ch},
        "scale": int(scale) if scale == int(scale) else round(scale, 4),
        "figure": {"x": fig[0], "y": fig[1], "width": fig[2] - fig[0], "height": fig[3] - fig[1]},
        "animations": anims,
    }
    with open(os.path.join(args.outdir, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=2, sort_keys=True)
        f.write("\n")


if __name__ == "__main__":
    main()
