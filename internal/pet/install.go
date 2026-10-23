package pet

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/paths"
)

// The mod's identity in Claude Code: plugin `ox-cli-oxy` from the local folder
// marketplace `ox-cli-pets`, which is the mod folder itself. The `ox-cli-`
// prefix marks it as owned by the ox binary (ADR-031): ox rewrites it freely.
// Coworkers still see "Oxy" and type /oxy.
const (
	PluginName      = "ox-cli-oxy"
	MarketplaceName = "ox-cli-pets"
	PluginRef       = PluginName + "@" + MarketplaceName

	versionPlaceholder = "{{VERSION}}"
	framesFile         = "hooks/frames.ts"
	// framesDir holds one folder per animation: its PNG frames and cells.json.
	framesDir = "hooks/frames"
	// fsReadLimit is the most `$.fs.read` hands a mod from one file.
	fsReadLimit   = 4 << 20
	claudeTimeout = 2 * time.Minute
)

//go:embed all:mod
var modFS embed.FS

// Dir is where ox keeps the Oxy mod for Claude Code. Claude reads a local
// folder marketplace in place, so rewriting this folder updates the pet.
func Dir() string {
	return filepath.Join(paths.DataDir(), "pets", "claude-code", PluginName)
}

// Claude runs the claude CLI. Swapped out in tests.
type Claude interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type claudeCLI struct{ bin string }

// FindClaude returns the claude CLI on PATH, or nil when it isn't installed.
func FindClaude() Claude {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil
	}
	return claudeCLI{bin: bin}
}

func (c claudeCLI) Run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, claudeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.bin, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("claude %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

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

// Result reports what enable, disable, or status found and did.
type Result struct {
	Dir              string   `json:"dir"`
	Plugin           string   `json:"plugin"`
	Version          string   `json:"version,omitempty"`
	FilesPresent     bool     `json:"files_present"`
	Installed        bool     `json:"installed"`
	InstalledVersion string   `json:"installed_version,omitempty"`
	ClaudeFound      bool     `json:"claude_found"`
	ManualSteps      []string `json:"manual_steps,omitempty"`
}

// Enable writes the mod (replacing any older copy) and installs it into
// Claude Code at user scope. Re-running it refreshes the mod after an ox
// upgrade.
func (i Installer) Enable(ctx context.Context) (*Result, error) {
	if err := i.WriteMod(); err != nil {
		return nil, err
	}
	res := &Result{Dir: i.Dir, Plugin: PluginRef, Version: i.Version, FilesPresent: true, ClaudeFound: i.Claude != nil}
	if i.Claude == nil {
		res.ManualSteps = []string{
			"/plugin marketplace add " + i.Dir,
			"/plugin install " + PluginRef,
		}
		return res, nil
	}
	if _, err := i.Claude.Run(ctx, "plugin", "validate", i.Dir); err != nil {
		return nil, fmt.Errorf("the Oxy mod did not validate: %w", err)
	}
	hasMarketplace, err := i.hasMarketplace(ctx)
	if err != nil {
		return nil, err
	}
	if !hasMarketplace {
		if _, err := i.Claude.Run(ctx, "plugin", "marketplace", "add", i.Dir); err != nil {
			return nil, err
		}
	}
	installed, version, err := i.installed(ctx)
	if err != nil {
		return nil, err
	}
	if installed && version != "" && version != i.Version {
		// Claude Code records a plugin's version when it installs it, so an
		// ox upgrade leaves the old one in `plugin list` until it reinstalls.
		if _, err := i.Claude.Run(ctx, "plugin", "uninstall", PluginRef, "--scope", "user"); err != nil {
			return nil, err
		}
		installed = false
	}
	if !installed {
		if _, err := i.Claude.Run(ctx, "plugin", "install", PluginRef, "--scope", "user"); err != nil {
			return nil, err
		}
	}
	res.Installed = true
	res.InstalledVersion = i.Version
	return res, nil
}

// Disable uninstalls the mod from Claude Code and removes its folder.
func (i Installer) Disable(ctx context.Context) (*Result, error) {
	res := &Result{Dir: i.Dir, Plugin: PluginRef, ClaudeFound: i.Claude != nil}
	if i.Claude != nil {
		installed, _, err := i.installed(ctx)
		if err != nil {
			return nil, err
		}
		if installed {
			if _, err := i.Claude.Run(ctx, "plugin", "uninstall", PluginRef, "--scope", "user"); err != nil {
				return nil, err
			}
		}
		hasMarketplace, err := i.hasMarketplace(ctx)
		if err != nil {
			return nil, err
		}
		if hasMarketplace {
			if _, err := i.Claude.Run(ctx, "plugin", "marketplace", "remove", MarketplaceName); err != nil {
				return nil, err
			}
		}
	} else {
		res.ManualSteps = []string{"/plugin uninstall " + PluginRef, "/plugin marketplace remove " + MarketplaceName}
	}
	if err := os.RemoveAll(i.Dir); err != nil {
		return nil, fmt.Errorf("remove %s: %w", i.Dir, err)
	}
	return res, nil
}

// Status reports whether the mod files exist and whether Claude Code has it installed.
func (i Installer) Status(ctx context.Context) (*Result, error) {
	res := &Result{Dir: i.Dir, Plugin: PluginRef, Version: i.Version, ClaudeFound: i.Claude != nil}
	if v, err := WrittenVersion(i.Dir); err == nil {
		res.FilesPresent = true
		res.InstalledVersion = v
	}
	if i.Claude != nil {
		installed, version, err := i.installed(ctx)
		if err != nil {
			return nil, err
		}
		res.Installed = installed
		if version != "" {
			res.InstalledVersion = version
		}
	}
	return res, nil
}

// WriteMod renders the frames and writes the whole mod to i.Dir, swapping the
// new copy in only once it is complete.
func (i Installer) WriteMod() error {
	render := i.Frames
	if render == nil {
		render = OxyFrames
	}
	if err := RecoverSwap(i.Dir); err != nil {
		return err
	}
	frames, err := render()
	if err != nil {
		return err
	}
	generated, err := FramesFiles(frames)
	if err != nil {
		return err
	}
	parent := filepath.Dir(i.Dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, "."+PluginName+"-new-")
	if err != nil {
		return fmt.Errorf("stage mod: %w", err)
	}
	defer os.RemoveAll(staging)

	err = fs.WalkDir(modFS, "mod", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := modFS.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "mod/")
		if path.Base(rel) == "plugin.json" {
			data = bytes.ReplaceAll(data, []byte(versionPlaceholder), []byte(i.Version))
		}
		return writeFile(filepath.Join(staging, filepath.FromSlash(rel)), data)
	})
	if err != nil {
		return fmt.Errorf("write mod: %w", err)
	}
	for rel, data := range generated {
		if err := writeFile(filepath.Join(staging, filepath.FromSlash(rel)), data); err != nil {
			return fmt.Errorf("write frames: %w", err)
		}
	}

	old := i.Dir + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(i.Dir, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("replace %s: %w", i.Dir, err)
	}
	if err := os.Rename(staging, i.Dir); err != nil {
		_ = os.Rename(old, i.Dir)
		return fmt.Errorf("install %s: %w", i.Dir, err)
	}
	_ = os.RemoveAll(old)
	return nil
}

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

