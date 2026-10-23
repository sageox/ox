package pet

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Oxy's identity as a Codex CLI custom pet: the folder under
// $CODEX_HOME/pets/ and the value of `pet` in config.toml's [tui] table.
const (
	CodexPetID       = "oxy"
	CodexPetSelector = "custom:" + CodexPetID

	codexManifestFile    = "pet.json"
	codexSpritesheetFile = "spritesheet.png"
	codexSkillName       = "oxy"
	codexSkillVersionTag = "ox-cli-oxy ver:"
)

// CodexDir is where Oxy lives inside a Codex home: <home>/pets/oxy.
func CodexDir(codexHome string) string {
	return filepath.Join(codexHome, "pets", CodexPetID)
}

func codexConfigPath(codexHome string) string {
	return filepath.Join(codexHome, "config.toml")
}

func codexSkillPath(codexHome string) string {
	return filepath.Join(codexHome, "skills", codexSkillName, "SKILL.md")
}

// CodexInstaller writes Oxy into a Codex home and makes him the active pet.
// The caller resolves Home (nativeimport.CodexHome), so this package stays
// free of session dependencies.
type CodexInstaller struct {
	Home    string
	Version string
	// Sheet builds the spritesheet and pet.json; nil uses OxyCodexSheet.
	// Tests swap in a tiny builder.
	Sheet func(version string) (*CodexSheet, error)
}

// CodexResult reports what enable, disable, or status found and did.
type CodexResult struct {
	Dir              string `json:"dir"`
	ConfigPath       string `json:"config_path"`
	SkillPath        string `json:"skill_path"`
	Version          string `json:"version,omitempty"`
	FilesPresent     bool   `json:"files_present"`
	InstalledVersion string `json:"installed_version,omitempty"`
	// Selected is true when config.toml's tui.pet is CodexPetSelector.
	Selected bool `json:"selected"`
	// SelectedPet is whatever tui.pet currently holds.
	SelectedPet string `json:"selected_pet,omitempty"`
	// Guidance is one or two sentences for an AI coworker to relay.
	Guidance string `json:"guidance,omitempty"`
}

// CodexSizeNote explains what Codex decides on its own: the pet's size and
// place are fixed in Codex's TUI (about five rows, right edge above the
// composer), so the sizes and pinned animations `/oxy` offers in Claude Code
// have no Codex counterpart.
const CodexSizeNote = "Codex draws Oxy at its own size and place (about five rows, right of the composer); " +
	"`/oxy small|medium|large` and pinning an animation are Claude Code options."

const (
	codexEnableGuidance = "Oxy is Codex's terminal pet now. Restart Codex, or open /pets, to see him. " +
		"He needs a terminal with Kitty graphics or Sixel (Ghostty, kitty, WezTerm, iTerm2 3.6+), not tmux. " +
		CodexSizeNote
	codexCurrentGuidance  = "Oxy is Codex's current terminal pet. " + CodexSizeNote
	codexDisableGuidance  = "Oxy is no longer Codex's terminal pet. Restart Codex for the change to show."
	codexStaleGuidance    = "Run `ox pet enable` to refresh Oxy for Codex."
	codexUnselectGuidance = "Oxy is installed but not Codex's current pet; pick him in /pets or run `ox pet enable`."
	codexMissingGuidance  = "Oxy is not installed for Codex. Run `ox pet enable` to add him."
)

func (i CodexInstaller) result() *CodexResult {
	return &CodexResult{
		Dir:        CodexDir(i.Home),
		ConfigPath: codexConfigPath(i.Home),
		SkillPath:  codexSkillPath(i.Home),
		Version:    i.Version,
	}
}

// Enable writes Oxy's files, selects him in config.toml, and writes the
// $oxy skill. Re-running it refreshes everything after an ox upgrade.
func (i CodexInstaller) Enable(_ context.Context) (*CodexResult, error) {
	if i.Home == "" {
		return nil, errors.New("codex home is not set")
	}
	cfg := codexConfigPath(i.Home)
	// Check the config parses before touching anything, so a broken
	// config.toml leaves the machine exactly as it was.
	if _, err := readCodexConfig(cfg); err != nil {
		return nil, err
	}
	if err := i.WriteFiles(); err != nil {
		return nil, err
	}
	if err := selectCodexPet(cfg, CodexPetSelector); err != nil {
		return nil, err
	}
	if err := writeCodexSkill(codexSkillPath(i.Home), i.Version); err != nil {
		return nil, err
	}
	res := i.result()
	res.FilesPresent = true
	res.InstalledVersion = i.Version
	res.Selected = true
	res.SelectedPet = CodexPetSelector
	res.Guidance = codexEnableGuidance
	return res, nil
}

