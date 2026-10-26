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
