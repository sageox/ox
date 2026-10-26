// Package claudemod installs a Claude Code mod that ox owns: it writes the
// mod folder (a local folder marketplace holding one plugin), swaps it in
// atomically, and registers it with Claude Code through the claude CLI.
//
// The package knows nothing about any particular mod. Callers hand it the
// mod's files (an fs.FS), its plugin and marketplace names, and any files
// generated at install time.
package claudemod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/paths"
)

const (
	// versionPlaceholder in a mod's plugin.json is replaced with the ox
	// version when the mod is written.
	versionPlaceholder = "{{VERSION}}"
	claudeTimeout      = 2 * time.Minute
)

// Dir is where ox keeps a Claude Code mod of the given kind (e.g. "pets").
// Claude reads a local folder marketplace in place, so rewriting this folder
// updates the mod.
func Dir(kind, plugin string) string {
	return filepath.Join(paths.DataDir(), kind, "claude-code", plugin)
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

// Installer writes one mod and registers it with Claude Code.
type Installer struct {
	// Dir is the mod folder, which is also the local marketplace's folder.
	Dir string
	// Plugin and Marketplace are the mod's names in Claude Code.
	Plugin      string
	Marketplace string
	// Version is stamped into plugin.json in place of {{VERSION}}.
	Version string
	// Claude is nil when the claude CLI isn't installed; the mod is still
	// written and the result carries the steps to finish by hand.
	Claude Claude
	// FS holds the mod's static files under Root (e.g. "mod").
	FS   fs.FS
	Root string
	// Generated renders files written after FS's, keyed by slash path
	// relative to the mod folder. Nil means none.
	Generated func() (map[string][]byte, error)
}

// Ref is the plugin's id in Claude Code: plugin@marketplace.
func (i Installer) Ref() string { return i.Plugin + "@" + i.Marketplace }

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
	ref := i.Ref()
	res := &Result{Dir: i.Dir, Plugin: ref, Version: i.Version, FilesPresent: true, ClaudeFound: i.Claude != nil}
	if i.Claude == nil {
		res.ManualSteps = []string{
			"/plugin marketplace add " + i.Dir,
			"/plugin install " + ref,
		}
		return res, nil
	}
	if _, err := i.Claude.Run(ctx, "plugin", "validate", i.Dir); err != nil {
		return nil, fmt.Errorf("the %s mod did not validate: %w", i.Plugin, err)
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
		if _, err := i.Claude.Run(ctx, "plugin", "uninstall", ref, "--scope", "user"); err != nil {
			return nil, err
		}
		installed = false
	}
	if !installed {
		if _, err := i.Claude.Run(ctx, "plugin", "install", ref, "--scope", "user"); err != nil {
			return nil, err
		}
	}
	res.Installed = true
	res.InstalledVersion = i.Version
	return res, nil
}

// Disable uninstalls the mod from Claude Code and removes its folder.
func (i Installer) Disable(ctx context.Context) (*Result, error) {
	ref := i.Ref()
	res := &Result{Dir: i.Dir, Plugin: ref, ClaudeFound: i.Claude != nil}
	if i.Claude != nil {
		installed, _, err := i.installed(ctx)
		if err != nil {
			return nil, err
		}
		if installed {
			if _, err := i.Claude.Run(ctx, "plugin", "uninstall", ref, "--scope", "user"); err != nil {
				return nil, err
			}
		}
		hasMarketplace, err := i.hasMarketplace(ctx)
		if err != nil {
			return nil, err
		}
		if hasMarketplace {
			if _, err := i.Claude.Run(ctx, "plugin", "marketplace", "remove", i.Marketplace); err != nil {
				return nil, err
			}
		}
	} else {
		res.ManualSteps = []string{"/plugin uninstall " + ref, "/plugin marketplace remove " + i.Marketplace}
	}
	if err := os.RemoveAll(i.Dir); err != nil {
		return nil, fmt.Errorf("remove %s: %w", i.Dir, err)
	}
	return res, nil
}

// Status reports whether the mod files exist and whether Claude Code has it installed.
func (i Installer) Status(ctx context.Context) (*Result, error) {
	res := &Result{Dir: i.Dir, Plugin: i.Ref(), Version: i.Version, ClaudeFound: i.Claude != nil}
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

// WriteMod renders the generated files and writes the whole mod to i.Dir,
// swapping the new copy in only once it is complete.
func (i Installer) WriteMod() error {
	if i.FS == nil {
		return errors.New("write mod: no mod files")
	}
	if err := RecoverSwap(i.Dir); err != nil {
		return err
	}
	var generated map[string][]byte
	if i.Generated != nil {
		var err error
		if generated, err = i.Generated(); err != nil {
			return err
		}
	}
	parent := filepath.Dir(i.Dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, "."+i.Plugin+"-new-")
	if err != nil {
		return fmt.Errorf("stage mod: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	root := i.Root
	if root == "" {
		root = "."
	}
	err = fs.WalkDir(i.FS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(i.FS, p)
		if err != nil {
			return err
		}
		rel := p
		if root != "." {
			rel = strings.TrimPrefix(p, root+"/")
		}
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
			return fmt.Errorf("write generated files: %w", err)
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

// writeFile writes one file of the staging folder. Plain writes: the folder
// as a whole is swapped in by rename once complete, so nothing reads a
// half-written file, and syncing each of hundreds of files would make enable
// take seconds.
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
		if m.Name == i.Marketplace {
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
	ref := i.Ref()
	for _, p := range list {
		if p.ID == ref {
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