// Disable deselects Oxy (only if he is the selected pet), removes his folder
// and the $oxy skill. Other pets are never touched.
func (i CodexInstaller) Disable(_ context.Context) (*CodexResult, error) {
	if i.Home == "" {
		return nil, errors.New("codex home is not set")
	}
	res := i.result()
	// Deselect first: if config.toml can't be edited, Codex is not left
	// pointing at a pet whose files are gone.
	if err := deselectCodexPet(res.ConfigPath, CodexPetSelector); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(res.Dir); err != nil {
		return nil, fmt.Errorf("remove %s: %w", res.Dir, err)
	}
	_ = os.RemoveAll(res.Dir + ".old")
	skillDir := filepath.Dir(res.SkillPath)
	if err := os.RemoveAll(skillDir); err != nil {
		return nil, fmt.Errorf("remove %s: %w", skillDir, err)
	}
	if cfg, err := readCodexConfig(res.ConfigPath); err == nil {
		res.SelectedPet = codexSelectedPet(cfg)
	}
	res.Guidance = codexDisableGuidance
	return res, nil
}

// Status reports whether Oxy's files are present, which version wrote them,
// and whether config.toml selects him.
func (i CodexInstaller) Status(_ context.Context) (*CodexResult, error) {
	if i.Home == "" {
		return nil, errors.New("codex home is not set")
	}
	res := i.result()
	if v, err := CodexWrittenVersion(res.Dir); err == nil {
		res.FilesPresent = true
		res.InstalledVersion = v
	}
	cfg, err := readCodexConfig(res.ConfigPath)
	if err != nil {
		return nil, err
	}
	res.SelectedPet = codexSelectedPet(cfg)
	res.Selected = res.SelectedPet == CodexPetSelector

	switch {
	case !res.FilesPresent:
		res.Guidance = codexMissingGuidance
	case res.InstalledVersion != i.Version || codexSkillVersion(res.SkillPath) != i.Version:
		res.Guidance = codexStaleGuidance
	case !res.Selected:
		res.Guidance = codexUnselectGuidance
	default:
		res.Guidance = codexCurrentGuidance
	}
	return res, nil
}

// WriteFiles writes spritesheet.png and pet.json to CodexDir(Home), staging
// them beside the target and swapping the folder in only once complete.
func (i CodexInstaller) WriteFiles() error {
	build := i.Sheet
	if build == nil {
		build = OxyCodexSheet
	}
	dir := CodexDir(i.Home)
	if err := recoverCodexSwap(dir); err != nil {
		return err
	}
	sheet, err := build(i.Version)
	if err != nil {
		return err
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, "."+CodexPetID+"-new-")
	if err != nil {
		return fmt.Errorf("stage codex pet: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := os.Chmod(staging, 0o755); err != nil {
		return fmt.Errorf("stage codex pet: %w", err)
	}
	if err := writeFile(filepath.Join(staging, codexSpritesheetFile), sheet.PNG); err != nil {
		return fmt.Errorf("write codex spritesheet: %w", err)
	}
	if err := writeFile(filepath.Join(staging, codexManifestFile), sheet.Manifest); err != nil {
		return fmt.Errorf("write codex pet.json: %w", err)
	}

	old := dir + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("replace %s: %w", dir, err)
	}
	if err := os.Rename(staging, dir); err != nil {
		_ = os.Rename(old, dir)
		return fmt.Errorf("install %s: %w", dir, err)
	}
	_ = os.RemoveAll(old)
	return nil
}

