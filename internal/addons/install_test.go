package addons

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Blank-imported so this package's test binary inherits git-isolation
	// env vars (GIT_CONFIG_GLOBAL=/dev/null, GIT_TERMINAL_PROMPT=0, ...).
	// Without it, `git commit` in these fixtures can inherit a developer's
	// global commit.gpgsign config and block on a passphrase prompt. See
	// internal/testenv/gitenv.go and internal/gitutil/gitenv_test.go, which
	// does the same for its own package.
	_ "github.com/sageox/ox/internal/testenv"
)

// --- fixtures -------------------------------------------------------------

// newTeamRepo creates a minimal, real git repository standing in for a Team
// Context checkout, with one initial commit so HEAD exists. Real git, not a
// mock: the collision and rollback behavior under test depends on actual
// sparse-checkout and index semantics a mock cannot reproduce faithfully.
func newTeamRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet", "--initial-branch=main")
	runGit(t, dir, "config", "user.name", "Team Test")
	runGit(t, dir, "config", "user.email", "team-test@example.com")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("team context\n"), 0o644))
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "--quiet", "-m", "init")
	return dir
}

// runGit runs git with cmd.Dir pinned to dir — never the developer's global
// git identity or config — and fails the test loudly on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func gitLsFiles(t *testing.T, dir string) []string {
	t.Helper()
	out := runGit(t, dir, "ls-files")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func gitStatusClean(t *testing.T, dir string) bool {
	t.Helper()
	return runGit(t, dir, "status", "--porcelain") == ""
}

// installFailingPreCommitHook makes every `git commit` in dir fail, so a
// test can force runTeamTransaction's commit step to fail deterministically
// without touching production code.
func installFailingPreCommitHook(t *testing.T, dir string) {
	t.Helper()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	hook := filepath.Join(hooksDir, "pre-commit")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755))
}

// fakeProvider is a minimal, in-memory Provider. It never touches disk —
// only Install/Update/Remove do, against the real git fixtures above — so it
// exercises exactly the seam types.go defines and nothing of a real catalog.
type fakeProvider struct {
	source   string
	versions map[string]map[string]Resolved
	latest   map[string]string
	err      error // when set, Resolve always fails with this
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		source:   "test:fake",
		versions: map[string]map[string]Resolved{},
		latest:   map[string]string{},
	}
}

// register adds a resolvable version of an add-on and makes it the version
// an empty-string Resolve call returns.
func (p *fakeProvider) register(name, version string, files ...File) {
	if p.versions[name] == nil {
		p.versions[name] = map[string]Resolved{}
	}
	p.versions[name][version] = Resolved{
		// A REAL digest over the files, not a synthetic label. validateResolved
		// now verifies the advertised digest against the bytes (a provider
		// could otherwise advertise the digest a team reviewed and ship
		// different content), so a fake with a made-up digest is a fake the
		// installer correctly refuses. Tests that want the mismatch REJECTED
		// set Digest explicitly after registering.
		Descriptor: Descriptor{Name: name, Version: version, Source: p.source, Digest: addonDigest(files)},
		Files:      files,
	}
	p.latest[name] = version
}

func (p *fakeProvider) Source() string { return p.source }

func (p *fakeProvider) List(ctx context.Context) ([]Descriptor, error) {
	var out []Descriptor
	for _, versions := range p.versions {
		for _, r := range versions {
			out = append(out, r.Descriptor)
		}
	}
	return out, nil
}

func (p *fakeProvider) Resolve(ctx context.Context, name, version string) (Resolved, error) {
	if p.err != nil {
		return Resolved{}, p.err
	}
	versions := p.versions[name]
	if versions == nil {
		return Resolved{}, fmt.Errorf("fakeProvider: no such add-on %q", name)
	}
	if version == "" {
		version = p.latest[name]
	}
	r, ok := versions[version]
	if !ok {
		return Resolved{}, fmt.Errorf("fakeProvider: no such version %q of %q", version, name)
	}
	return r, nil
}

