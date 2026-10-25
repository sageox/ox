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