// recoverCodexSwap finishes a refresh that stopped between WriteFiles' two
// renames: the live folder is gone but a complete previous copy (one with a
// pet.json) sits beside it as dir+".old", so it is put back.
func recoverCodexSwap(dir string) error {
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	old := dir + ".old"
	if _, err := os.Stat(filepath.Join(old, codexManifestFile)); err != nil {
		return nil
	}
	if err := os.Rename(old, dir); err != nil {
		return fmt.Errorf("restore %s: %w", dir, err)
	}
	return nil
}

// CodexWrittenVersion is the ox version stamped in <dir>/pet.json, or an
// error when there is no readable pet.json there.
func CodexWrittenVersion(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, codexManifestFile))
	if err != nil {
		return "", err
	}
	return CodexManifestVersion(data)
}

// --- the $oxy skill ---

// codexSkill is a thin relay to `ox pet`; behaviour lives in the command's
// guidance. Codex requires the frontmatter to open the file, so the version
// stamp is the first line after it.
const codexSkill = `---
name: oxy
description: "Show, hide, or check Oxy, SageOx's mascot, as the Codex terminal pet."
disable-model-invocation: true
---
<!-- ` + codexSkillVersionTag + ` %s -->

> **Explicit invocation only.** Run this when the user asks for it by name.

Run the ox command for what the user said and relay its ` + "`guidance`" + ` field:

- ` + "`$oxy on`" + `: ` + "`ox pet enable --host codex --json`" + `
- ` + "`$oxy off`" + `: ` + "`ox pet disable --host codex --json`" + `
- ` + "`$oxy status`" + `, or any other word (a size such as small, medium or large,
  auto, or an animation name): ` + "`ox pet status --host codex --json`" + `. The guidance
  explains what Codex decides on its own.

Codex loads the pet at startup, so a change shows after a restart or from /pets.
`

func writeCodexSkill(path, version string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, fmt.Appendf(nil, codexSkill, version), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

var codexSkillVersionRe = regexp.MustCompile(`<!-- ` + regexp.QuoteMeta(codexSkillVersionTag) + ` (\S*) -->`)

// codexSkillVersion is the ox version stamped on the skill, or "" when the
// skill is missing or unstamped.
func codexSkillVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if m := codexSkillVersionRe.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// --- config.toml ---

// readCodexConfig parses config.toml. A missing or empty file is an empty
// config; one that does not parse is an error naming the path.
func readCodexConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parseCodexConfig(path, data)
}

func parseCodexConfig(path string, data []byte) (map[string]any, error) {
	cfg := map[string]any{}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

func codexSelectedPet(cfg map[string]any) string {
	tui, _ := cfg["tui"].(map[string]any)
	pet, _ := tui["pet"].(string)
	return pet
}

// selectCodexPet sets tui.pet to selector with a line-level edit, leaving
// every other byte of config.toml as it was. The result is parsed and must
// differ from the original only in tui.pet, or nothing is written.
func selectCodexPet(path, selector string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		return os.WriteFile(path, []byte(codexTUIBlock(selector)), 0o600)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	before, err := parseCodexConfig(path, data)
	if err != nil {
		return err
	}
	if codexSelectedPet(before) == selector {
		return nil
	}
	out := setTUIPet(data, selector)
	if err := verifyCodexEdit(path, before, out, selector); err != nil {
		return err
	}
	return writeCodexConfig(path, out)
}

// deselectCodexPet removes tui.pet when it names Oxy (selector, or the bare
// id); any other value, a missing key, or a missing file is left alone.
func deselectCodexPet(path, selector string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	before, err := parseCodexConfig(path, data)
	if err != nil {
		return err
	}
	if current := codexSelectedPet(before); current != selector && current != CodexPetID {
		return nil
	}
	out := removeTUIPet(data)
	if err := verifyCodexEdit(path, before, out, ""); err != nil {
		return err
	}
	return writeCodexConfig(path, out)
}

// verifyCodexEdit parses the edited config and checks it equals the original
// with tui.pet set to pet (or removed when pet is ""). A layout the line
// editor does not understand (an inline [tui], a dotted tui.pet key, a
// header inside a multiline string) fails here instead of corrupting the file.
func verifyCodexEdit(path string, before map[string]any, out []byte, pet string) error {
	after, err := parseCodexConfig(path, out)
	if err != nil {
		return fmt.Errorf("could not set the Codex pet in %s automatically; set `pet = %q` under [tui] by hand: %w", path, pet, err)
	}
	want := cloneTOML(before)
	tui, _ := want["tui"].(map[string]any)
	if pet == "" {
		delete(tui, "pet")
	} else {
		if tui == nil {
			tui = map[string]any{}
			want["tui"] = tui
		}
		tui["pet"] = pet
	}
	if !reflect.DeepEqual(want, after) {
		if pet == "" {
			return fmt.Errorf("could not remove the Codex pet from %s automatically; remove `pet` under [tui] by hand", path)
		}
		return fmt.Errorf("could not set the Codex pet in %s automatically; set `pet = %q` under [tui] by hand", path, pet)
	}
	return nil
}

func cloneTOML(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			v = cloneTOML(sub)
		}
		out[k] = v
	}
	return out
}