func addonFile(relPath, content string) File {
	return File{Path: relPath, Content: []byte(content), Mode: 0o644}
}

// --- Install ---------------------------------------------------------------

func TestInstall_WritesFilesAndTracksLock(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "---\nname: hello\n---\nHello skill.\n"),
		addonFile("agents/skills/hello/references/notes.md", "notes"),
	)

	result, err := Install(context.Background(), dir, p, "hello", "")
	require.NoError(t, err)
	assert.Equal(t, OpInstall, result.Op)
	assert.Equal(t, "1.0.0", result.Version)
	assert.ElementsMatch(t, []string{
		"agents/skills/hello/SKILL.md",
		"agents/skills/hello/references/notes.md",
	}, result.Written)
	assert.Empty(t, result.Removed)
	assert.Empty(t, result.Refused)

	tracked := gitLsFiles(t, dir)
	assert.Contains(t, tracked, "agents/skills/hello/SKILL.md")
	assert.Contains(t, tracked, "agents/skills/hello/references/notes.md")
	assert.Contains(t, tracked, LockRelativePath)
	assert.True(t, gitStatusClean(t, dir), "checkout must be clean after a successful install")

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	require.Len(t, lock.Addons, 1)
	assert.Equal(t, "hello", lock.Addons[0].Addon)
	assert.Equal(t, "1.0.0", lock.Addons[0].Version)
	assert.Len(t, lock.Addons[0].Files, 2)
}

func TestInstall_AlreadyInstalledErrors(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	_, err = Install(ctx, dir, p, "hello", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAlreadyInstalled)
}

// TestInstall_RefusesNamespaceCollision_HandAuthored is a RED-FIRST claim:
// ADR-032 D3's one collision rule. A destination path the lock does not own
// but that already exists (hand-authored, tracked, on disk) must refuse the
// whole install by name and mutate nothing — not overwrite it, not partially
// write other files from the same add-on.
func TestInstall_RefusesNamespaceCollision_HandAuthored(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)

	handDir := filepath.Join(dir, "agents", "skills", "hello")
	require.NoError(t, os.MkdirAll(handDir, 0o755))
	original := []byte("hand-authored, not ox's to touch\n")
	require.NoError(t, os.WriteFile(filepath.Join(handDir, "SKILL.md"), original, 0o644))
	runGit(t, dir, "add", "agents/skills/hello/SKILL.md")
	runGit(t, dir, "commit", "--quiet", "-m", "hand-authored skill")

	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "ox-authored content\n"),
		addonFile("agents/skills/hello/references/notes.md", "would also have landed\n"),
	)

	_, err := Install(context.Background(), dir, p, "hello", "")
	require.Error(t, err)
	var collErr *CollisionError
	require.ErrorAs(t, err, &collErr)
	assert.Equal(t, []string{"agents/skills/hello/SKILL.md"}, collErr.Paths)

	// All-or-nothing: nothing mutated, not even the OTHER file in the same
	// add-on that had no collision of its own.
	got, err := os.ReadFile(filepath.Join(handDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, original, got, "the hand-authored file must survive byte-for-byte")
	_, err = os.Stat(filepath.Join(handDir, "references", "notes.md"))
	assert.True(t, os.IsNotExist(err), "no file from the refused add-on may land, even one with no collision of its own")
	_, err = os.Stat(filepath.Join(dir, filepath.FromSlash(LockRelativePath)))
	assert.True(t, os.IsNotExist(err), "the lock must not be written on a refused install")
	assert.True(t, gitStatusClean(t, dir), "the checkout must be exactly as it was found")
}

