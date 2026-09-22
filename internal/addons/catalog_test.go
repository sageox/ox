package addons

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// --- A. EmbeddedProvider against the real, compiled-in catalog ---

func TestEmbeddedProvider_Source(t *testing.T) {
	require.Equal(t, SourceBuiltin, NewEmbeddedProvider().Source())
}

// TestEmbeddedProvider_List_ReturnsPostCutoff verifies the one add-on shipped
// today lists with a non-empty version and summary, sorted (trivially, with
// one entry) by name.
func TestEmbeddedProvider_List_ReturnsPostCutoff(t *testing.T) {
	descriptors, err := NewEmbeddedProvider().List(context.Background())
	require.NoError(t, err)
	require.Len(t, descriptors, 1, "extensions/addons ships exactly post-cutoff today")

	d := descriptors[0]
	require.Equal(t, "post-cutoff", d.Name)
	require.NotEmpty(t, d.Version)
	require.NotEmpty(t, d.Summary)
	require.Equal(t, SourceBuiltin, d.Source)
	require.NotEmpty(t, d.Digest)
	require.Equal(t, []string{"post-cutoff"}, d.Skills)
	require.Empty(t, d.Rules)
	require.Equal(t, []ArtifactKind{KindSkill}, d.Kinds)
	require.False(t, d.HasScripts)
}

// TestEmbeddedProvider_Resolve_FilesUnderAgents verifies every resolved file
// path is slash-separated and rooted at agents/, per the File.Path contract
// in types.go.
func TestEmbeddedProvider_Resolve_FilesUnderAgents(t *testing.T) {
	resolved, err := NewEmbeddedProvider().Resolve(context.Background(), "post-cutoff", "")
	require.NoError(t, err)
	require.NotEmpty(t, resolved.Files)

	for _, f := range resolved.Files {
		require.True(t, strings.HasPrefix(f.Path, "agents/"), "file path %q must start with agents/", f.Path)
		require.False(t, strings.Contains(f.Path, "\\"), "file path %q must be slash-separated", f.Path)
		require.NotEmpty(t, f.Digest)
		require.NotEmpty(t, f.Content)
	}
	require.Equal(t, "agents/skills/post-cutoff/SKILL.md", resolved.Files[0].Path,
		"files are sorted by path; SKILL.md sorts before references/*")
}

// TestEmbeddedProvider_Resolve_VersionPinning proves the empty-version and
// pinned-version paths agree, and that a version the provider does not offer
// is refused rather than silently coerced to the one it does.
func TestEmbeddedProvider_Resolve_VersionPinning(t *testing.T) {
	p := NewEmbeddedProvider()

	unpinned, err := p.Resolve(context.Background(), "post-cutoff", "")
	require.NoError(t, err)
	require.NotEmpty(t, unpinned.Version, "an empty version request must still return a concrete version")

	pinned, err := p.Resolve(context.Background(), "post-cutoff", unpinned.Version)
	require.NoError(t, err)
	require.Equal(t, unpinned.Version, pinned.Version)
	require.Equal(t, unpinned.Digest, pinned.Digest)

	_, err = p.Resolve(context.Background(), "post-cutoff", unpinned.Version+"-does-not-exist")
	require.ErrorIs(t, err, ErrVersionMismatch)
}

// TestEmbeddedProvider_Resolve_UnknownAddon and the invalid-name case below
// are the two ways a caller can ask for something the catalog does not have.
func TestEmbeddedProvider_Resolve_UnknownAddon(t *testing.T) {
	_, err := NewEmbeddedProvider().Resolve(context.Background(), "does-not-exist", "")
	require.ErrorIs(t, err, ErrAddonNotFound)
}

func TestEmbeddedProvider_Resolve_InvalidName(t *testing.T) {
	// A name a caller might pass straight from a CLI arg: contains characters
	// addonNameRE rejects outright, so it never reaches a path.Join at all.
	_, err := NewEmbeddedProvider().Resolve(context.Background(), "../etc/passwd", "")
	require.ErrorIs(t, err, ErrAddonNotFound)
}