func writeCodexConfig(path string, data []byte) error {
	// WriteFile on the existing path keeps its mode and follows a symlinked
	// config.toml (dotfile managers) instead of replacing the link.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func codexTUIBlock(selector string) string {
	return "[tui]\npet = " + tomlString(selector) + "\n"
}

func tomlString(s string) string {
	return fmt.Sprintf("%q", s)
}

// codexPetLine matches a `pet = ...` assignment, capturing its prefix up to
// the value.
var codexPetLine = regexp.MustCompile(`^(\s*(?:pet|"pet"|'pet')\s*=\s*)`)

// tomlHeader returns the table name of a `[name]` or `[[name]]` header
// line, and whether line is a header at all.
func tomlHeader(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") {
		return "", false
	}
	if i := strings.Index(t, "#"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	t = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	if strings.HasPrefix(t, "[") {
		return "[" + strings.TrimSpace(strings.Trim(t, "[]")) + "]", true
	}
	return strings.TrimSpace(t), true
}

// tuiPetLine finds the [tui] header line and the `pet` line inside that
// table (-1 when absent). Lines keep their trailing newline.
func tuiPetLine(lines []string) (header, pet int) {
	header, pet = -1, -1
	inTUI := false
	for n, line := range lines {
		if name, ok := tomlHeader(line); ok {
			inTUI = name == "tui"
			if inTUI && header < 0 {
				header = n
			}
			continue
		}
		if inTUI && pet < 0 && codexPetLine.MatchString(line) {
			pet = n
		}
	}
	return header, pet
}

func setTUIPet(data []byte, selector string) []byte {
	lines := strings.SplitAfter(string(data), "\n")
	header, pet := tuiPetLine(lines)
	value := tomlString(selector)
	switch {
	case pet >= 0:
		line := lines[pet]
		prefix := codexPetLine.FindString(line)
		rest := line[len(prefix):]
		lines[pet] = prefix + value + rest[tomlValueEnd(rest):]
	case header >= 0:
		h := lines[header]
		if !strings.HasSuffix(h, "\n") {
			h += "\n"
		}
		lines[header] = h + "pet = " + value + "\n"
	default:
		s := string(data)
		switch {
		case strings.TrimSpace(s) == "":
			return []byte(s + codexTUIBlock(selector))
		case !strings.HasSuffix(s, "\n"):
			s += "\n"
		}
		return []byte(s + "\n" + codexTUIBlock(selector))
	}
	return []byte(strings.Join(lines, ""))
}

func removeTUIPet(data []byte) []byte {
	lines := strings.SplitAfter(string(data), "\n")
	_, pet := tuiPetLine(lines)
	if pet < 0 {
		return data
	}
	return []byte(strings.Join(append(lines[:pet:pet], lines[pet+1:]...), ""))
}

// tomlValueEnd is the length of the single-line TOML value at the start of
// s: a basic or literal string through its closing quote, otherwise up to a
// comment or the end of the line.
func tomlValueEnd(s string) int {
	if s == "" {
		return 0
	}
	switch s[0] {
	case '"':
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
			case '"':
				return i + 1
			}
		}
	case '\'':
		if i := strings.IndexByte(s[1:], '\''); i >= 0 {
			return i + 2
		}
	}
	end := len(s)
	if i := strings.IndexAny(s, "#\r\n"); i >= 0 {
		end = i
	}
	return len(strings.TrimRight(s[:end], " \t"))
}
