package addons

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	embeddedaddons "github.com/sageox/ox/extensions/addons"
	"gopkg.in/yaml.v3"
)

// addonManifestFile is the hand-authored manifest every add-on directory
// carries at its root, alongside its skills/ and rules/ subtrees.
const addonManifestFile = "addon.yaml"

// addonNameRE matches both add-on directory names and the skill/rule names
// found inside them. It mirrors extensions/skills' skillNameRE: lowercase,
// starting alphanumeric, hyphen-separated. Rejecting anything else before it
// ever reaches a path.Join is what keeps a caller-supplied name (e.g. from
// `ox addons install <name>`) from being treated as a path fragment at all.
var addonNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ErrAddonNotFound is returned by List/Resolve for a name the provider does
// not carry. Wrapped with context; callers use errors.Is, never string match.
var ErrAddonNotFound = errors.New("add-on not found")

// ErrVersionMismatch is returned by Resolve when a non-empty version was
// requested and the provider offers a different one. The embedded provider
// only ever offers exactly one version per add-on, so this is always the
// concrete version named in the manifest, never a "latest available" list.
var ErrVersionMismatch = errors.New("add-on version not available")

// manifest is addon.yaml's shape: the handful of facts that cannot be
// derived from the on-disk tree. Everything else on Descriptor — Skills,
// Rules, Kinds, HasScripts — is derived in descriptorFromFiles from the
// files actually found under skills/ and rules/, deliberately never
// duplicated here.
type manifest struct {
	Name         string `yaml:"name"`
	Version      string `yaml:"version"`
	Summary      string `yaml:"summary"`
	ValidThrough string `yaml:"valid_through,omitempty"`
}

// EmbeddedProvider is the SourceBuiltin Provider: add-ons compiled into the
// ox binary under extensions/addons/. It is stateless — every call re-walks
// the embedded fs.FS directly, which costs nothing because the tree already
// lives in the binary's memory image.
type EmbeddedProvider struct{}

// NewEmbeddedProvider returns the builtin catalog Provider.
func NewEmbeddedProvider() EmbeddedProvider {
	return EmbeddedProvider{}
}

func (EmbeddedProvider) Source() string {
	return SourceBuiltin
}

// List returns every add-on this provider offers, sorted by name.
func (EmbeddedProvider) List(ctx context.Context) ([]Descriptor, error) {
	return listFromFS(embeddedaddons.FS)
}

// Resolve pins one add-on to exact bytes. An empty version means "the
// version this provider currently offers" — the embedded provider only ever
// has one version on disk per add-on, so that is also the only version a
// non-empty request can match.
func (EmbeddedProvider) Resolve(ctx context.Context, name, version string) (Resolved, error) {
	return resolveFromFS(embeddedaddons.FS, name, version)
}

// listFromFS and resolveFromFS take an fs.FS parameter, rather than reading
// embeddedaddons.FS directly, so tests can exercise the parsing, digesting,
// and path-escape rules against a crafted fstest.MapFS without needing a
// second copy of the embedded catalog compiled in just for the test.
func listFromFS(source fs.FS) ([]Descriptor, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read add-on catalog root: %w", err)
	}
	var descriptors []Descriptor
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		resolved, err := resolveFromFS(source, entry.Name(), "")
		if err != nil {
			return nil, err
		}
		descriptors = append(descriptors, resolved.Descriptor)
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Name < descriptors[j].Name })
	return descriptors, nil
}

func resolveFromFS(source fs.FS, name, version string) (Resolved, error) {
	if !addonNameRE.MatchString(name) {
		return Resolved{}, fmt.Errorf("invalid add-on name %q: %w", name, ErrAddonNotFound)
	}
	man, err := readManifest(source, name)
	if err != nil {
		return Resolved{}, err
	}
	if version != "" && version != man.Version {
		return Resolved{}, fmt.Errorf("add-on %q version %q not available (have %q): %w", name, version, man.Version, ErrVersionMismatch)
	}
	files, err := readAddonFiles(source, name)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Descriptor: descriptorFromFiles(man, files),
		Files:      files,
	}, nil
}

func readManifest(source fs.FS, name string) (manifest, error) {
	raw, err := fs.ReadFile(source, path.Join(name, addonManifestFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return manifest{}, fmt.Errorf("add-on %q: %w", name, ErrAddonNotFound)
		}
		return manifest{}, fmt.Errorf("read add-on %q manifest: %w", name, err)
	}
	var man manifest
	if err := yaml.Unmarshal(raw, &man); err != nil {
		return manifest{}, fmt.Errorf("parse add-on %q manifest: %w", name, err)
	}
	if man.Name != name {
		return manifest{}, fmt.Errorf("add-on %q manifest declares name %q", name, man.Name)
	}
	if !addonNameRE.MatchString(man.Name) {
		return manifest{}, fmt.Errorf("add-on %q manifest has invalid name %q", name, man.Name)
	}
	if strings.TrimSpace(man.Version) == "" {
		return manifest{}, fmt.Errorf("add-on %q manifest is missing version", name)
	}
	if strings.TrimSpace(man.Summary) == "" {
		return manifest{}, fmt.Errorf("add-on %q manifest is missing summary", name)
	}
	return man, nil
}