// writeFile writes one file of the staging folder. Plain writes: the folder
// as a whole is swapped in by rename once complete, so nothing reads a
// half-written file, and syncing each of the hundreds of frames would make
// enable take seconds.
func writeFile(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// RecoverSwap finishes a refresh that stopped between WriteMod's two renames:
// the live folder is gone but the complete previous copy sits beside it as
// dir+".old", so it is put back. Anything else is left alone.
func RecoverSwap(dir string) error {
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	old := dir + ".old"
	if _, err := os.Stat(filepath.Join(old, ".claude-plugin", "plugin.json")); err != nil {
		return nil
	}
	if err := os.Rename(old, dir); err != nil {
		return fmt.Errorf("restore %s: %w", dir, err)
	}
	return nil
}

// WrittenVersion is the ox version stamped on the mod in dir, or an error when
// there is no readable mod there.
func WrittenVersion(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil {
		return "", err
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", err
	}
	return m.Version, nil
}

func (i Installer) hasMarketplace(ctx context.Context) (bool, error) {
	out, err := i.Claude.Run(ctx, "plugin", "marketplace", "list", "--json")
	if err != nil {
		return false, err
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := unmarshalList(out, &list); err != nil {
		return false, fmt.Errorf("read claude marketplace list: %w", err)
	}
	for _, m := range list {
		if m.Name == MarketplaceName {
			return true, nil
		}
	}
	return false, nil
}

func (i Installer) installed(ctx context.Context) (bool, string, error) {
	out, err := i.Claude.Run(ctx, "plugin", "list", "--json")
	if err != nil {
		return false, "", err
	}
	var list []struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if err := unmarshalList(out, &list); err != nil {
		return false, "", fmt.Errorf("read claude plugin list: %w", err)
	}
	for _, p := range list {
		if p.ID == PluginRef {
			return true, p.Version, nil
		}
	}
	return false, "", nil
}

// unmarshalList decodes the JSON array in a claude CLI `--json` output,
// skipping any notice the CLI printed before it.
func unmarshalList(out string, v any) error {
	start := strings.Index(out, "[")
	if start < 0 {
		return fmt.Errorf("no JSON list in output: %q", strings.TrimSpace(out))
	}
	return json.Unmarshal([]byte(out[start:]), v)
}
