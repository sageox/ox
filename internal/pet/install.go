package pet

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/claudemod"
)

// The mod's identity in Claude Code: plugin `ox-cli-oxy` from the local folder
// marketplace `ox-cli-pets`, which is the mod folder itself. The `ox-cli-`
// prefix marks it as owned by the ox binary (ADR-031): ox rewrites it freely.
// Coworkers still see "Oxy" and type /hatch-pet.
const (
	PluginName      = "ox-cli-oxy"
	MarketplaceName = "ox-cli-pets"
	PluginRef       = PluginName + "@" + MarketplaceName

	framesFile = "hooks/frames.ts"
	// framesDir holds one folder per animation: its PNG frames and cells.json.
	framesDir = "hooks/frames"
	// fsReadLimit is the most `$.fs.read` hands a mod from one file.
	fsReadLimit = 4 << 20
)

//go:embed all:mod
var modFS embed.FS

// Dir is where ox keeps the Oxy mod for Claude Code. Claude reads a local
// folder marketplace in place, so rewriting this folder updates the pet.
func Dir() string {
	return claudemod.Dir("pets", PluginName)
}

// Claude runs the claude CLI. Swapped out in tests.
type Claude = claudemod.Claude

// Result reports what enable, disable, or status found and did.
type Result = claudemod.Result

// FindClaude returns the claude CLI on PATH, or nil when it isn't installed.
func FindClaude() Claude { return claudemod.FindClaude() }

// RecoverSwap restores a mod folder whose refresh stopped between its two
// renames. See claudemod.RecoverSwap.
func RecoverSwap(dir string) error { return claudemod.RecoverSwap(dir) }

// WrittenVersion is the ox version stamped on the mod in dir.
func WrittenVersion(dir string) (string, error) { return claudemod.WrittenVersion(dir) }

// Installer writes the Oxy mod and registers it with Claude Code.
type Installer struct {
	Dir     string
	Version string
	// Claude is nil when the claude CLI isn't installed; the mod is still
	// written and the result carries the steps to finish by hand.
	Claude Claude
	// Frames renders the frames the mod draws; nil renders the embedded
	// sheet (OxyFrames). Tests swap in a tiny sheet.
	Frames func() (*Frames, error)
}

// mod is the generic installer for the Oxy mod: the embedded files plus the
// frames rendered at install time.
func (i Installer) mod() claudemod.Installer {
	render := i.Frames
	if render == nil {
		render = OxyFrames
	}
	return claudemod.Installer{
		Dir: i.Dir, Plugin: PluginName, Marketplace: MarketplaceName,
		Version: i.Version, Claude: i.Claude, FS: modFS, Root: "mod",
		Generated: func() (map[string][]byte, error) {
			frames, err := render()
			if err != nil {
				return nil, err
			}
			return FramesFiles(frames)
		},
	}
}

// Enable writes the mod (replacing any older copy) and installs it into
// Claude Code at user scope. Re-running it refreshes the mod after an ox
// upgrade.
func (i Installer) Enable(ctx context.Context) (*Result, error) { return i.mod().Enable(ctx) }

// Disable uninstalls the mod from Claude Code and removes its folder.
func (i Installer) Disable(ctx context.Context) (*Result, error) { return i.mod().Disable(ctx) }

// Status reports whether the mod files exist and whether Claude Code has it installed.
func (i Installer) Status(ctx context.Context) (*Result, error) { return i.mod().Status(ctx) }

// WriteMod renders the frames and writes the whole mod to i.Dir, swapping the
// new copy in only once it is complete.
func (i Installer) WriteMod() error { return i.mod().WriteMod() }

// FramesFiles renders the frames as the files the mod reads: hooks/frames.ts,
// a small module with every animation's timing and the sizes, plus per
// animation a folder of PNG frames the terminal opens by path and a
// cells.json the mod reads on first use. Nothing heavy sits in the module
// graph, which Claude Code caps at 8 MiB in all and 1 MiB a file; a cells.json
// stays under the 4 MiB one `$.fs.read` returns. Keys are paths relative to
// the mod folder.
func FramesFiles(frames *Frames) (map[string][]byte, error) {
	out := make(map[string][]byte)
	names := make([]string, 0, len(frames.Animations))
	for name := range frames.Animations {
		names = append(names, name)
	}
	sort.Strings(names)

	fields := make([]string, 0, len(names))
	for _, name := range names {
		a := frames.Animations[name]
		for i, b64 := range a.PNG {
			raw, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, fmt.Errorf("decode %s frame %d: %w", name, i, err)
			}
			out[fmt.Sprintf("%s/%s/%d.png", framesDir, name, i)] = raw
		}
		cells, err := json.Marshal(a.Cells)
		if err != nil {
			return nil, fmt.Errorf("encode %s cells: %w", name, err)
		}
		if len(cells) >= fsReadLimit {
			return nil, fmt.Errorf("%s cells are %d bytes, over the %d the mod can read at once", name, len(cells), fsReadLimit)
		}
		out[framesDir+"/"+name+"/cells.json"] = cells
		ms, err := json.Marshal(a.MS)
		if err != nil {
			return nil, fmt.Errorf("encode %s timing: %w", name, err)
		}
		fields = append(fields, fmt.Sprintf("%q: { ms: %s }", name, ms))
	}
	sizes := make([]string, 0, len(Sizes))
	for _, s := range Sizes {
		box := frames.Sizes[s.Name]
		sizes = append(sizes, fmt.Sprintf("%s: { columns: %d, rows: %d, cells: { columns: %d, rows: %d } }",
			s.Name, box.Columns, box.Rows, box.Cells.Columns, box.Cells.Rows))
	}
	var index bytes.Buffer
	index.WriteString(generatedHeader)
	index.WriteString("import type { OxyFrames } from './logic'\n\n")
	fmt.Fprintf(&index, "export const FRAMES: OxyFrames = {\n  aspect: %g,\n  sizes: {\n    %s,\n  },\n  animations: {\n    %s,\n  },\n}\n",
		frames.Aspect, strings.Join(sizes, ",\n    "), strings.Join(fields, ",\n    "))
	out[framesFile] = index.Bytes()
	return out, nil
}

const generatedHeader = "// Generated by `ox pet enable` from the Oxy sprite sheet. Do not edit.\n"