// TestEmbeddedProvider_Resolve_DigestDeterministic is the claim types.go
// makes on Resolved: "the same (name, version) yields the same digest on
// every machine". Resolving twice in the same process is the cheapest proof
// that nothing non-deterministic (map iteration order, wall-clock time, a
// random UUID) has leaked into the construction.
func TestEmbeddedProvider_Resolve_DigestDeterministic(t *testing.T) {
	p := NewEmbeddedProvider()

	first, err := p.Resolve(context.Background(), "post-cutoff", "")
	require.NoError(t, err)
	second, err := p.Resolve(context.Background(), "post-cutoff", "")
	require.NoError(t, err)

	require.Equal(t, first.Digest, second.Digest, "resolving the same (name, version) twice must yield identical digests")
	require.Len(t, second.Files, len(first.Files))
	for i := range first.Files {
		require.Equal(t, first.Files[i].Path, second.Files[i].Path)
		require.Equal(t, first.Files[i].Digest, second.Files[i].Digest)
	}
}

// TestMovedSkillContent_ByteIdentical proves the git mv from
// extensions/skills/post-cutoff into extensions/addons/post-cutoff/skills/
// lost nothing. These hashes were captured from the pre-move originals with
// `shasum -a 256` before the move; if the move (or any later edit) changes a
// single byte, this fails instead of silently shipping drifted content.
func TestMovedSkillContent_ByteIdentical(t *testing.T) {
	tests := []struct {
		path string // relative to extensions/addons/post-cutoff/skills/post-cutoff/
		want string
	}{
		{"SKILL.md", "1cc197c6c79b0167537711bdd22fbc7f53c74f0822fa87a09a7d24ce62108aaf"},
		{"references/AUTHORING.md", "1d6bf10ce0d15c6cfdff3a48520b22f1caf42353c9d782cdfec9278be3015b39"},
		{"references/jev.md", "33f393df4c997698e60a48bda0dc19a10f9fc59f2535bb4d6c5c0733eb557952"},
	}

	root, err := os.Getwd()
	require.NoError(t, err)
	root = filepath.Join(root, "..", "..", "extensions", "addons", "post-cutoff", "skills", "post-cutoff")

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tt.path)))
			require.NoError(t, err)
			sum := sha256.Sum256(content)
			require.Equal(t, tt.want, hex.EncodeToString(sum[:]), "content drifted from the pre-move original")
		})
	}
}

// --- B. resolveFromFS/listFromFS against synthetic trees ---
//
// These use fstest.MapFS rather than the real embedded catalog so the
// error-path and digest-mutation claims can be proven without editing the
// shipped add-on content.

func fakeAddon(t *testing.T, name, version, skillBody string) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		name + "/addon.yaml": &fstest.MapFile{
			Data: []byte("name: " + name + "\nversion: " + version + "\nsummary: a test add-on\n"),
		},
		name + "/skills/" + name + "/SKILL.md": &fstest.MapFile{Data: []byte(skillBody)},
	}
}

func TestResolveFromFS_DigestChangesWithContent(t *testing.T) {
	base := fakeAddon(t, "widget", "1.0.0", "---\nname: widget\ndescription: test\n---\nbody\n")
	mutated := fakeAddon(t, "widget", "1.0.0", "---\nname: widget\ndescription: test\n---\nbody!\n")

	baseResolved, err := resolveFromFS(base, "widget", "")
	require.NoError(t, err)
	mutatedResolved, err := resolveFromFS(mutated, "widget", "")
	require.NoError(t, err)

	require.NotEqual(t, baseResolved.Digest, mutatedResolved.Digest, "a single changed byte must change the digest")
	require.NotEqual(t, baseResolved.Files[0].Digest, mutatedResolved.Files[0].Digest)
}

func TestResolveFromFS_UnknownAddon(t *testing.T) {
	fsys := fakeAddon(t, "widget", "1.0.0", "body")
	_, err := resolveFromFS(fsys, "gadget", "")
	require.ErrorIs(t, err, ErrAddonNotFound)
}