// TestInstall_RefusesNamespaceCollision_SparseTrackedButAbsent is the second
// RED-FIRST claim: a Team Context is a sparse checkout, so a path can be
// tracked in git while absent from disk. A worktree-only existence check
// would call that path free and let install silently overwrite a teammate's
// committed content the moment it materializes. The lock does not own this
// path, so it must refuse exactly like the on-disk collision above.
func TestInstall_RefusesNamespaceCollision_SparseTrackedButAbsent(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)

	handDir := filepath.Join(dir, "agents", "skills", "hello")
	require.NoError(t, os.MkdirAll(handDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(handDir, "SKILL.md"), []byte("hand-authored\n"), 0o644))
	runGit(t, dir, "add", "agents/skills/hello/SKILL.md")
	runGit(t, dir, "commit", "--quiet", "-m", "hand-authored skill")

	runGit(t, dir, "sparse-checkout", "init", "--no-cone")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "info", "sparse-checkout"),
		[]byte("/*\n!/agents/skills/hello/\n"), 0o644))
	runGit(t, dir, "sparse-checkout", "reapply")

	_, statErr := os.Stat(filepath.Join(handDir, "SKILL.md"))
	require.True(t, os.IsNotExist(statErr), "fixture setup failed: the file must be absent from disk before the real assertion runs")

	p := newFakeProvider()
	p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "ox-authored\n"))

	_, err := Install(context.Background(), dir, p, "hello", "")
	require.Error(t, err)
	var collErr *CollisionError
	require.ErrorAs(t, err, &collErr)
	assert.Equal(t, []string{"agents/skills/hello/SKILL.md"}, collErr.Paths)

	_, err = os.Stat(filepath.Join(dir, filepath.FromSlash(LockRelativePath)))
	assert.True(t, os.IsNotExist(err), "the lock must not be written on a refused install")
}

// --- Update ------------------------------------------------------------

// TestUpdate_NotInstalledErrors uses a provider that DOES know the add-on
// (Resolve succeeds) but a lock that has never installed it — the realistic
// "not installed yet" case, as opposed to an unknown name the provider
// itself would reject first.
func TestUpdate_NotInstalledErrors(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	p := newFakeProvider()
	p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))

	_, err := Update(context.Background(), dir, p, "hello")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotInstalled)
}

// TestUpdate_OverwritesAndRemovesDroppedPaths is the third RED-FIRST claim:
// ADR-032 D4 — update overwrites every owned path unconditionally and
// removes owned paths the new version no longer ships, with no merge and no
// partial state.
func TestUpdate_OverwritesAndRemovesDroppedPaths(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/scripts/run.sh", "#!/bin/sh\necho v1\n"),
	)
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)
	require.Contains(t, gitLsFiles(t, dir), "agents/skills/hello/scripts/run.sh")

	p.register("hello", "2.0.0", addonFile("agents/skills/hello/SKILL.md", "v2\n"))
	result, err := Update(ctx, dir, p, "hello")
	require.NoError(t, err)

	assert.Equal(t, []string{"agents/skills/hello/SKILL.md"}, result.Written)
	assert.Equal(t, []string{"agents/skills/hello/scripts/run.sh"}, result.Removed)
	assert.Equal(t, "2.0.0", result.Version)

	got, err := os.ReadFile(filepath.Join(dir, "agents", "skills", "hello", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "v2\n", string(got))

	_, statErr := os.Stat(filepath.Join(dir, "agents", "skills", "hello", "scripts", "run.sh"))
	assert.True(t, os.IsNotExist(statErr), "the dropped path must be gone from disk")
	_, statErr = os.Stat(filepath.Join(dir, "agents", "skills", "hello", "scripts"))
	assert.True(t, os.IsNotExist(statErr), "the now-empty scripts/ directory must be pruned")

	tracked := gitLsFiles(t, dir)
	assert.NotContains(t, tracked, "agents/skills/hello/scripts/run.sh", "the dropped path must no longer be tracked")
	assert.Contains(t, tracked, "agents/skills/hello/SKILL.md")

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	locked, ok := lock.Find("hello")
	require.True(t, ok)
	assert.Equal(t, "2.0.0", locked.Version)
	require.Len(t, locked.Files, 1)
	assert.Equal(t, "agents/skills/hello/SKILL.md", locked.Files[0].Path)

	assert.True(t, gitStatusClean(t, dir))
}