// readAddonFiles walks one add-on's tree and turns every file under skills/
// or rules/ into an installable File, with Path rewritten to the Team
// Context path it lands on (agents/skills/... or agents/rules/...).
//
// Anything NOT under skills/ or rules/ — including addon.yaml itself, which
// is skipped explicitly — is refused rather than silently included or
// remapped. That refusal is the entire defense against "an add-on whose tree
// contains a path that would escape agents/", and it is sufficient by
// construction: every fs.FS (embed.FS, fstest.MapFS, and any future
// provider's own fs.FS) rejects ".." path elements before WalkDir ever sees
// one (fs.ValidPath), and destPath below is built by path.Join, which always
// returns an already-cleaned path — so a file that passes the skills/rules
// prefix check can never resolve to anything other than agents/skills/... or
// agents/rules/.... There is deliberately no second "is this still under
// agents/" check after that; it could never fail without the first one
// already having failed, and a check that can never fail is not a check.
func readAddonFiles(source fs.FS, name string) ([]File, error) {
	var files []File
	err := fs.WalkDir(source, name, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, name+"/")
		if rel == addonManifestFile {
			return nil
		}
		if !strings.HasPrefix(rel, "skills/") && !strings.HasPrefix(rel, "rules/") {
			return fmt.Errorf("add-on %q file %q is outside skills/ and rules/: refusing", name, rel)
		}
		// path.Join both joins and cleans, so destPath is already normalized;
		// there is no further ".." or traversal check to make here that the
		// prefix check above has not already made. See the doc comment on
		// readAddonFiles for why that single check is the whole defense.
		destPath := path.Join("agents", rel)
		content, err := fs.ReadFile(source, p)
		if err != nil {
			return fmt.Errorf("read add-on %q file %q: %w", name, rel, err)
		}
		files = append(files, File{
			Path: destPath,
			// Mode is fixed at 0644 for every file. embed.FS reports every
			// regular file as 0444 regardless of its source permissions — it
			// does not preserve the executable bit — so a future add-on
			// shipping scripts/ cannot get +x from this field either way; the
			// installer must set it explicitly from HasScripts, not trust Mode.
			Content: content,
			Mode:    0o644,
			Digest:  hashBytes(content),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("add-on %q has no installable files under skills/ or rules/", name)
	}
	// fs.WalkDir already yields entries in lexical order, so this is
	// belt-and-suspenders — but it is also what makes the digest
	// determinism claim true BY CONSTRUCTION rather than by accident of the
	// stdlib's current walk order, which addonDigest below depends on.
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func descriptorFromFiles(man manifest, files []File) Descriptor {
	var skillNames, ruleNames []string
	seenSkill := map[string]bool{}
	seenRule := map[string]bool{}
	kindSeen := map[ArtifactKind]bool{}
	hasScripts := false

	for _, f := range files {
		rel := strings.TrimPrefix(f.Path, "agents/")
		parts := strings.Split(rel, "/")
		switch parts[0] {
		case "skills":
			kindSeen[KindSkill] = true
			if len(parts) >= 2 && !seenSkill[parts[1]] {
				seenSkill[parts[1]] = true
				skillNames = append(skillNames, parts[1])
			}
			if len(parts) >= 3 && parts[2] == "scripts" {
				hasScripts = true
			}
		case "rules":
			kindSeen[KindRule] = true
			if len(parts) >= 2 {
				ruleName := strings.TrimSuffix(parts[1], ".md")
				if !seenRule[ruleName] {
					seenRule[ruleName] = true
					ruleNames = append(ruleNames, ruleName)
				}
			}
		}
	}
	sort.Strings(skillNames)
	sort.Strings(ruleNames)

	var kinds []ArtifactKind
	for _, k := range []ArtifactKind{KindSkill, KindRule} {
		if kindSeen[k] {
			kinds = append(kinds, k)
		}
	}

	return Descriptor{
		Name:         man.Name,
		Version:      man.Version,
		Summary:      man.Summary,
		Source:       SourceBuiltin,
		Digest:       addonDigest(files),
		Skills:       skillNames,
		Rules:        ruleNames,
		Kinds:        kinds,
		HasScripts:   hasScripts,
		ValidThrough: man.ValidThrough,
	}
}

// addonDigest computes an add-on's identity: sha256 over every file's Team
// Context path and content, path-sorted, both path and content written per
// file. This is the construction a future remote Provider must reproduce
// exactly, because the lock records this digest as the team's selection —
// two machines resolving the same (name, version) must agree on it byte for
// byte, on every OS, forever (until the version changes).
//
// Path is hashed alongside content deliberately: hashing content alone would
// let two add-ons that happened to ship byte-identical file content at
// different paths collide on digest, silently conflating two different
// installs. Files are sorted by Path first because directory walk order is
// not a cross-platform guarantee worth depending on for a value this one is
// used for.
func addonDigest(files []File) string {
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write(f.Content)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
