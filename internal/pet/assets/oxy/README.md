# Oxy sprite sheet

`ox pet enable` renders `pixel/sheet.png` into the Claude Code mod's frames,
one animation per row, cut by `pixel/sheet.json` (cell width and height, and
each animation's row, frame count, and per-frame durations in ms, or a steady
fps). The mod needs every name in `pet.Animations` (`frames.go`): what Oxy
does while Claude works, and one per ox command.

The art lives with the rest of the Oxy sources in
`sageox-monorepo/.context/oxy/oxy-animations/`: one APNG per animation on a
shared 2048×3004 stage (`oxy-<name>.png`, half size as `oxy-<name>-512.png`),
each made by its `make-<name>.py`. `make-sheet.py` there packs them into a
sheet, merging frames into ticks of at least 80 ms and shrinking 6× (cells
of about 160×243, the whole sheet about 2 MB):

```bash
cd sageox-monorepo/.context/oxy/oxy-animations && python3 make-sheet.py sheet 6 80
cd ox && go run ./internal/pet/importsheet -png <…>/oxy-animations/sheet/sheet.png -json <…>/oxy-animations/sheet/sheet.json
```

A single still can stand in for everything while art is in progress:

```bash
go run ./internal/pet/importsheet -base <base.png>
```

`FramesFiles` writes each animation as separate PNG frames and a `cells.json`
file. The small `frames.ts` module holds only timing and sizes, keeping frame
data outside Claude Code's module limits. Check
`TestFramesFiles_StayUnderClaudeLimits` after an import.

The Claude desktop app (and VS Code) draws the same frames, each as an `Svg`
element wrapping the PNG as a base64 data URI. Claude Code caps an `Svg`
source at 131,072 characters, so every frame PNG must stay under about 98 KB;
the same test guards it.

## Desktop set

In the Claude desktop app's Code tab, Oxy stands in a narrow side pane beside
the transcript, with no card behind him: the thinking row is one text line
tall and clips anything taller, and the band above the prompt is a
full-width card a plugin can't restyle. `/oxy small|medium|large` draws his
figure 48, 80 or 120 px tall there; the terminal keeps the thinking line.

The Claude desktop app and VS Code draw Oxy from a high-resolution set in
`desktop/` instead of the 160×207 pixel frames, which the terminal keeps.
`desktop/make-desktop-set.py` builds it from any sheet in the pixel set's
layout (one animation per row, the same animations, each row's `ms` a list
of per-frame durations; the cell size comes from the sheet's own manifest):

```bash
python3 internal/pet/assets/oxy/desktop/make-desktop-set.py <sheet.png> <sheet.json> internal/pet/assets/oxy/desktop
```

It writes one palette PNG strip per animation (`<anim>.png`, frames left to
right) and `manifest.json`: the cell size, `scale` (hi-res cell width over
the pixel cell's), `figure`, and each animation's crop box and `ms`, its
per-frame durations. The hi-res set runs at its own, higher frame rate (up
to 30 fps), so its frame count is `len(ms)` and may differ from the pixel
set's; each loop must last within 10% of the pixel animation's. A set with
no `ms` must match the pixel set frame for frame and borrows its timing.

- **Crop.** Each animation is cut to the union of its frames' visible pixels,
  which keeps about half the pixels and holds the figure still across frames.
  The box is kept in uncropped cell coordinates, so every animation lands on
  the same ground line. `figure` is the union of `blink`, `wag` and `wag-big`,
  Oxy with no props; the mod sizes him by its height, so he stays one size
  whatever he holds.
- **Palette.** Each strip gets its own palette: pngquant, 128 colors, no
  dithering. On the 480×621 trial sheet that scored 39.9 dB PSNR at 5.9 MB
  against 38.5 dB at 5.1 MB for Pillow's 255-color octree with
  Floyd–Steinberg (`--quantizer octree`, needs only Pillow), and undithered
  pixels don't shimmer from frame to frame. 256-color pngquant scored 42.8 dB
  but at 7.7 MB.

`ox pet enable` slices each strip into `hooks/hires/<anim>/<i>.png`, still
palette PNGs, and adds a `hires` field to `hooks/frames.ts` with the cell,
scale, figure, and each animation's crop box and `ms`. Per-frame files lose the matches deflate finds
across a strip's near-identical frames, so the current set's 9.3 MB of strips
(a 300 px figure in 384×497 cells, 2,426 frames at up to 30 fps) installs as
about 51 MB of frames, the largest 29 KB (39 K base64), inside the
131,072-character `Svg` cap. Without `desktop/manifest.json` the build skips
the set and the desktop draws the pixel frames.
`TestFramesFiles_StayUnderClaudeLimits` checks frame counts, the per-frame
cap and the folder's total (80 MB, room for 30 fps).

## Codex sheet

`ox pet enable` also builds a Codex custom pet from the same `sheet.png`
(`codex.go`): one 1536×1872 `spritesheet.png`, a 12×12 grid of 128×156
cells read row-major from the top left, of which Oxy's frames fill the first
137, plus a `pet.json` naming each animation's cells. Codex's five states map
to Oxy's animations:

| Codex state | Oxy animation | Loops |
|---|---|---|
| `idle` | `blink` | yes |
| `running` | `laptop` | yes |
| `waiting` | `wave` | yes |
| `review` | `confetti` | no |
| `failed` | `fail` | no |

The names in Codex's built-in default table alias onto the same art
(`running-right`, `running-left`, `move_right`, `move_left` → `laptop`;
`waving`, `wave`, `jumping`, `bounce` → `wave`; `sad` → `fail`), so none
points at an empty cell. Every animation falls back to `idle`. The full
table is `CodexAnimations`.