// TestUpdate_WarnsOnLocallyModifiedFile proves the per-file digest exists for
// honesty (ADR-032 D4): the warning names the exact path the team edited,
// and the update overwrites it anyway rather than keeping the edit.
func TestUpdate_WarnsOnLocallyModifiedFile(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1 content\n"),
		addonFile("agents/skills/hello/references/notes.md", "v1 notes\n"),
	)
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	// The team edits ONE of the two installed files by hand.
	skillPath := filepath.Join(dir, "agents", "skills", "hello", "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte("team's own edit\n"), 0o644))
	runGit(t, dir, "add", "agents/skills/hello/SKILL.md")
	runGit(t, dir, "commit", "--quiet", "-m", "team edit")

	p.register("hello", "2.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v2 content\n"),
		addonFile("agents/skills/hello/references/notes.md", "v2 notes\n"),
	)
	result, err := Update(ctx, dir, p, "hello")
	require.NoError(t, err)

	assert.Equal(t, []string{"agents/skills/hello/SKILL.md"}, result.Modified,
		"the warning must name exactly the path the team edited, not the untouched sibling")

	got, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Equal(t, "v2 content\n", string(got), "update must overwrite the team's edit regardless of the warning")
}

// --- Remove ------------------------------------------------------------

func TestRemove_NotInstalledErrors(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	_, err := Remove(context.Background(), dir, "hello")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotInstalled)
}

func TestRemove_DeletesOwnedPathsOnly(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	// A hand-authored neighbor sharing agents/skills/ that Remove must never
	// touch — ownership is per-path in the lock, never inferred from a
	// shared parent directory.
	neighbor := filepath.Join(dir, "agents", "skills", "hello-notes.md")
	require.NoError(t, os.WriteFile(neighbor, []byte("hand-authored neighbor\n"), 0o644))
	runGit(t, dir, "add", "agents/skills/hello-notes.md")
	runGit(t, dir, "commit", "--quiet", "-m", "hand-authored neighbor")

	result, err := Remove(ctx, dir, "hello")
	require.NoError(t, err)
	assert.Equal(t, []string{"agents/skills/hello/SKILL.md"}, result.Removed)

	_, statErr := os.Stat(filepath.Join(dir, "agents", "skills", "hello"))
	assert.True(t, os.IsNotExist(statErr), "the add-on's own now-empty directory must be pruned")
	_, statErr = os.Stat(neighbor)
	require.NoError(t, statErr, "the hand-authored neighbor must survive untouched")

	tracked := gitLsFiles(t, dir)
	assert.NotContains(t, tracked, "agents/skills/hello/SKILL.md")
	assert.Contains(t, tracked, "agents/skills/hello-notes.md")

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	_, ok := lock.Find("hello")
	assert.False(t, ok, "the removed add-on must have no lock record left")
	assert.True(t, gitStatusClean(t, dir))
}

// --- rollback ------------------------------------------------------------

