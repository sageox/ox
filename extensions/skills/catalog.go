// Package skills owns ox's canonical Agent Skills source tree and catalog.
package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const SkillFileName = "SKILL.md"

var skillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Bundle is the stable unit of installation and future Team Context sync.
// Team Context transport adds provenance and trust metadata around this model;
// it must not change the local installer contract.
type Bundle struct {
	ID          string
	Description string
	Default     bool
	SkillIDs    []string
}

// Catalog is intentionally a slice: declaration order is the human-facing
// order, while selection below remains deterministic and duplicate-safe.
var Catalog = []Bundle{
	{ID: "core", Description: "Everyday SageOx workflows and skill manager", Default: true, SkillIDs: []string{"ox-cli-consult", "ox-cli-conversation", "ox-cli-decision", "ox-cli-plan", "ox-cli-pr-header", "ox-cli-recap", "ox-cli-session-review", "ox-cli-skill-manager", "ox-cli-viz"}},
	{ID: "onramp", Description: "The single committed SageOx on-ramp skill", Default: true, SkillIDs: []string{"sageox"}},
	{ID: "lifecycle", Description: "Explicit ox lifecycle and diagnostic slash surfaces", Default: true, SkillIDs: []string{"ox-cli-cart", "ox-cli-cart-done", "ox-cli-cart-drop", "ox-cli-cart-start", "ox-cli-doctor", "ox-cli-init", "ox-cli-prime", "ox-cli-session-abort", "ox-cli-session-list", "ox-cli-session-start", "ox-cli-session-status", "ox-cli-session-stop", "ox-cli-status"}},
	{ID: "attest", Description: "Optional Attest BDD and evidence playbooks", SkillIDs: []string{"ox-cli-attest-goal", "ox-cli-attest-create"}},
}

// Retired names ox once installed and no longer ships.
//
// Retirement is the half of the lifecycle that never worked under the old model:
// removing a file meant asking every customer to accept a deletion commit, so in
// practice nobody ever did it, and five orphaned command files survived in this
// repository across releases with no release removing them.
//
// A name stays here for two releases after it stops shipping. That window is what
// lets the reconciler recognize a file as ox-owned — and therefore safe to remove
// once its stamp or digest verifies — rather than treating it as a stranger and
// leaving it behind forever. Removing a name from this list is the second, final
// step of a retirement, and it is only safe when the file can no longer be found
// in the wild.
//
// Entries are matched against BOTH skill directory names and Claude command file
// basenames, because the 0.15.0 fold turned one surface into the other.
var Retired = []string{
	// 0.15.0: skills renamed to the reserved ox-cli-* namespace.
	"ox-consult", "ox-conversation", "ox-decision", "ox-plan", "ox-pr-header",
	"ox-recap", "ox-session-review", "ox-skill-manager", "ox-viz",
	"ox-attest-goal", "ox-attest-create",
	// 0.15.0: the Claude-only command surface folded into skills.
	"ox", "ox-status", "ox-doctor", "ox-init", "ox-prime",
	"ox-session-start", "ox-session-stop", "ox-session-status",
	"ox-session-list", "ox-session-abort",
	"ox-cart", "ox-cart-start", "ox-cart-done", "ox-cart-drop",
	// Orphans that predate the fold: shipped once, dropped from the catalog, but
	// never removed from anyone's repository because removal required a commit.
	"ox-agents", "ox-session-pause", "ox-session-recover", "ox-session-resume",
}

// IsRetired reports whether name is a surface ox once installed and now removes.
func IsRetired(name string) bool {
	for _, r := range Retired {
		if r == name {
			return true
		}
	}
	return false
}

// Skill is one complete canonical skill directory. Files are relative to the
// skill root and include SKILL.md plus optional references/, assets/, scripts/.
type Skill struct {
	Name    string
	Content []byte // SKILL.md, retained for simple adapter consumers.
	Files   []File
	Version string
}

type File struct {
	Path    string
	Content []byte
}

func BundleNames(ids []string) ([]string, error) {
	byID := make(map[string]Bundle, len(Catalog))
	for _, bundle := range Catalog {
		byID[bundle.ID] = bundle
	}
	var names []string
	for _, id := range ids {
		bundle, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown ox skill bundle %q", id)
		}
		names = append(names, bundle.SkillIDs...)
	}
	return names, nil
}