func TestResolveFromFS_VersionMismatch(t *testing.T) {
	fsys := fakeAddon(t, "widget", "2.0.0", "body")
	_, err := resolveFromFS(fsys, "widget", "1.0.0")
	require.ErrorIs(t, err, ErrVersionMismatch)
}

func TestResolveFromFS_MalformedManifest(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{"not yaml", "not: [valid: yaml", "parse add-on"},
		{"name mismatch", "name: other\nversion: 1.0.0\nsummary: x\n", "declares name"},
		{"missing version", "name: widget\nsummary: x\n", "missing version"},
		{"missing summary", "name: widget\nversion: 1.0.0\n", "missing summary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fsys := fstest.MapFS{
				"widget/addon.yaml":             &fstest.MapFile{Data: []byte(tt.manifest)},
				"widget/skills/widget/SKILL.md": &fstest.MapFile{Data: []byte("body")},
			}
			_, err := resolveFromFS(fsys, "widget", "")
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestResolveFromFS_RefusesFileOutsideSkillsAndRules is the red-first proof
// for the path-escape claim: a file anywhere in an add-on's tree other than
// skills/ or rules/ is refused outright, never silently dropped, renamed, or
// included under some other agents/ path.
func TestResolveFromFS_RefusesFileOutsideSkillsAndRules(t *testing.T) {
	fsys := fstest.MapFS{
		"widget/addon.yaml":             &fstest.MapFile{Data: []byte("name: widget\nversion: 1.0.0\nsummary: test\n")},
		"widget/skills/widget/SKILL.md": &fstest.MapFile{Data: []byte("body")},
		// Not under skills/ or rules/: must be refused, not remapped to
		// agents/evil.md or silently skipped.
		"widget/evil.md": &fstest.MapFile{Data: []byte("must never reach the Team Context")},
	}
	_, err := resolveFromFS(fsys, "widget", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside skills/ and rules/")
	require.Contains(t, err.Error(), "evil.md")
}

func TestResolveFromFS_NoInstallableFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"widget/addon.yaml": &fstest.MapFile{Data: []byte("name: widget\nversion: 1.0.0\nsummary: test\n")},
	}
	_, err := resolveFromFS(fsys, "widget", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no installable files")
}

func TestListFromFS_SortsByName(t *testing.T) {
	fsys := fstest.MapFS{
		"zeta/addon.yaml":             &fstest.MapFile{Data: []byte("name: zeta\nversion: 1.0.0\nsummary: z\n")},
		"zeta/skills/zeta/SKILL.md":   &fstest.MapFile{Data: []byte("z")},
		"alpha/addon.yaml":            &fstest.MapFile{Data: []byte("name: alpha\nversion: 1.0.0\nsummary: a\n")},
		"alpha/skills/alpha/SKILL.md": &fstest.MapFile{Data: []byte("a")},
	}
	descriptors, err := listFromFS(fsys)
	require.NoError(t, err)
	require.Len(t, descriptors, 2)
	require.Equal(t, "alpha", descriptors[0].Name)
	require.Equal(t, "zeta", descriptors[1].Name)
}

func TestDescriptorFromFiles_DetectsScripts(t *testing.T) {
	fsys := fstest.MapFS{
		"widget/addon.yaml":                   &fstest.MapFile{Data: []byte("name: widget\nversion: 1.0.0\nsummary: test\n")},
		"widget/skills/widget/SKILL.md":       &fstest.MapFile{Data: []byte("body")},
		"widget/skills/widget/scripts/run.sh": &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
		"widget/rules/team-style.md":          &fstest.MapFile{Data: []byte("# style")},
	}
	resolved, err := resolveFromFS(fsys, "widget", "")
	require.NoError(t, err)
	require.True(t, resolved.HasScripts)
	require.Equal(t, []string{"widget"}, resolved.Skills)
	require.Equal(t, []string{"team-style"}, resolved.Rules)
	require.ElementsMatch(t, []ArtifactKind{KindSkill, KindRule}, resolved.Kinds)
}