// TestRollback_RestoresExactStateOnCommitFailure forces the commit step of
// an Update to fail (a rejecting pre-commit hook) after the write phase has
// already touched disk, and proves the deferred rollback undoes exactly
// that: an overwritten owned file back to its pre-update bytes, a dropped
// owned file restored (it existed at HEAD), a brand-new file gone (it never
// did), and the lock back to its pre-update record — worktree and index
// both, since a Team Context is a sparse checkout where the two can diverge.
func TestRollback_RestoresExactStateOnCommitFailure(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/old/DROP.md", "drop-me\n"),
	)
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	beforeLock, err := LoadLock(dir)
	require.NoError(t, err)
	beforeTracked := gitLsFiles(t, dir)

	installFailingPreCommitHook(t, dir)

	p.register("hello", "2.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v2-should-not-stick\n"),
		addonFile("agents/skills/hello/NEW.md", "new-should-not-exist\n"),
	)
	_, err = Update(ctx, dir, p, "hello")
	require.Error(t, err, "the rejecting pre-commit hook must fail the transaction")

	// Overwritten owned file: back to v1, not the failed update's bytes.
	got, err := os.ReadFile(filepath.Join(dir, "agents", "skills", "hello", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(got), "an owned file the failed update overwrote must be restored to its pre-update bytes")

	// Dropped owned file: existed at HEAD, so rollback restores it.
	got, err = os.ReadFile(filepath.Join(dir, "agents", "skills", "hello", "old", "DROP.md"))
	require.NoError(t, err)
	assert.Equal(t, "drop-me\n", string(got), "an owned file the failed update deleted must come back")

	// Brand-new file: never existed at HEAD, so rollback removes it rather
	// than trying to "restore" it to nothing.
	_, statErr := os.Stat(filepath.Join(dir, "agents", "skills", "hello", "NEW.md"))
	assert.True(t, os.IsNotExist(statErr), "a file the failed update created for the first time must not survive rollback")

	afterLock, err := LoadLock(dir)
	require.NoError(t, err)
	locked, ok := afterLock.Find("hello")
	require.True(t, ok)
	assert.Equal(t, "1.0.0", locked.Version, "the lock must still show the pre-update version")
	assert.Equal(t, beforeLock.Addons, afterLock.Addons, "the lock content must be byte-for-byte what it was before the failed update")

	assert.True(t, gitStatusClean(t, dir), "the checkout must show no stray or half-staged changes after rollback")
	assert.ElementsMatch(t, beforeTracked, gitLsFiles(t, dir), "the tracked set must exactly match what it was before the failed update")
}

// pathExistsAtHEAD's precision is what makes the rollback split above safe:
// a false "exists" on a path HEAD does not have would abort the whole
// restore batch (git checkout fails outright on any pathspec it cannot
// match), and a false "absent" on a path HEAD does have would delete
// committed content instead of restoring it. Exercise both HEAD states plus
// the unborn-branch case directly, rather than relying only on the coarser
// rollback test above to exercise them indirectly.
func TestPathExistsAtHEAD(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents", "skills", "hello"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "skills", "hello", "SKILL.md"), []byte("v1\n"), 0o644))
	runGit(t, dir, "add", "agents/skills/hello/SKILL.md")
	runGit(t, dir, "commit", "--quiet", "-m", "add hello")

	ctx := context.Background()
	exists, checked := pathExistsAtHEAD(ctx, dir, "agents/skills/hello/SKILL.md")
	assert.True(t, checked)
	assert.True(t, exists)

	exists, checked = pathExistsAtHEAD(ctx, dir, "agents/skills/never/SKILL.md")
	assert.True(t, checked)
	assert.False(t, exists)

	unborn := t.TempDir()
	runGit(t, unborn, "init", "--quiet", "--initial-branch=main")
	_, checked = pathExistsAtHEAD(ctx, unborn, "agents/skills/hello/SKILL.md")
	assert.True(t, checked, "an unborn branch is a confidently-known state, not an unchecked one")
}

// gitTracksPath is the primitive the sparse-checkout collision check above
// depends on; assert its two states directly against the same kind of
// fixture (path tracked, materialization removed) so a future change to
// pathOccupied's plumbing has a focused failure to point at.
func TestGitTracksPath(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents", "skills", "hello"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "skills", "hello", "SKILL.md"), []byte("v1\n"), 0o644))
	runGit(t, dir, "add", "agents/skills/hello/SKILL.md")
	runGit(t, dir, "commit", "--quiet", "-m", "add hello")

	tracked, err := gitTracksPath(context.Background(), dir, "agents/skills/hello/SKILL.md")
	require.NoError(t, err)
	assert.True(t, tracked)

	tracked, err = gitTracksPath(context.Background(), dir, "agents/skills/never/SKILL.md")
	require.NoError(t, err)
	assert.False(t, tracked)
}