func DefaultBundleIDs() []string {
	var ids []string
	for _, bundle := range Catalog {
		if bundle.Default {
			ids = append(ids, bundle.ID)
		}
	}
	return ids
}

var (
	digestOnce  sync.Once
	digestValue string
	digestErr   error
)

// Digest is a stable identity for the complete catalog source. Schedulers use
// it as a dedupe key; it is not a signature or a Team Context trust decision.
//
// The result is memoized because the catalog is compiled into the binary and
// cannot change while the process runs, and because Digest is now on the session
// hot path: `ox agent prime` calls it once per start to decide whether the
// materialized inventory is stale. Recomputing means validating the whole source
// tree and hashing every skill file on every session, to reach the same answer.
func Digest() (string, error) {
	digestOnce.Do(func() { digestValue, digestErr = computeDigest() })
	return digestValue, digestErr
}

func computeDigest() (string, error) {
	if err := Validate(); err != nil {
		return "", err
	}
	h := sha256.New()
	for _, bundle := range Catalog {
		_, _ = h.Write([]byte(bundle.ID + "\x00"))
		for _, name := range bundle.SkillIDs {
			skill, err := readSkill(name, "")
			if err != nil {
				return "", err
			}
			_, _ = h.Write([]byte(name + "\x00"))
			for _, file := range skill.Files {
				_, _ = h.Write([]byte(file.Path + "\x00"))
				_, _ = h.Write(file.Content)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func SelectedBundles(version string, names, bundleIDs []string) ([]Skill, error) {
	if len(bundleIDs) == 0 {
		return Selected(version, names)
	}
	bundleNames, err := BundleNames(bundleIDs)
	if err != nil {
		return nil, err
	}
	return Selected(version, append(names, bundleNames...))
}

func IsKnown(name string) bool {
	_, err := fs.ReadFile(FS, path.Join(name, SkillFileName))
	return err == nil
}

// Validate checks the embedded source against the portable Agent Skills shape
// before it is installed anywhere. It is deliberately strict about layout and
// identity but does not execute or interpret scripts/assets.
func Validate() error {
	return validateSource(FS, Catalog)
}

func validateSource(source fs.FS, catalog []Bundle) error {
	seenBundles := map[string]struct{}{}
	declared := map[string]struct{}{}
	for _, bundle := range catalog {
		if bundle.ID == "" || !skillNameRE.MatchString(bundle.ID) {
			return fmt.Errorf("invalid skill bundle id %q", bundle.ID)
		}
		if _, ok := seenBundles[bundle.ID]; ok {
			return fmt.Errorf("duplicate skill bundle %q", bundle.ID)
		}
		seenBundles[bundle.ID] = struct{}{}
		if strings.TrimSpace(bundle.Description) == "" || len(bundle.SkillIDs) == 0 {
			return fmt.Errorf("skill bundle %q needs a description and skills", bundle.ID)
		}
		for _, name := range bundle.SkillIDs {
			if !skillNameRE.MatchString(name) {
				return fmt.Errorf("invalid skill name %q in bundle %q", name, bundle.ID)
			}
			declared[name] = struct{}{}
		}
	}
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return fmt.Errorf("read canonical skills: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			// The embed package's Go sources share this directory with the
			// canonical skill folders. Only directories participate in the
			// portable skill source tree.
			continue
		}
		name := entry.Name()
		if !skillNameRE.MatchString(name) {
			return fmt.Errorf("invalid canonical skill directory %q", name)
		}
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("canonical skill %q is not in a bundle", name)
		}
		content, err := fs.ReadFile(source, path.Join(name, SkillFileName))
		if err != nil {
			return fmt.Errorf("canonical skill %q has no %s: %w", name, SkillFileName, err)
		}
		content = normalizeEOL(content)
		if err := validateFrontmatter(name, string(content)); err != nil {
			return err
		}
		if err := fs.WalkDir(source, name, func(file string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if file == name || d.IsDir() {
				return nil
			}
			rel := strings.TrimPrefix(file, name+"/")
			if rel != SkillFileName && !strings.HasPrefix(rel, "references/") && !strings.HasPrefix(rel, "assets/") && !strings.HasPrefix(rel, "scripts/") {
				return fmt.Errorf("canonical skill %q has unsupported file %q", name, rel)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for name := range declared {
		if _, err := fs.ReadFile(source, path.Join(name, SkillFileName)); err != nil {
			return fmt.Errorf("bundle declares missing canonical skill %q", name)
		}
	}
	return nil
}

func validateFrontmatter(name, content string) error {
	if !strings.HasPrefix(content, "---\n") {
		return fmt.Errorf("canonical skill %q must start with YAML frontmatter", name)
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return fmt.Errorf("canonical skill %q has unterminated frontmatter", name)
	}
	fm := content[4 : end+4]
	var foundName, description string
	for _, line := range strings.Split(fm, "\n") {
		if value, ok := strings.CutPrefix(line, "name:"); ok {
			foundName = strings.Trim(strings.TrimSpace(value), "\"")
		}
		if value, ok := strings.CutPrefix(line, "description:"); ok {
			description = strings.TrimSpace(value)
		}
	}
	if foundName != name {
		return fmt.Errorf("canonical skill %q frontmatter name is %q", name, foundName)
	}
	// A description that is really an HTML comment is a conversion accident, not a
	// description: the source file's first line was a `<!-- ... -->` block and it
	// was lifted verbatim into the frontmatter. YAML accepts it as a quoted string,
	// so nothing else catches it, and the skill ships with its activation surface
	// replaced by a stray comment while its continuation and `-->` leak into the
	// body as visible text.
	// TrimSpace AFTER the quotes: `description: " <!-- ..."` leaves a leading space
	// once the quotes are gone, and a bare HasPrefix would sail straight past it.
	if strings.HasPrefix(strings.TrimSpace(strings.Trim(description, "\"'")), "<!--") {
		return fmt.Errorf("canonical skill %q has an HTML comment as its description", name)
	}
	if description == "" {
		// Folded YAML values are accepted when a subsequent indented line exists.
		if !strings.Contains(fm, "description: >") && !strings.Contains(fm, "description: |") {
			return fmt.Errorf("canonical skill %q has no description", name)
		}
	}
	return nil
}

func Selected(version string, names []string) ([]Skill, error) {
	if err := Validate(); err != nil {
		return nil, err
	}
	requested := make(map[string]struct{}, len(names))
	for _, name := range names {
		requested[name] = struct{}{}
	}
	named := len(names) > 0
	var out []Skill
	for _, bundle := range Catalog {
		if !named && !bundle.Default {
			continue
		}
		for _, name := range bundle.SkillIDs {
			if named {
				if _, ok := requested[name]; !ok {
					continue
				}
				delete(requested, name)
			}
			skill, err := readSkill(name, version)
			if err != nil {
				return nil, err
			}
			out = append(out, skill)
		}
	}
	if len(requested) > 0 {
		unknown := make([]string, 0, len(requested))
		for name := range requested {
			unknown = append(unknown, name)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown ox skill(s): %v", unknown)
	}
	return out, nil
}

// normalizeEOL rewrites CRLF to LF.
//
// The catalog is embedded from the checkout at BUILD time, and git on Windows
// checks .md files out with CRLF by default. Without this, a Windows-built ox
// embeds "---\r\n…" and then rejects its own catalog: validateFrontmatter looks
// for a literal "---\n" prefix, so every skill fails validation and the binary
// can install nothing at all.
//
// Normalizing here rather than only in the validator also keeps the catalog
// DIGEST identical across platforms, so a Windows machine and a Linux machine
// agree about which revision is installed instead of reconciling forever.
func normalizeEOL(b []byte) []byte {
	if !bytes.Contains(b, []byte("\r\n")) {
		return b
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func readSkill(name, version string) (Skill, error) {
	var files []File
	err := fs.WalkDir(FS, name, func(file string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		content, err := fs.ReadFile(FS, file)
		if err != nil {
			return err
		}
		content = normalizeEOL(content)
		rel := strings.TrimPrefix(file, name+"/")
		files = append(files, File{Path: rel, Content: content})
		return nil
	})
	if err != nil {
		return Skill{}, fmt.Errorf("read canonical skill %s: %w", name, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var content []byte
	for _, file := range files {
		if file.Path == SkillFileName {
			content = file.Content
			break
		}
	}
	return Skill{Name: name, Content: content, Files: files, Version: version}, nil
}
