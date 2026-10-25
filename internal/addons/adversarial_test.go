package addons

// adversarial_test.go — the Add-on Catalog under attack.
//
// ADR-032's third-party authorship section makes the threat model explicit:
// "an add-on's author is not necessarily SageOx, and not necessarily the team
// installing it … third-party bytes are untrusted input." Everything in this
// file treats a Provider as hostile and asks one question of the installer:
// does it REFUSE, or does it WRITE?
//
// The organizing sections, each named after the failure it prevents:
//
//	A. destination-path attacks — traversal, absolute, backslash, duplicate
//	B. symlink escape — os.Root is the defense; prove it holds
//	C. case and Unicode-normalization collisions — platform-divergent
//	D. what the installer recomputes vs what it trusts (digests, modes)
//	E. oversized content
//	F. the lock as a cross-version contract — newer schema, corrupt, unreadable
//	G. cancellation and a busy Team Context
//	H. rollback at every reachable transaction boundary
//	I. concurrent writers
//	J. state transitions a human's own edits create
//	K. the Result / Lock JSON other tools parse
//	L. a provider that fails mid-lifecycle (yank, offline, network)
//
// Fixtures (newTeamRepo, runGit, gitLsFiles, gitStatusClean,
// installFailingPreCommitHook, addonFile, newFakeProvider) come from
// install_test.go; the rigged Provider comes from conformance_test.go. Real
// git in a temp directory, never a mock: sparse-checkout, index, and
// case-folding semantics are the substance of half these claims.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/gitutil"
)

// --- shared harness -------------------------------------------------------

// teamSnapshot is everything about a Team Context checkout that a FAILED
// add-on operation must leave exactly as it found. "Exactly" is the whole
// claim, so this records more than the obvious: HEAD (no commit slipped in),
// the tracked set (nothing staged or unstaged), porcelain status (ADR-032 D5 —
// an untracked file in a Team Context wedges GC permanently, so "clean" is a
// correctness requirement and not tidiness), and a digest of every path
// outside .git (a stray file three directories down is still a stray file).
type teamSnapshot struct {
	head      string
	tracked   []string
	porcelain string
	tree      map[string]string
}

func snapshotTeam(t *testing.T, dir string) teamSnapshot {
	t.Helper()
	head, _ := gitMaybe(t, dir, "rev-parse", "HEAD")
	return teamSnapshot{
		head:      head,
		tracked:   gitLsFiles(t, dir),
		porcelain: runGit(t, dir, "status", "--porcelain"),
		tree:      teamTree(t, dir),
	}
}

// requireUnchanged is the assertion every boundary test in section H ends
// with. why names the boundary, so a failure says which one leaked.
func (before teamSnapshot) requireUnchanged(t *testing.T, dir, why string) {
	t.Helper()
	after := snapshotTeam(t, dir)
	assert.Equal(t, before.head, after.head, "%s: HEAD moved", why)
	assert.ElementsMatch(t, before.tracked, after.tracked, "%s: the tracked set changed", why)
	assert.Equal(t, before.porcelain, after.porcelain, "%s: the index or worktree is no longer as it was found", why)
	assert.Equal(t, before.tree, after.tree, "%s: a file appeared, vanished, or changed", why)
}

// teamTree digests every FILE and symlink in the checkout except under .git,
// so a comparison catches a stray file, a changed byte, and a
// file-turned-symlink alike.
//
// Directories are deliberately excluded, and the reason is a finding rather
// than a convenience: rollbackTeamWrite removes the files a failed operation
// wrote but never the directories it created along the way, so a failed
// install leaves agents/, agents/skills/<name>/ and .sageox/ behind, empty.
// Git does not track empty directories, so porcelain stays clean and
// ADR-032 D5's GC wedge does not fire — which is why this is residue and not
// a wedge. Every test here still asserts porcelain emptiness separately, so
// the D5 claim is checked directly rather than inferred from this map.
func teamTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			return filepath.SkipDir
		}
		key := filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, linkErr := os.Readlink(p)
			if linkErr != nil {
				return linkErr
			}
			tree[key] = "symlink -> " + target
		case d.IsDir():
			return nil
		default:
			content, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			tree[key] = contentDigest(content)
		}
		return nil
	}))
	return tree
}

// gitMaybe runs git without failing the test, for probes whose failure is a
// legitimate answer (an unborn branch has no HEAD).
func gitMaybe(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	out, err := gitutil.RunGit(context.Background(), dir, args...)
	return strings.TrimSpace(out), err == nil
}

// lockFileBytes returns the raw committed lock bytes, or nil when absent.
// Raw bytes rather than a parsed Lock: "the lock is byte-for-byte what it
// was" is a stronger claim than "it parses to the same struct", and the
// stronger one is what a teammate pulling the repository depends on.
func lockFileBytes(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(LockRelativePath)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	return data
}

func requireNoLockFile(t *testing.T, dir, why string) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(LockRelativePath)))
	assert.True(t, errors.Is(err, fs.ErrNotExist), "%s: %s exists", why, LockRelativePath)
}

func requireAbsent(t *testing.T, path, why string) {
	t.Helper()
	_, err := os.Lstat(path)
	assert.True(t, errors.Is(err, fs.ErrNotExist), "%s: %s exists", why, path)
}

// writeRaw writes an arbitrary file under dir, creating parents. Used to
// stage the hostile on-disk conditions the portable failure-injection shapes
// need (a directory where a file is expected; a file where a directory
// component is needed).
func writeRaw(t *testing.T, dir, relSlash, content string) string {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(relSlash))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
	return abs
}

// --- A. destination-path attacks ------------------------------------------

// TestInstall_RefusesEveryEscapingDestinationPath is the traversal gate.
//
// Failure prevented: a third-party add-on writing outside the Team Context —
// into $HOME, into /etc, into .git, or over the lock that records ownership.
// validateResolved runs BEFORE the transaction opens the checkout, so a
// refusal here must leave the Team Context untouched down to its porcelain
// status.
func TestInstall_RefusesEveryEscapingDestinationPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"empty", "", "empty destination path"},
		{"parent traversal", "../../etc/cron.d/ox", "escapes the team context"},
		{"bare dotdot", "..", "escapes the team context"},
		{"bare dot", ".", "escapes the team context"},
		{"absolute posix", "/etc/passwd", "escapes the team context"},
		{"absolute home", "/Users/someone/.ssh/authorized_keys", "escapes the team context"},
		{"traversal that leaves agents", "agents/../../etc/x", "not a clean relative path"},
		{"traversal back to the root", "agents/skills/a/../../../x", "not a clean relative path"},
		{"traversal that normalizes back inside", "agents/skills/a/../../rules/a.md", "not a clean relative path"},
		{"windows separators", `..\..\Windows\System32\drivers\etc\hosts`, "must use slashes"},
		{"backslash inside agents", `agents\skills\a\SKILL.md`, "must use slashes"},
		{"dot segment", "agents/skills/./a/SKILL.md", "not a clean relative path"},
		{"double slash", "agents/skills//a/SKILL.md", "not a clean relative path"},
		{"trailing slash", "agents/skills/a/", "not a clean relative path"},
		{"agents itself", "agents", "outside agents/"},
		{"agents prefix without a separator", "agentsfoo/SKILL.md", "outside agents/"},
		{"outside agents", "docs/add-ons/x.md", "outside agents/"},
		// The lock is the record of what ox owns. A provider that could write
		// it could grant itself ownership of hand-authored content.
		{"the ownership lock itself", LockRelativePath, "outside agents/"},
		{"git internals", ".git/hooks/post-checkout", "outside agents/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newTeamRepo(t)
			before := snapshotTeam(t, dir)

			p := newFakeProvider()
			p.register("hostile", "1.0.0",
				addonFile("agents/skills/hostile/SKILL.md", "the benign sibling\n"),
				File{Path: tt.path, Content: []byte("must never land\n"), Mode: 0o644},
			)

			result, err := Install(context.Background(), dir, p, "hostile", "")
			require.Error(t, err, "a destination path of %q must be refused", tt.path)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Empty(t, result.Written, "a refused install must report nothing written")

			requireNoLockFile(t, dir, "a refused install must not write the lock")
			before.requireUnchanged(t, dir, "refusing an escaping destination path")
		})
	}
}

// TestInstall_RefusesDuplicateDestinationPaths: two File entries in one
// Resolved targeting the same path.
//
// Failure prevented: last-write-wins in silence. Whichever entry ran second
// would decide the bytes, while the lock recorded a digest for each — one of
// which would then be a lie about what is on disk, making the next update
// report a file the team never touched as "modified".
func TestInstall_RefusesDuplicateDestinationPaths(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	before := snapshotTeam(t, dir)

	p := newFakeProvider()
	p.register("hostile", "1.0.0",
		addonFile("agents/skills/hostile/SKILL.md", "first writer\n"),
		addonFile("agents/skills/hostile/SKILL.md", "second writer\n"),
	)

	_, err := Install(context.Background(), dir, p, "hostile", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate destination path")
	requireNoLockFile(t, dir, "a refused install must not write the lock")
	before.requireUnchanged(t, dir, "refusing duplicate destination paths")
}

// TestInstall_RefusesAResolvedForADifferentAddon: the provider answers a
// different name than the one asked for.
//
// Failure prevented: `ox addons install post-cutoff` installing something
// else's bytes under the post-cutoff name — a substitution attack the lock
// would then record as the team's audited selection.
func TestInstall_RefusesAResolvedForADifferentAddon(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	before := snapshotTeam(t, dir)

	p := newRigged("test:substituting", twoAddonCatalog()...)
	conforming := p.resolveFn
	p.resolveFn = func(ctx context.Context, name, version string) (Resolved, error) {
		return conforming(ctx, "beta", version) // always beta, whatever was asked
	}

	_, err := Install(context.Background(), dir, p, "alpha", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `resolved "beta" but was asked for "alpha"`)
	before.requireUnchanged(t, dir, "refusing a substituted add-on")
}

// TestInstall_RefusesAnUnpinnedResolution: Resolve returned no concrete
// version.
//
// Failure prevented: a lock record with an empty version — unauditable, and
// indistinguishable from a lock written by an ox that did not record versions
// at all.
func TestInstall_RefusesAnUnpinnedResolution(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	before := snapshotTeam(t, dir)

	p := newRigged("test:unpinned", twoAddonCatalog()...)
	conforming := p.resolveFn
	p.resolveFn = func(ctx context.Context, name, version string) (Resolved, error) {
		r, err := conforming(ctx, name, version)
		r.Version = ""
		return r, err
	}

	_, err := Install(context.Background(), dir, p, "alpha", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not pin a concrete version")
	before.requireUnchanged(t, dir, "refusing an unpinned resolution")
}

// TestInstall_RefusesAPathContainingNUL covers the one escape shape
// cleanAddonPath does NOT inspect: a NUL byte, which every POSIX syscall
// rejects but no path-cleaning function removes.
//
// The claim under test is the outcome, not the mechanism: whatever layer
// refuses it, nothing lands and the Team Context is untouched. (Observed
// today: the refusal comes from the kernel at MkdirAll — "invalid argument"
// — not from validateResolved, so the human sees a syscall error rather
// than "this add-on is malformed". Noted in the review report; the behavior
// asserted here is correct either way.)
func TestInstall_RefusesAPathContainingNUL(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	before := snapshotTeam(t, dir)

	p := newFakeProvider()
	p.register("hostile", "1.0.0", addonFile("agents/skills/ho\x00stile/SKILL.md", "nul\n"))

	_, err := Install(context.Background(), dir, p, "hostile", "")
	require.Error(t, err)
	requireNoLockFile(t, dir, "a refused install must not write the lock")
	before.requireUnchanged(t, dir, "refusing a NUL-containing destination path")
}

// --- B. symlink escape ----------------------------------------------------

// TestInstall_SymlinkCannotDepositOutsideTheCheckout is the os.Root claim the
// contract names: "every write through an os.Root handle rooted at the
// checkout, never a joined absolute path. A Team Context is a
// remote-controlled clone; a symlinked agents/ would otherwise deposit files
// outside the tree."
//
// Failure prevented: a Team Context whose server, or whose earlier bad merge,
// left a symlink in the tree — after which an install writes wherever that
// link points. Every row plants a link escaping the checkout and asserts the
// escape target stays empty.
//
// WHICH GATE FIRES, established by breaking them one at a time rather than
// assumed (.claude/rules/testing.md, "confirm which gate you actually hit"):
// the defense is TWO layers of os.Root and either one alone is sufficient.
// Replacing only tx.write's handle with a joined absolute path leaves this
// test green, because pathOccupied's tx.root.Lstat already refused with "path
// escapes from parent". Replacing only pathOccupied's leaves it green too,
// because the write then refuses. Replacing BOTH makes all three rows fail
// here with content outside the checkout. So a future change that swaps one
// of them for filepath.Join will not be caught by this test — the redundancy
// is real protection and a real blind spot at the same time, and it is worth
// knowing which.
//
// Skipped on Windows rather than ported blind: os.Symlink needs elevation
// there, and a test that cannot create its own isolation asserts nothing
// (.claude/rules/testing.md, "prefer an honest skip to a port nobody can
// exercise").
func TestInstall_SymlinkCannotDepositOutsideTheCheckout(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires elevated privileges on Windows; an unexercisable port would assert nothing")
	}

	tests := []struct {
		name string
		// plant creates the hostile link inside dir, pointing at outside.
		plant func(t *testing.T, dir, outside string)
	}{
		{
			name: "agents/ is a symlink out of the checkout",
			plant: func(t *testing.T, dir, outside string) {
				require.NoError(t, os.Symlink(outside, filepath.Join(dir, "agents")))
			},
		},
		{
			name: "the skill directory is a symlink out of the checkout",
			plant: func(t *testing.T, dir, outside string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents", "skills"), 0o755))
				require.NoError(t, os.Symlink(outside, filepath.Join(dir, "agents", "skills", "hostile")))
			},
		},
		{
			name: "a relative symlink climbing out of the checkout",
			plant: func(t *testing.T, dir, outside string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents", "skills"), 0o755))
				rel, err := filepath.Rel(filepath.Join(dir, "agents", "skills"), outside)
				require.NoError(t, err)
				require.NoError(t, os.Symlink(rel, filepath.Join(dir, "agents", "skills", "hostile")))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newTeamRepo(t)
			outside := t.TempDir()
			tt.plant(t, dir, outside)
			before := snapshotTeam(t, dir)

			p := newFakeProvider()
			p.register("hostile", "1.0.0",
				addonFile("agents/skills/hostile/SKILL.md", "escaped\n"),
				addonFile("agents/skills/hostile/references/notes.md", "escaped too\n"),
			)

			_, err := Install(context.Background(), dir, p, "hostile", "")
			require.Error(t, err, "writing through a symlink out of the checkout must be refused")

			// The claim, stated where it can fail: nothing reached the escape
			// target. Asserted on the whole directory, not one expected name,
			// so an escape under any name still fails.
			entries, readErr := os.ReadDir(outside)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "a file escaped the Team Context through a symlink")

			requireNoLockFile(t, dir, "a refused install must not write the lock")
			before.requireUnchanged(t, dir, "refusing to write through an escaping symlink")
		})
	}
}

// TestInstall_RefusesADanglingSymlinkAsANamespaceCollision covers the reason
// pathOccupied uses Lstat rather than Stat: a broken symlink is a name that
// is taken. Stat would follow it, find nothing, call the destination free,
// and replace whatever a human deliberately parked there.
func TestInstall_RefusesADanglingSymlinkAsANamespaceCollision(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires elevated privileges on Windows; an unexercisable port would assert nothing")
	}
	dir := newTeamRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents", "skills", "hostile"), 0o755))
	link := filepath.Join(dir, "agents", "skills", "hostile", "SKILL.md")
	require.NoError(t, os.Symlink("/nonexistent/deliberately", link))
	before := snapshotTeam(t, dir)

	p := newFakeProvider()
	p.register("hostile", "1.0.0", addonFile("agents/skills/hostile/SKILL.md", "replacement\n"))

	_, err := Install(context.Background(), dir, p, "hostile", "")
	require.Error(t, err)
	var collision *CollisionError
	require.ErrorAs(t, err, &collision)
	assert.Equal(t, []string{"agents/skills/hostile/SKILL.md"}, collision.Paths)

	target, readErr := os.Readlink(link)
	require.NoError(t, readErr, "the symlink itself must survive")
	assert.Equal(t, "/nonexistent/deliberately", target)
	before.requireUnchanged(t, dir, "refusing a dangling symlink as a collision")
}

// --- C. case and Unicode-normalization collisions -------------------------

// fsCollapses reports whether the filesystem under dir treats the two given
// names as ONE file. Probed at runtime, never inferred from runtime.GOOS:
// macOS is usually case-insensitive and Linux usually is not, but either can
// be mounted the other way, and a test that assumes the platform is wrong on
// whichever machine actually matters.
func fsCollapses(t *testing.T, dir, first, second string) bool {
	t.Helper()
	probe := filepath.Join(dir, "collapse-probe")
	require.NoError(t, os.MkdirAll(probe, 0o755))
	defer func() { _ = os.RemoveAll(probe) }()

	require.NoError(t, os.WriteFile(filepath.Join(probe, first), []byte("a"), 0o644))
	_, err := os.Lstat(filepath.Join(probe, second))
	return err == nil
}

// installTwoCollidingNames installs one add-on shipping two files whose
// destination names are the two given spellings, and reports whether the
// filesystem under the checkout treats those two spellings as one file.
//
// Shared by the two tests below because the SETUP is identical and the
// OUTCOMES are not: case-collapsing and normalization-collapsing filesystems
// diverge, and pretending otherwise is how one of them goes unnoticed.
func installTwoCollidingNames(t *testing.T, first, second string) (dir string, result Result, collapses bool, err error) {
	t.Helper()
	dir = newTeamRepo(t)
	collapses = fsCollapses(t, dir, first, second)

	p := newFakeProvider()
	p.register("casing", "1.0.0",
		addonFile("agents/skills/casing/"+first, "first content\n"),
		addonFile("agents/skills/casing/"+second, "second content\n"),
	)
	result, err = Install(context.Background(), dir, p, "casing", "")
	return dir, result, collapses, err
}

// requireTwoDistinctFilesInstalled is the outcome on a filesystem that keeps
// the two spellings apart: two real files and a lock that tells the truth
// about both.
func requireTwoDistinctFilesInstalled(t *testing.T, dir string, result Result, err error, first, second string) {
	t.Helper()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"agents/skills/casing/" + first,
		"agents/skills/casing/" + second,
	}, result.Written)
	requireLockAgreesWithDisk(t, dir, "casing")
	assert.True(t, gitStatusClean(t, dir))
}

// TestInstall_CaseOnlyPathDifference covers two destinations in one add-on
// that differ only by letter case.
//
// validateResolved's duplicate check keys a map on the exact byte string, so
// "agents/skills/casing/A.md" and "agents/skills/casing/a.md" are two
// destinations to ox and one file to APFS, HFS+ and NTFS. The filesystem is
// probed at runtime, never inferred from runtime.GOOS.
//
// Observed outcomes:
//
//   - case-sensitive filesystem (ext4): two files, truthful lock.
//   - case-insensitive filesystem (APFS, HFS+, NTFS): the install FAILS and
//     rolls back — but git catches it, not ox. The second `git add` finds the
//     other spelling already in the case-folding index, so the final
//     path-scoped `git commit` gets a pathspec matching nothing and aborts.
//
// Failure prevented: a committed selection whose per-file digests are true on
// a Linux teammate's machine and false on a macOS one.
//
// FINDING (reported, not asserted): the refusal is accidental and its message
// is not actionable — "pathspec 'agents/skills/casing/a.md' did not match any
// file(s) known to git" names neither the add-on nor the cause. It also
// depends on the commit being path-scoped; a future change to how
// commitTeamWrite builds its pathspec would silently turn this refusal into
// the silent divergence the normalization case below already exhibits.
func TestInstall_CaseOnlyPathDifference(t *testing.T) {
	t.Parallel()
	const first, second = "A.md", "a.md"
	dir, result, collapses, err := installTwoCollidingNames(t, first, second)

	if !collapses {
		requireTwoDistinctFilesInstalled(t, dir, result, err, first, second)
		return
	}

	require.Error(t, err,
		"two destinations that are one file on this filesystem must not produce a committed selection whose digests disagree with disk")
	requireNoLockFile(t, dir, "a failed install must not leave a lock recording paths that do not exist")
	assert.Empty(t, runGit(t, dir, "status", "--porcelain"),
		"a failed install must leave nothing untracked (ADR-032 D5)")
}

// TestInstall_NormalizationOnlyPathDifference documents a DEFECT: the same
// grapheme in two Unicode normalization forms — NFC U+00E9 versus NFD
// "e"+U+0301 — installs SUCCESSFULLY on macOS and commits a lock that is not
// a true statement about the checkout.
//
// Why this one does not fail where the case variant does: git on macOS sets
// core.precomposeunicode, so both spellings become the SAME index entry and
// every pathspec matches. Nothing anywhere refuses. The result is a committed
// lock with two file records, one on-disk file, and a digest that disagrees
// with the bytes at one of those two paths.
//
// The consequences are the ones ADR-032 D4 depends on being reliable:
//
//   - the next `ox addons update` compares the survivor's bytes against the
//     other path's recorded digest, finds a mismatch, and tells the team it
//     modified a file it never touched — the one message the ADR makes ox
//     responsible for getting right before it overwrites;
//   - one committed lock produces a different tree on macOS than on Linux,
//     which is the thing pinning a version and digest exists to prevent.
//
// The fix is in validateResolved: reject a Resolved whose destinations
// collide after case-folding AND after NFC normalization, beside the
// exact-match duplicate check already there. Refusing is the only answer that
// is the same on every filesystem.
//
// Until then this PINS the divergence, because these tests may not change
// production code. When the fix lands this test fails with the message below
// — replace the pinned branch with a refusal assertion.
func TestInstall_NormalizationOnlyPathDifference(t *testing.T) {
	t.Parallel()
	const first, second = "caf\u00e9.md", "cafe\u0301.md"
	dir, result, collapses, err := installTwoCollidingNames(t, first, second)

	if !collapses {
		requireTwoDistinctFilesInstalled(t, dir, result, err, first, second)
		return
	}

	require.NoError(t, err,
		"pinned defect: install currently SUCCEEDS when two destinations differ only by Unicode normalization. If it now refuses, the validateResolved fix has landed — replace this branch with a refusal assertion")

	onDisk, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash("agents/skills/casing/"+first)))
	require.NoError(t, readErr)
	assert.Equal(t, "second content\n", string(onDisk),
		"pinned defect: the second write landed on the first file")

	lock, lockErr := LoadLock(dir)
	require.NoError(t, lockErr)
	locked, ok := lock.Find("casing")
	require.True(t, ok)
	require.Len(t, locked.Files, 2, "pinned defect: the lock records two paths for one on-disk file")

	assert.NotEmpty(t, lockDigestMismatches(t, dir, locked),
		"pinned defect: the committed lock should be recording a digest that disagrees with the bytes on disk. An empty result means the fix landed — invert this test")
}

// requireLockAgreesWithDisk asserts the invariant a successful install owes
// the team: every digest the lock records is the digest of the bytes actually
// at that path. The lock is committed and read by every teammate, so a false
// digest there is a false statement in shared history.
func requireLockAgreesWithDisk(t *testing.T, dir, name string) {
	t.Helper()
	lock, err := LoadLock(dir)
	require.NoError(t, err)
	locked, ok := lock.Find(name)
	require.True(t, ok, "%q must have a lock record", name)
	assert.Empty(t, lockDigestMismatches(t, dir, locked),
		"the lock records digests that disagree with the bytes on disk")
}

func lockDigestMismatches(t *testing.T, dir string, locked LockedAddon) []string {
	t.Helper()
	var mismatches []string
	for _, f := range locked.Files {
		content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil {
			mismatches = append(mismatches, f.Path+" (unreadable)")
			continue
		}
		if got := contentDigest(content); got != f.Digest {
			mismatches = append(mismatches, fmt.Sprintf("%s (lock %s, disk %s)", f.Path, f.Digest, got))
		}
	}
	return mismatches
}

// --- D. what the installer recomputes vs what it trusts -------------------

// TestInstall_RecomputesPerFileDigestsFromTheBytesItWrote is the digest-tamper
// claim. A hostile provider ships content that does not match its declared
// File.Digest.
//
// Failure prevented: the lock inheriting a provider's claim about bytes
// instead of measuring them. install.go computes LockedFile.Digest with its
// own contentDigest over the content it is about to write, which is what
// makes a later "you modified this" honest — a trusted claim would make every
// subsequent comparison meaningless, and a malicious one would let an add-on
// declare a file unmodified forever.
//
// NOTE (reported, not asserted): the add-on-level Descriptor.Digest is NOT
// verified. install.go records `Digest: resolved.Digest` verbatim, and
// nothing compares it to addonDigest(files). A provider may therefore
// advertise the digest a team reviewed and ship different bytes; the lock
// records the advertised one, and `ox addons list` decides
// update_available from it.
func TestInstall_RecomputesPerFileDigestsFromTheBytesItWrote(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)

	const tamperedDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	p := newFakeProvider()
	p.register("tamper", "1.0.0",
		File{Path: "agents/skills/tamper/SKILL.md", Content: []byte("the real bytes\n"), Mode: 0o644, Digest: tamperedDigest},
		File{Path: "agents/skills/tamper/references/n.md", Content: []byte("more real bytes\n"), Mode: 0o644, Digest: "not-even-a-digest-format"},
	)

	_, err := Install(context.Background(), dir, p, "tamper", "")
	require.NoError(t, err)

	requireLockAgreesWithDisk(t, dir, "tamper")

	raw := lockFileBytes(t, dir)
	require.NotNil(t, raw)
	assert.NotContains(t, string(raw), tamperedDigest,
		"the provider's own digest claim must never be recorded as if ox had measured it")
	assert.NotContains(t, string(raw), "not-even-a-digest-format",
		"the provider's digest FORMAT must not leak into the lock either")

	// And the honesty check that depends on it: an untouched file is not
	// reported as modified on the next update, despite the provider's lie.
	p.register("tamper", "2.0.0",
		File{Path: "agents/skills/tamper/SKILL.md", Content: []byte("v2 bytes\n"), Mode: 0o644, Digest: tamperedDigest},
	)
	result, err := Update(context.Background(), dir, p, "tamper")
	require.NoError(t, err)
	assert.Empty(t, result.Modified,
		"no file was edited by the team, so nothing may be reported as modified")
}

// TestFileMode_IsAlwaysPlain0644 pins the rule ADR-032's third-party section
// requires: an untrusted provider may not choose what mode ox writes.
//
// This test used to assert a weaker clamp — type and setuid/setgid/sticky bits
// stripped, permission bits HONORED — which let a provider supplying 0o755
// land an executable file with no human decision anywhere in the path.
// catalog.go already stated the correct rule ("the installer must set it
// explicitly from HasScripts, not trust Mode") while install.go trusted Mode.
//
// Failure prevented: an add-on shipping +x content. Whether a script is ever
// runnable is decided downstream by `ox skills approve --allow-scripts`, by a
// human, on the projected copy — not by the provider that supplied the bytes
// over a path where nothing verifies signatures.
func TestFileMode_IsAlwaysPlain0644(t *testing.T) {
	t.Parallel()

	for _, in := range []uint32{
		0,                              // unset
		0o644,                          // already correct
		0o755,                          // THE case: a provider asking for +x
		0o777,                          // world-writable and executable
		0o4755,                         // setuid
		0o2755,                         // setgid
		0o1644,                         // sticky
		0o7777,                         // every special bit
		uint32(fs.ModeDir) | 0o644,     // a type bit claiming directory
		uint32(fs.ModeSymlink) | 0o644, // a type bit claiming symlink
		^uint32(0),                     // every bit set
	} {
		t.Run(fmt.Sprintf("%#o", in), func(t *testing.T) {
			t.Parallel()
			got := fileMode(in)
			assert.Equal(t, fs.FileMode(0o644), got,
				"a provider-supplied mode must never reach disk; %#o must still write 0644", in)
			assert.Zero(t, got&0o111,
				"no execute bit may survive: an add-on cannot grant itself +x (%#o)", in)
		})
	}
}

// TestInstall_NeverWritesASetuidFile is the end-to-end half of the clamp: the
// mode that reaches disk, not just the one fileMode returns.
func TestInstall_NeverWritesASetuidFile(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)

	p := newFakeProvider()
	p.register("hostile", "1.0.0",
		File{Path: "agents/skills/hostile/scripts/run.sh", Content: []byte("#!/bin/sh\necho hi\n"), Mode: 0o6755},
	)
	_, err := Install(context.Background(), dir, p, "hostile", "")
	require.NoError(t, err)

	info, err := os.Lstat(filepath.Join(dir, "agents", "skills", "hostile", "scripts", "run.sh"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "an add-on file must land as a regular file")
	assert.Zero(t, info.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky),
		"setuid/setgid/sticky must never reach disk from a provider-supplied mode")
}

// --- E. oversized content -------------------------------------------------

// TestInstall_LargeFileInstallsAtomically bounds the "oversized content"
// attack with a size that proves the behavior without making the suite slow:
// 4 MiB, roughly 40x the largest real add-on file and enough to exercise a
// real write, a real sha256, and a real git blob. A gigabyte would prove
// nothing further and would cost every run.
//
// It is NOT skipped in short mode: .config/test-tiers.json's fast tier
// explicitly includes bounded hermetic git fixtures, and this one measures
// 0.14s in isolation — well inside the 500ms slow-test threshold.
//
// NOTE (reported, not asserted): nothing bounds File.Content. A remote
// provider could return an arbitrarily large body and ox would hold it in
// memory and write it; the only limit today is the machine's.
func TestInstall_LargeFileInstallsAtomically(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)

	const size = 4 << 20
	big := bytes.Repeat([]byte("sageox add-on payload 0123456789\n"), size/33)

	p := newFakeProvider()
	p.register("bulky", "1.0.0",
		File{Path: "agents/skills/bulky/assets/big.bin", Content: big, Mode: 0o644},
		addonFile("agents/skills/bulky/SKILL.md", "---\nname: bulky\n---\n"),
	)

	result, err := Install(context.Background(), dir, p, "bulky", "")
	require.NoError(t, err)
	require.Len(t, result.Written, 2)

	onDisk, err := os.ReadFile(filepath.Join(dir, "agents", "skills", "bulky", "assets", "big.bin"))
	require.NoError(t, err)
	require.Len(t, onDisk, len(big), "the whole payload must be written, not a truncated prefix")
	requireLockAgreesWithDisk(t, dir, "bulky")
	assert.True(t, gitStatusClean(t, dir), "a large file must end up tracked, not merely present (ADR-032 D5)")
}

// --- F. the lock as a cross-version contract ------------------------------

// TestMutatingOps_RefuseAnUnreadableLockAndPreserveItByteForByte is the
// downgrade and corruption gate, run across all three verbs.
//
// Failure prevented, in two directions:
//
//   - FORWARD: a teammate on a newer ox writes a schema this binary does not
//     understand. Guessing at unknown fields would silently drop another
//     add-on's ownership record; overwriting the file would destroy it. The
//     only safe answer is to refuse, say "upgrade ox", and leave the bytes
//     alone — which is why this asserts the file is byte-identical
//     afterwards, not merely that an error was returned.
//   - BACKWARD: the same file on the same machine tomorrow. A lock that
//     exists but cannot be parsed must never read as "nothing installed";
//     that would make Install miss a real collision and Remove believe it
//     owns paths it does not.
func TestMutatingOps_RefuseAnUnreadableLockAndPreserveItByteForByte(t *testing.T) {
	t.Parallel()

	locks := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "a schema from a newer ox",
			content: `{"schema_version": 99, "addons": [{"addon": "future", "source": "builtin:sageox", "version": "9.9.9", "files": [{"path": "agents/skills/future/SKILL.md"}]}]}`,
			wantErr: "upgrade ox",
		},
		{
			name:    "one version past what this binary understands",
			content: fmt.Sprintf(`{"schema_version": %d, "addons": []}`, LockSchemaVersion+1),
			wantErr: "upgrade ox",
		},
		{
			name:    "truncated mid-write",
			content: `{"schema_version": 1, "addons": [{"addon": "half`,
			wantErr: "parse",
		},
		{
			name:    "not json at all",
			content: "# this is yaml, not json\naddons: []\n",
			wantErr: "parse",
		},
		{
			name:    "the right keys with the wrong types",
			content: `{"schema_version": "one", "addons": "none"}`,
			wantErr: "parse",
		},
	}

	ops := []struct {
		name string
		run  func(ctx context.Context, dir string, p Provider) error
	}{
		{"install", func(ctx context.Context, dir string, p Provider) error {
			_, err := Install(ctx, dir, p, "hello", "")
			return err
		}},
		{"update", func(ctx context.Context, dir string, p Provider) error {
			_, err := Update(ctx, dir, p, "hello")
			return err
		}},
		{"remove", func(ctx context.Context, dir string, _ Provider) error {
			_, err := Remove(ctx, dir, "hello")
			return err
		}},
	}

	for _, lk := range locks {
		for _, op := range ops {
			t.Run(lk.name+"/"+op.name, func(t *testing.T) {
				t.Parallel()
				dir := newTeamRepo(t)
				writeRaw(t, dir, LockRelativePath, lk.content)
				runGit(t, dir, "add", "--", LockRelativePath)
				runGit(t, dir, "commit", "--quiet", "-m", "a teammate's lock")
				before := snapshotTeam(t, dir)

				p := newFakeProvider()
				p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))

				err := op.run(context.Background(), dir, p)
				require.Error(t, err, "%s must refuse a lock it cannot read", op.name)
				assert.Contains(t, err.Error(), lk.wantErr)
				assert.Contains(t, err.Error(), LockRelativePath,
					"the message must name the file so a human knows where to look")

				assert.Equal(t, lk.content, string(lockFileBytes(t, dir)),
					"the unreadable lock must survive byte-for-byte; rewriting it would destroy a teammate's ownership record")
				before.requireUnchanged(t, dir, op.name+" on an unreadable lock")
			})
		}
	}
}

// TestMutatingOps_RefuseAStructurallyBrokenLockPath covers the portable
// failure-injection shapes from .claude/rules/testing.md, which is how a bad
// merge or an interrupted extraction actually presents: a directory where the
// lock file belongs, and a regular file where .sageox/ belongs.
//
// Failure prevented: "I could not read the lock" rendering identically to
// "nothing is installed". Either would make Install write over content it
// does not own.
func TestMutatingOps_RefuseAStructurallyBrokenLockPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		break_ func(t *testing.T, dir string)
	}{
		{
			name: "the lock path is a directory",
			break_: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.FromSlash(LockRelativePath)), 0o755))
			},
		},
		{
			name: "the lock path is a directory containing a file",
			break_: func(t *testing.T, dir string) {
				writeRaw(t, dir, LockRelativePath+"/stray.json", "{}")
			},
		},
		{
			name: ".sageox is a regular file",
			break_: func(t *testing.T, dir string) {
				writeRaw(t, dir, ".sageox", "not a directory\n")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newTeamRepo(t)
			tt.break_(t, dir)
			before := snapshotTeam(t, dir)

			p := newFakeProvider()
			p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))

			_, err := Install(context.Background(), dir, p, "hello", "")
			require.Error(t, err, "an unreadable lock path must not be read as an empty selection")
			before.requireUnchanged(t, dir, "install against a structurally broken lock path")
		})
	}
}

// TestLoadLock_NullAddonsIsAnEmptySelection: a lock whose addons key is JSON
// null must normalize to an empty slice, not a nil one.
//
// Failure prevented: a nil slice reaching Lock.Owns and Lock.Find is harmless
// in Go, but WriteLock would then re-emit null, and a JSON consumer iterating
// `.addons` breaks on null where it copes with [].
func TestLoadLock_NullAddonsIsAnEmptySelection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeRaw(t, dir, LockRelativePath, `{"schema_version": 1, "addons": null}`)

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	assert.NotNil(t, lock.Addons, "addons must normalize to an empty slice, never nil")
	assert.Empty(t, lock.Addons)
}

// --- G. cancellation and a busy Team Context ------------------------------

// TestInstall_AlreadyCanceledContextMutatesNothing.
//
// Failure prevented: a Ctrl-C'd install leaving half a tree behind. The
// ASSERTION is the invariant, not the error text, on purpose: the in-process
// lock gate is a buffered-channel select, so an already-canceled context may
// or may not win the acquire race and the failure can surface either from the
// gate or from the first git invocation. What must hold either way is that
// the checkout is untouched.
func TestInstall_AlreadyCanceledContextMutatesNothing(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	before := snapshotTeam(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/n.md", "notes\n"),
	)

	_, err := Install(ctx, dir, p, "hello", "")
	require.Error(t, err, "an install on a canceled context must not report success")
	requireNoLockFile(t, dir, "a canceled install must not leave a lock")
	requireAbsent(t, filepath.Join(dir, "agents", "skills", "hello", "SKILL.md"), "a canceled install must leave no written file")
	before.requireUnchanged(t, dir, "install on an already-canceled context")
}

// TestInstall_CanceledMidCommitRollsTheCheckoutBack is the mid-transaction
// case: the files are already on disk and `git commit` is in flight when the
// context dies.
//
// The synchronization is deterministic, not timed: a pre-commit hook writes a
// sentinel OUTSIDE the checkout to announce that the write phase finished and
// the commit has begun, then blocks. The test waits for the sentinel, cancels,
// and the hook's git process dies with it.
//
// Failure prevented: the D5 wedge. A half-written Team Context leaves an
// untracked file, isCheckoutClean() then treats the clone as dirty forever,
// and blue-green reclone is blocked permanently — with nothing saying why.
//
// FINDING (reported, not asserted): killing git mid-commit leaves a stale
// .git/index.lock in the Team Context, and the transaction does not clean it
// up. Rollback's own git calls then fail — observed in this test's log:
//
//	WARN add-ons rollback incomplete step=unstage
//	error="git reset: fatal: Unable to create '.../.git/index.lock': File
//	exists. Another git process seems to be running in this repository, or
//	the lock file may be stale"
//
// The file-level rollback still completes, because it uses os.Remove rather
// than git for this invocation's own new paths — which is why the assertions
// below hold. But every subsequent git operation on that clone fails until
// something removes the lock file, and gitutil.WithRepoLock cannot help: it
// is an flock on a sidecar, while index.lock is an ordinary file git left
// behind. Not asserted here because pinning it would fail the day a cleanup
// lands, which is the outcome we want.
func TestInstall_CanceledMidCommitRollsTheCheckoutBack(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel hook is a POSIX shell script; a Windows port would need a different hook interpreter")
	}
	dir := newTeamRepo(t)
	sentinel := filepath.Join(t.TempDir(), "commit-started")
	installBlockingPreCommitHook(t, dir, sentinel)

	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/n.md", "notes\n"),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Install(ctx, dir, p, "hello", "")
		done <- err
	}()

	require.Eventually(t, func() bool {
		_, err := os.Lstat(sentinel)
		return err == nil
	}, 20*time.Second, 5*time.Millisecond, "the commit step never started, so nothing mid-transaction was canceled")

	cancel()
	err := <-done
	require.Error(t, err, "a canceled commit must not report success")

	requireAbsent(t, filepath.Join(dir, "agents", "skills", "hello", "SKILL.md"), "the written files must be rolled back")
	requireAbsent(t, filepath.Join(dir, "agents", "skills", "hello", "references", "n.md"), "every written file must be rolled back, not just the first")
	requireNoLockFile(t, dir, "the lock written before the commit must be rolled back")
}

// installBlockingPreCommitHook makes `git commit` announce itself and then
// hang, so a test can act at a known point inside the transaction. The
// sentinel lives outside the checkout so it cannot dirty the tree under test.
//
// The hook closes its inherited stdout and stderr before blocking, and that
// line is load-bearing. RunGit reads the child's combined output, which does
// not return until every writer to that pipe has closed it — including a
// GRANDCHILD the killed git process leaves behind. Without the redirect,
// canceling the context does not unblock RunGit until the hook's own sleep
// expires, and this test takes as long as the sleep rather than as long as
// the cancellation. (Worth knowing beyond the test: ox's own commit calls
// inherit the same property against a real slow hook.)
func installBlockingPreCommitHook(t *testing.T, dir, sentinel string) {
	t.Helper()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	script := fmt.Sprintf("#!/bin/sh\n: > %q\nexec 1>/dev/null 2>&1\nsleep 60\n", sentinel)
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(script), 0o755))
}

// TestInstall_ReportsABusyTeamContextDistinctlyFromAFailure covers the
// contract's "distinguish 'lock was busy' from 'the operation failed'" rule.
//
// Failure prevented: telling a human "install failed" for a clone that is
// merely syncing sends them looking for damage that is not there. The
// caller's own deadline bounds the wait, so this costs milliseconds rather
// than the two-minute RepoLockTimeout.
func TestInstall_ReportsABusyTeamContextDistinctlyFromAFailure(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	before := snapshotTeam(t, dir)

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = gitutil.WithRepoLock(context.Background(), dir, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer func() { close(release); <-holderDone }()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	p := newFakeProvider()
	p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))

	_, err := Install(ctx, dir, p, "hello", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "syncing right now",
		"a busy Team Context must read as busy, not as a failed install")
	assert.Contains(t, err.Error(), dir, "the message must name which Team Context is busy")
	before.requireUnchanged(t, dir, "install while the Team Context lock is held")
}

// --- H. rollback at every reachable transaction boundary ------------------
//
// runTeamTransaction's failure points, and which are reachable from outside
// this package (i.e. testable without editing production code):
//
//	1. os.OpenRoot(teamPath)          reachable — teamPath absent or a file
//	2. loadLockRoot                   reachable — section F
//	3. apply: already/not installed   reachable — install_test.go
//	4. apply: collisionsFor           reachable — below, and install_test.go
//	5. apply: tx.write                reachable — below (a file where a
//	                                  directory component is needed)
//	6. apply: tx.modifiedSince        reachable — below (an owned path
//	                                  replaced by a directory)
//	7. apply: tx.remove               UNREACHABLE — every lever that makes
//	                                  root.Remove fail also makes
//	                                  modifiedSince's ReadFile of the same
//	                                  path fail first, so (6) always fires
//	                                  instead. Noted, not skipped silently.
//	8. WriteLock                      UNREACHABLE — every lever that makes the
//	                                  lock unwritable makes loadLockRoot's
//	                                  read of it fail first, so (2) always
//	                                  fires instead.
//	9. commitTeamWrite: git add       reachable — corrupt .git/index
//	10. commitTeamWrite: git commit    reachable — a rejecting pre-commit hook
//
// Each reachable boundary gets a test below asserting the same thing: the
// Team Context is exactly as it was found.

// TestInstall_RefusesAnAbsentOrNonDirectoryTeamContext — boundary 1.
func TestInstall_RefusesAnAbsentOrNonDirectoryTeamContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{
			name: "the Team Context does not exist",
			path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-such-team-context") },
		},
		{
			name: "the Team Context path is a regular file",
			path: func(t *testing.T) string {
				dir := t.TempDir()
				f := filepath.Join(dir, "team-context")
				require.NoError(t, os.WriteFile(f, []byte("not a checkout\n"), 0o644))
				return f
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newFakeProvider()
			p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))

			_, err := Install(context.Background(), tt.path(t), p, "hello", "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "open team context")
		})
	}
}

// TestInstall_RollsBackAPartialWriteWithinOneAddon — boundary 5, and the
// atomicity claim ADR-032 makes when it rejects "partial application on
// conflict": "a half-applied add-on version is a state no one can name. An
// update either lands whole or changes nothing."
//
// The lever is provider-driven and portable: the add-on ships a file, then a
// second file NESTED UNDER the first one's path. Both pass validation and the
// collision check (neither exists yet), the first write succeeds, and the
// second's MkdirAll hits a regular file where it needs a directory. No
// os.Chmod, which no-ops on Windows.
//
// Failure prevented: the first file surviving as an untracked orphan —
// ADR-032 D5's permanent GC wedge, from an add-on whose install "failed".
func TestInstall_RollsBackAPartialWriteWithinOneAddon(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	before := snapshotTeam(t, dir)

	p := newFakeProvider()
	p.register("hostile", "1.0.0",
		addonFile("agents/skills/hostile/SKILL.md", "lands first\n"),
		addonFile("agents/skills/hostile/SKILL.md/nested.md", "cannot land\n"),
	)

	_, err := Install(context.Background(), dir, p, "hostile", "")
	require.Error(t, err, "a write failure partway through an add-on must fail the whole install")

	requireAbsent(t, filepath.Join(dir, "agents", "skills", "hostile", "SKILL.md"),
		"the file written before the failure must not survive")
	requireNoLockFile(t, dir, "a failed install must not leave a lock")
	before.requireUnchanged(t, dir, "rolling back a partial write inside one add-on")
}

// TestUpdate_RefusesWhenAnOwnedPathBecameADirectory — boundary 6.
//
// Failure prevented: the honesty check reading "I could not look" as "the
// file is unchanged". A path the lock owns that is now a directory (a bad
// merge, an interrupted extraction) must stop the update, not be silently
// classified as unmodified and then be half-overwritten.
func TestUpdate_RefusesWhenAnOwnedPathBecameADirectory(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/n.md", "notes\n"),
	)
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	// Replace an owned file with a non-empty directory of the same name, then
	// commit that state so the checkout is clean before the real assertion.
	owned := filepath.Join(dir, "agents", "skills", "hello", "references", "n.md")
	require.NoError(t, os.Remove(owned))
	require.NoError(t, os.MkdirAll(owned, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(owned, "inside.md"), []byte("stray\n"), 0o644))
	runGit(t, dir, "add", "--all", "--", "agents")
	runGit(t, dir, "commit", "--quiet", "-m", "a bad merge left a directory here")
	before := snapshotTeam(t, dir)

	p.register("hello", "2.0.0", addonFile("agents/skills/hello/SKILL.md", "v2\n"))
	_, err = Update(ctx, dir, p, "hello")
	require.Error(t, err, "an owned path ox cannot read must stop the update, not be assumed unchanged")

	before.requireUnchanged(t, dir, "update with an owned path replaced by a directory")
}

// TestInstall_RefusesWhenAnotherAddonOwnsTheDestination — boundary 4, the
// half of ADR-032 D3's collision rule install_test.go does not cover: the
// occupant is not hand-authored, it belongs to a DIFFERENT add-on.
//
// Failure prevented: two add-ons silently fighting over one path, with the
// lock claiming both own it and `ox addons remove` then deleting the other
// one's file.
func TestInstall_RefusesWhenAnotherAddonOwnsTheDestination(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()

	p := newFakeProvider()
	p.register("first", "1.0.0", addonFile("agents/rules/shared.md", "from first\n"))
	_, err := Install(ctx, dir, p, "first", "")
	require.NoError(t, err)
	before := snapshotTeam(t, dir)

	p.register("second", "1.0.0",
		addonFile("agents/rules/shared.md", "from second\n"),
		addonFile("agents/rules/second-only.md", "would also have landed\n"),
	)
	_, err = Install(ctx, dir, p, "second", "")
	require.Error(t, err)
	var collision *CollisionError
	require.ErrorAs(t, err, &collision)
	assert.Equal(t, []string{"agents/rules/shared.md"}, collision.Paths)

	got, err := os.ReadFile(filepath.Join(dir, "agents", "rules", "shared.md"))
	require.NoError(t, err)
	assert.Equal(t, "from first\n", string(got), "the first add-on's file must survive untouched")
	requireAbsent(t, filepath.Join(dir, "agents", "rules", "second-only.md"),
		"no file from the refused add-on may land, even one with no collision of its own")

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	_, ok := lock.Find("second")
	assert.False(t, ok, "a refused add-on must have no lock record")
	before.requireUnchanged(t, dir, "install colliding with another add-on's owned path")
}

// TestUpdate_RefusesWhenTheNewVersionCollidesWithHandAuthoredContent —
// boundary 4 on the update path.
//
// Failure prevented: an update that adds a NEW path silently overwriting
// hand-authored content, and — worse — doing so after it has already
// overwritten the paths it does own, leaving a version nobody chose.
func TestUpdate_RefusesWhenTheNewVersionCollidesWithHandAuthoredContent(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()

	p := newFakeProvider()
	p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	writeRaw(t, dir, "agents/rules/hello-style.md", "hand-authored, not ox's\n")
	runGit(t, dir, "add", "--", "agents/rules/hello-style.md")
	runGit(t, dir, "commit", "--quiet", "-m", "hand-authored rule")
	before := snapshotTeam(t, dir)

	p.register("hello", "2.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v2\n"),
		addonFile("agents/rules/hello-style.md", "the add-on's version\n"),
	)
	_, err = Update(ctx, dir, p, "hello")
	require.Error(t, err)
	var collision *CollisionError
	require.ErrorAs(t, err, &collision)
	assert.Equal(t, []string{"agents/rules/hello-style.md"}, collision.Paths)

	got, err := os.ReadFile(filepath.Join(dir, "agents", "skills", "hello", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(got), "the owned file must NOT be updated when the operation is refused")
	before.requireUnchanged(t, dir, "update colliding with hand-authored content")
}

// TestInstall_RollsBackWhenGitAddFails — boundary 9. A corrupt .git/index is
// the portable "corrupt bytes where a parser expects structure" lever from
// .claude/rules/testing.md, and it is realistic: an interrupted git operation
// produces exactly this.
//
// Failure prevented: an unreadable index read as an empty index. If
// gitTracksPath answered (false, nil) on error, a Team Context whose index ox
// cannot read would look like one where every path is free — and install
// would overwrite tracked content. The gate that actually fires here is the
// collision check, BEFORE any write, which is the strongest possible outcome.
func TestInstall_RollsBackWhenGitAddFails(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	treeBefore := teamTree(t, dir)

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "index"),
		[]byte("not an index, just bytes\x00\x01\x02"), 0o644))

	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/n.md", "notes\n"),
	)

	_, err := Install(context.Background(), dir, p, "hello", "")
	require.Error(t, err, "an unreadable git index must not be treated as a clean, empty one")

	// teamTree, not snapshotTeam: git itself cannot answer anything about a
	// repository with a corrupt index, so the filesystem is the only honest
	// witness here.
	requireAbsent(t, filepath.Join(dir, "agents", "skills", "hello", "SKILL.md"), "no add-on file may survive")
	requireNoLockFile(t, dir, "no lock may survive")
	assert.Equal(t, treeBefore, teamTree(t, dir), "the checkout must be exactly as it was found")
}

// TestInstall_RollsBackWhenTheCommitIsRejected — boundary 10 on the INSTALL
// path. install_test.go covers it for Update (restoring files that exist at
// HEAD); this covers the all-new-paths branch, where rollback must DELETE
// rather than restore.
//
// Failure prevented: ADR-032 D5's permanent GC wedge. A failed install that
// leaves even one untracked file makes isCheckoutClean() report the clone
// dirty forever, blocking blue-green reclone — with nothing saying why. So
// the assertion is on porcelain emptiness, not just on the absence of the
// files ox meant to write.
func TestInstall_RollsBackWhenTheCommitIsRejected(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	before := snapshotTeam(t, dir)
	installFailingPreCommitHook(t, dir)

	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/n.md", "notes\n"),
		addonFile("agents/rules/hello-style.md", "# style\n"),
	)

	_, err := Install(context.Background(), dir, p, "hello", "")
	require.Error(t, err, "a rejecting pre-commit hook must fail the install")

	assert.Empty(t, runGit(t, dir, "status", "--porcelain"),
		"a failed install must leave NOTHING untracked — an untracked file in a Team Context wedges GC permanently (ADR-032 D5)")
	requireNoLockFile(t, dir, "a failed install must not leave the lock behind")
	requireAbsent(t, filepath.Join(dir, "agents", "skills", "hello", "SKILL.md"), "no written file may survive")
	requireAbsent(t, filepath.Join(dir, "agents", "rules", "hello-style.md"), "no written file may survive")
	before.requireUnchanged(t, dir, "rolling back an install whose commit was rejected")
}

// TestInstall_RollsBackOnAnUnbornBranch covers rollback's other unstage
// branch: with no HEAD to reset to, index entries must be dropped outright
// with `rm --cached --sparse`.
//
// Failure prevented: a freshly cloned-but-empty Team Context (no commits yet)
// keeping a staged, never-committed add-on after a failed install — a state
// where `ox addons list` says nothing is installed while git says otherwise.
func TestInstall_RollsBackOnAnUnbornBranch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet", "--initial-branch=main")
	runGit(t, dir, "config", "user.name", "Team Test")
	runGit(t, dir, "config", "user.email", "team-test@example.com")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	installFailingPreCommitHook(t, dir)

	p := newFakeProvider()
	p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))

	_, err := Install(context.Background(), dir, p, "hello", "")
	require.Error(t, err)

	assert.Empty(t, gitLsFiles(t, dir), "nothing may remain staged on an unborn branch after a failed install")
	assert.Empty(t, runGit(t, dir, "status", "--porcelain"), "nothing untracked may remain either")
	requireNoLockFile(t, dir, "no lock may survive")
}

// --- I. concurrent writers ------------------------------------------------

// TestConcurrentInstalls_DifferentAddonsBothLandWhole proves gitutil.WithRepoLock
// serializes the whole transaction, not just its git calls.
//
// Failure prevented: two installs interleaving. Both load the lock, both
// write, and the second's whole-file lock rewrite drops the first add-on's
// ownership record — after its files are already on disk, where nothing owns
// them and nothing will ever remove them.
//
// Synchronized with a start barrier, never a sleep.
func TestConcurrentInstalls_DifferentAddonsBothLandWhole(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	p := newRigged("test:concurrent", twoAddonCatalog()...)

	names := []string{"alpha", "beta"}
	errs := make([]error, len(names))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			<-start
			_, errs[i] = Install(context.Background(), dir, p, name, "")
		}(i, name)
	}
	close(start)
	wg.Wait()

	for i, name := range names {
		require.NoError(t, errs[i], "%s must install even when racing", name)
	}

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	require.Len(t, lock.Addons, 2, "a racing install must not drop the other's ownership record")
	requireLockAgreesWithDisk(t, dir, "alpha")
	requireLockAgreesWithDisk(t, dir, "beta")

	tracked := gitLsFiles(t, dir)
	assert.Contains(t, tracked, "agents/skills/alpha/SKILL.md")
	assert.Contains(t, tracked, "agents/rules/beta-style.md")
	assert.Contains(t, tracked, LockRelativePath)
	assert.True(t, gitStatusClean(t, dir), "two racing installs must leave a clean checkout")
}

// TestConcurrentInstalls_SameAddonExactlyOneWins.
//
// Failure prevented: two `ox addons install post-cutoff` invocations both
// believing they installed it, producing two lock records for one add-on —
// after which Remove deletes the files and leaves the duplicate record
// claiming to own them.
func TestConcurrentInstalls_SameAddonExactlyOneWins(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	p := newRigged("test:concurrent", twoAddonCatalog()...)

	const racers = 4
	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = Install(context.Background(), dir, p, "alpha", "")
		}(i)
	}
	close(start)
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		assert.ErrorIs(t, err, ErrAlreadyInstalled,
			"a loser must lose by name — 'already installed' — not with a corruption error")
	}
	assert.Equal(t, 1, wins, "exactly one racer may install")

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	assert.Len(t, lock.Addons, 1, "one add-on must produce exactly one lock record")
	requireLockAgreesWithDisk(t, dir, "alpha")
	assert.True(t, gitStatusClean(t, dir))
}

// TestConcurrentRemoveAndInstall_LeaveAConsistentSelection races the two
// verbs that both rewrite the whole lock.
//
// Failure prevented: a remove reading the lock before a concurrent install
// commits, then rewriting it without the newly installed add-on — losing an
// ownership record for files that are already on disk.
func TestConcurrentRemoveAndInstall_LeaveAConsistentSelection(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newRigged("test:concurrent", twoAddonCatalog()...)
	_, err := Install(ctx, dir, p, "alpha", "")
	require.NoError(t, err)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var removeErr, installErr error
	wg.Add(2)
	go func() { defer wg.Done(); <-start; _, removeErr = Remove(ctx, dir, "alpha") }()
	go func() { defer wg.Done(); <-start; _, installErr = Install(ctx, dir, p, "beta", "") }()
	close(start)
	wg.Wait()

	require.NoError(t, removeErr)
	require.NoError(t, installErr)

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	_, hasAlpha := lock.Find("alpha")
	assert.False(t, hasAlpha, "the removed add-on must be gone from the selection")
	_, hasBeta := lock.Find("beta")
	assert.True(t, hasBeta, "the concurrently installed add-on must survive in the selection")
	requireLockAgreesWithDisk(t, dir, "beta")
	assert.True(t, gitStatusClean(t, dir))
}

// --- J. state transitions -------------------------------------------------

// TestLifecycle_InstallEditUpdateRemove walks the whole state machine in one
// checkout, asserting the checkout is committed and clean at every step.
//
// Failure prevented: a transition that works in isolation and corrupts state
// in sequence — an update that leaves the lock's file list stale so the
// following remove misses a path, or a remove that leaves a directory behind
// so the next install collides with itself.
func TestLifecycle_InstallEditUpdateRemove(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/keep.md", "keep\n"),
		addonFile("agents/skills/hello/references/drop.md", "drop\n"),
	)

	// clean -> installed
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)
	require.True(t, gitStatusClean(t, dir), "install must commit what it wrote")
	requireLockAgreesWithDisk(t, dir, "hello")

	// installed -> edited by the team
	edited := filepath.Join(dir, "agents", "skills", "hello", "references", "keep.md")
	require.NoError(t, os.WriteFile(edited, []byte("the team's own words\n"), 0o644))
	runGit(t, dir, "add", "--", "agents/skills/hello/references/keep.md")
	runGit(t, dir, "commit", "--quiet", "-m", "team edit")

	// edited -> updated (overwrites, names the edit, drops what v2 dropped)
	p.register("hello", "2.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v2\n"),
		addonFile("agents/skills/hello/references/keep.md", "v2 keep\n"),
	)
	result, err := Update(ctx, dir, p, "hello")
	require.NoError(t, err)
	assert.Equal(t, []string{"agents/skills/hello/references/keep.md"}, result.Modified,
		"the update must name exactly the file the team edited")
	assert.Equal(t, []string{"agents/skills/hello/references/drop.md"}, result.Removed)
	got, err := os.ReadFile(edited)
	require.NoError(t, err)
	assert.Equal(t, "v2 keep\n", string(got), "ADR-032 D4: the update overwrites the edit")
	require.True(t, gitStatusClean(t, dir), "update must commit what it wrote")
	requireLockAgreesWithDisk(t, dir, "hello")

	// updated -> removed
	removeResult, err := Remove(ctx, dir, "hello")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"agents/skills/hello/SKILL.md",
		"agents/skills/hello/references/keep.md",
	}, removeResult.Removed, "remove must delete exactly what the CURRENT lock owns, not the original install's file list")
	requireAbsent(t, filepath.Join(dir, "agents", "skills", "hello"), "the add-on's directory must be pruned")
	require.True(t, gitStatusClean(t, dir), "remove must commit its deletions")

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	assert.Empty(t, lock.Addons)

	// removed -> installed again: the removed paths must no longer count as
	// occupied, so a reinstall is a clean install and not a collision.
	_, err = Install(ctx, dir, p, "hello", "")
	require.NoError(t, err, "reinstalling after a remove must not collide with the remove's own leftovers")
	requireLockAgreesWithDisk(t, dir, "hello")
	assert.True(t, gitStatusClean(t, dir))
}

// TestUpdate_ToleratesAnOwnedFileDeletedByHand: the human deleted an owned
// file and committed the deletion, and the new version still ships that path.
//
// Failure prevented: a path the lock owns that a human already deleted making
// the whole update fail. The lock is a record of intent, not a guarantee
// about the filesystem, and an add-on operation must converge rather than
// refuse. The deleted file is also not "modified" — there is nothing to
// compare — so reporting it would be a false claim about a team edit.
//
// This case works because the update WRITES the path again, putting it back
// in the index before the path-scoped commit runs. The variant where the new
// version drops it instead does not work; see
// TestOwnedPathAlreadyUntracked_WedgesTheOperation. Keeping both in the suite
// is deliberate — the difference between them is the whole defect.
func TestUpdate_ToleratesAnOwnedFileDeletedByHand(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/n.md", "notes\n"),
	)
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	runGit(t, dir, "rm", "--quiet", "--", "agents/skills/hello/references/n.md")
	runGit(t, dir, "commit", "--quiet", "-m", "a human deleted an owned file")

	p.register("hello", "2.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v2\n"),
		addonFile("agents/skills/hello/references/n.md", "v2 notes\n"),
	)
	result, err := Update(ctx, dir, p, "hello")
	require.NoError(t, err, "an owned path that is already gone must not fail the update")
	assert.Empty(t, result.Modified, "a file that is absent was not modified; claiming otherwise is a false report")

	got, err := os.ReadFile(filepath.Join(dir, "agents", "skills", "hello", "references", "n.md"))
	require.NoError(t, err)
	assert.Equal(t, "v2 notes\n", string(got), "the update must restore the path it owns")
	requireLockAgreesWithDisk(t, dir, "hello")
	assert.True(t, gitStatusClean(t, dir))
}

// TestOwnedPathAlreadyUntracked_WedgesTheOperation documents a DEFECT, and it
// is the most consequential one in this file.
//
// Every layer of the transaction is written to tolerate an owned path a human
// already deleted: tx.remove ignores fs.ErrNotExist, and commitTeamWrite
// stages removals with `git rm --cached --sparse --ignore-unmatch`
// specifically because "a removed path may already be gone from git
// entirely". The FINAL step then throws that away. install.go:613 builds one
// path-scoped commit over written ∪ removed:
//
//	args := append([]string{"commit", "-m", message, "--"}, all...)
//
// and `git commit -- <pathspec>` aborts the WHOLE commit when any single
// pathspec matches nothing in the index or the worktree — which is exactly
// what a path the human already `git rm`'d is. Observed:
//
//	record the add-ons change in the Team Context: git commit: error:
//	pathspec 'agents/skills/hello/references/n.md' did not match any
//	file(s) known to git: exit status 1
//
// The damage is not one failed command. Rollback restores the lock, so the
// add-on is still installed and still owns the missing path — so the next
// attempt fails identically, forever. A team that deletes one file of an
// add-on can never remove that add-on, and the message they get names a
// pathspec rather than a cause.
//
// FIXED in commitTeamWrite: the commit pathspec is now built from removals git
// actually knew about (gitTracksPath before the `rm --cached`), so the
// tolerance the two earlier steps already implement — tx.remove ignoring
// fs.ErrNotExist, the unstage passing --ignore-unmatch — survives to the
// commit instead of being discarded by it.
//
// This test was written to PIN the defect and is now inverted to require the
// correct behavior. Both entry points are exercised: a remove, and an update
// that drops the already-deleted path.
//
// Failure prevented: a single hand-deleted file making an add-on PERMANENTLY
// unremovable. `git commit -- <pathspec>` aborts the whole commit when any one
// pathspec matches nothing git knows, so the operation failed, rolled back,
// left the lock intact, and failed identically on every retry — forever.
func TestOwnedPathAlreadyUntracked_DoesNotWedgeTheOperation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// run is the second, real operation — the one that must not wedge.
		run func(ctx context.Context, dir string, p *fakeProvider) (Result, error)
	}{
		{
			name: "remove",
			run: func(ctx context.Context, dir string, _ *fakeProvider) (Result, error) {
				return Remove(ctx, dir, "hello")
			},
		},
		{
			name: "update that drops the deleted path",
			run: func(ctx context.Context, dir string, p *fakeProvider) (Result, error) {
				p.register("hello", "2.0.0", addonFile("agents/skills/hello/SKILL.md", "v2\n"))
				return Update(ctx, dir, p, "hello")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newTeamRepo(t)
			ctx := context.Background()
			p := newFakeProvider()
			p.register("hello", "1.0.0",
				addonFile("agents/skills/hello/SKILL.md", "v1\n"),
				addonFile("agents/skills/hello/references/n.md", "notes\n"),
			)
			_, err := Install(ctx, dir, p, "hello", "")
			require.NoError(t, err)

			// A human deletes one of the add-on's files and commits it. The
			// lock still owns the path; git no longer knows it.
			runGit(t, dir, "rm", "--quiet", "--", "agents/skills/hello/references/n.md")
			runGit(t, dir, "commit", "--quiet", "-m", "a human deleted an owned file")

			_, err = tt.run(ctx, dir, p)
			require.NoError(t, err,
				"%s must survive the lock owning a path git no longer knows — otherwise one "+
					"hand-deleted file makes the add-on permanently unremovable", tt.name)

			// The selection actually changed, which is what proves the commit
			// landed rather than being rolled back with a swallowed error.
			lock, lockErr := LoadLock(dir)
			require.NoError(t, lockErr)
			switch tt.name {
			case "remove":
				_, stillInstalled := lock.Find("hello")
				assert.False(t, stillInstalled, "remove must drop the lock record")
			default:
				rec, stillInstalled := lock.Find("hello")
				require.True(t, stillInstalled, "update must leave the add-on installed")
				assert.Equal(t, "2.0.0", rec.Version, "update must record the new version")
				for _, f := range rec.Files {
					assert.NotEqual(t, "agents/skills/hello/references/n.md", f.Path,
						"update must drop the path the new version no longer ships")
				}
			}

			// And the checkout is left clean — no stray files, nothing
			// half-staged. An untracked file here wedges GC permanently.
			assert.Empty(t, runGit(t, dir, "status", "--porcelain"),
				"the Team Context must be clean after the operation")
		})
	}
}

// TestRemove_ToleratesAnOwnedFileDeletedFromDiskOnly is the same shape with
// the human's deletion NOT staged — the far more common case, since a teammate
// or a stray script usually just deletes the file.
//
// This one works, and the contrast is the point: the tolerance
// commitTeamWrite implements covers a path that is absent from disk but still
// in the index, and only breaks when the path is absent from BOTH. Keeping
// both cases in the suite means a fix for one cannot silently regress the
// other.
func TestRemove_ToleratesAnOwnedFileDeletedFromDiskOnly(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/n.md", "notes\n"),
	)
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	// Deleted from the worktree only: git still has the path in its index.
	require.NoError(t, os.Remove(filepath.Join(dir, "agents", "skills", "hello", "references", "n.md")))

	result, err := Remove(ctx, dir, "hello")
	require.NoError(t, err, "a lock-owned path already gone from disk must not fail the remove")
	assert.ElementsMatch(t, []string{
		"agents/skills/hello/SKILL.md",
		"agents/skills/hello/references/n.md",
	}, result.Removed, "remove reports every path it stopped owning, including one already absent")
	assert.Empty(t, result.Modified, "a file that is absent was not modified; claiming otherwise is a false report")

	lock, err := LoadLock(dir)
	require.NoError(t, err)
	_, ok := lock.Find("hello")
	assert.False(t, ok, "the ownership record must be cleared even when a file was already gone")
	requireAbsent(t, filepath.Join(dir, "agents", "skills", "hello"), "the add-on's directory must be pruned")
	assert.True(t, gitStatusClean(t, dir))
}

// TestUpdate_ChangedBytesChangeTheLockedDigest is the in-package half of
// ADR-032's capability-delta claim: "an update replaces owned bytes
// wholesale, so a previously approved add-on skill whose content changed
// returns to needing approval — the pin names bytes, not names."
//
// The approval gate itself lives elsewhere; what this package owes it is a
// lock whose per-file digest actually moves when the bytes move, and stays
// put when they do not. A digest that did not change would silently carry a
// stale approval across a content change.
func TestUpdate_ChangedBytesChangeTheLockedDigest(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/stable.md", "unchanged across versions\n"),
	)
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	beforeLock, err := LoadLock(dir)
	require.NoError(t, err)
	_, beforeSkill, ok := beforeLock.Owns("agents/skills/hello/SKILL.md")
	require.True(t, ok)
	_, beforeStable, ok := beforeLock.Owns("agents/skills/hello/references/stable.md")
	require.True(t, ok)

	p.register("hello", "2.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v2 — different bytes\n"),
		addonFile("agents/skills/hello/references/stable.md", "unchanged across versions\n"),
	)
	_, err = Update(ctx, dir, p, "hello")
	require.NoError(t, err)

	afterLock, err := LoadLock(dir)
	require.NoError(t, err)
	_, afterSkill, ok := afterLock.Owns("agents/skills/hello/SKILL.md")
	require.True(t, ok)
	_, afterStable, ok := afterLock.Owns("agents/skills/hello/references/stable.md")
	require.True(t, ok)

	assert.NotEqual(t, beforeSkill.Digest, afterSkill.Digest,
		"changed bytes must change the pinned digest, or a stale approval survives the change")
	assert.Equal(t, beforeStable.Digest, afterStable.Digest,
		"unchanged bytes must keep their digest, or every update re-asks for approval of files nobody touched")
}

// --- K. the Result / Lock JSON other tools parse --------------------------

// TestResultJSON_KeysAndTypes pins the machine-readable contract `ox addons
// install|update|remove --json` emits. Keys and types only — never the human
// prose the renderer produces, which is free to change.
//
// Failure prevented: an absent key. `written: null` breaks a consumer doing
// `.written.length` where `written: []` does not, and the same reasoning that
// keeps update_available non-omitempty in the list output applies here: an
// absent array cannot be told apart from an ox too old to report it.
func TestResultJSON_KeysAndTypes(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/skills/hello/references/drop.md", "drop\n"),
	)

	installResult, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)
	p.register("hello", "2.0.0", addonFile("agents/skills/hello/SKILL.md", "v2\n"))
	updateResult, err := Update(ctx, dir, p, "hello")
	require.NoError(t, err)
	removeResult, err := Remove(ctx, dir, "hello")
	require.NoError(t, err)

	// A refusal's Result is emitted too — the CLI renders the error, but the
	// Result carries the paths, so its shape is part of the contract.
	writeRaw(t, dir, "agents/rules/taken.md", "hand-authored\n")
	runGit(t, dir, "add", "--", "agents/rules/taken.md")
	runGit(t, dir, "commit", "--quiet", "-m", "hand-authored rule")
	p.register("colliding", "1.0.0", addonFile("agents/rules/taken.md", "mine now\n"))
	refusedResult, err := Install(ctx, dir, p, "colliding", "")
	require.Error(t, err)

	tests := []struct {
		name     string
		result   Result
		wantOp   string
		wantKeys []string
	}{
		{"install", installResult, "install", []string{"addon", "op", "source", "version", "written", "removed"}},
		{"update", updateResult, "update", []string{"addon", "op", "source", "version", "written", "removed"}},
		{"remove", removeResult, "remove", []string{"addon", "op", "source", "version", "written", "removed"}},
		{"refused install", refusedResult, "install", []string{"addon", "op", "written", "removed", "refused"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tt.result)
			require.NoError(t, err)

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded))

			for _, key := range tt.wantKeys {
				require.Contains(t, decoded, key, "the JSON contract requires the %q key", key)
			}
			assert.Equal(t, tt.wantOp, decoded["op"])
			assert.IsType(t, "", decoded["addon"])
			assert.IsType(t, []any{}, decoded["written"], "written must always be an array, never null")
			assert.IsType(t, []any{}, decoded["removed"], "removed must always be an array, never null")

			// modified and refused are omitempty — present only when they say
			// something. When present they must still be arrays.
			if v, ok := decoded["refused"]; ok {
				assert.IsType(t, []any{}, v)
			}
			if v, ok := decoded["modified"]; ok {
				assert.IsType(t, []any{}, v)
			}
		})
	}

	assert.NotEmpty(t, refusedResult.Refused, "a collision must report the refused paths on the Result, not only on the error")
	assert.Empty(t, refusedResult.Written, "a refused install must report nothing written")
}

// TestLockJSON_KeysAndTypes pins the committed lock's on-disk shape. It is
// read by other ox versions and reviewed by humans in a pull request, so its
// keys are a contract in exactly the way Result's are.
func TestLockJSON_KeysAndTypes(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	p := newFakeProvider()
	p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))
	_, err := Install(context.Background(), dir, p, "hello", "")
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(lockFileBytes(t, dir), &decoded))

	require.Contains(t, decoded, "schema_version")
	assert.EqualValues(t, LockSchemaVersion, decoded["schema_version"])
	require.Contains(t, decoded, "addons")
	addonsAny, ok := decoded["addons"].([]any)
	require.True(t, ok, "addons must be an array")
	require.Len(t, addonsAny, 1)

	entry, ok := addonsAny[0].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"addon", "source", "version", "digest", "installed_at", "files"} {
		require.Contains(t, entry, key, "the lock contract requires the %q key", key)
	}
	assert.Equal(t, "hello", entry["addon"])
	_, timeErr := time.Parse(time.RFC3339, entry["installed_at"].(string))
	assert.NoError(t, timeErr, "installed_at must be RFC3339 so any reader can parse it")

	files, ok := entry["files"].([]any)
	require.True(t, ok, "files must be an array")
	require.Len(t, files, 1)
	file, ok := files[0].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"path", "digest", "mode"} {
		require.Contains(t, file, key, "the lock contract requires the per-file %q key", key)
	}
	assert.Equal(t, "agents/skills/hello/SKILL.md", file["path"])
	assert.Contains(t, file["digest"], "sha256:", "the per-file digest must name its algorithm")
}

// TestDescriptorJSON_AlwaysReportsHasScripts.
//
// Failure prevented: the same absent-boolean trap `ox addons list` guards for
// update_available. has_scripts false is the ANSWER to "does this add-on
// carry runnable scripts"; an absent key cannot be told apart from an ox too
// old to report it, and the answer gates `ox skills approve --allow-scripts`.
func TestDescriptorJSON_AlwaysReportsHasScripts(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(Descriptor{Name: "hello", Version: "1.0.0", HasScripts: false})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Contains(t, decoded, "has_scripts",
		"has_scripts must never be omitempty: false is an answer, absence is not")
	assert.Equal(t, false, decoded["has_scripts"])
}

// --- L. a provider that fails mid-lifecycle -------------------------------

// TestMutatingOps_ProviderFailureMutatesNothing covers the whole class a
// remote provider introduces and the embedded one cannot produce: an add-on
// yanked between list and install, an offline cache miss, a network error, a
// revoked credential.
//
// Failure prevented: a resolve failure being partially applied. Resolve runs
// before the transaction opens the checkout, so the correct outcome is that
// nothing is touched at all — including the lock, whose absence is how ox
// knows nothing is installed.
func TestMutatingOps_ProviderFailureMutatesNothing(t *testing.T) {
	t.Parallel()

	failures := []struct {
		name string
		err  error
	}{
		{"yanked between list and install", fmt.Errorf("%q was withdrawn by its publisher: %w", "hello", ErrAddonNotFound)},
		{"offline with no cached copy", errors.New("catalog unreachable: no cached copy of this add-on")},
		{"credential revoked", errors.New("403 forbidden")},
	}

	for _, f := range failures {
		t.Run(f.name+"/install", func(t *testing.T) {
			t.Parallel()
			dir := newTeamRepo(t)
			before := snapshotTeam(t, dir)

			p := newFakeProvider()
			p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))
			p.err = f.err

			_, err := Install(context.Background(), dir, p, "hello", "")
			require.ErrorIs(t, err, f.err, "the provider's own failure must reach the caller intact")
			requireNoLockFile(t, dir, "a provider failure must not write a lock")
			before.requireUnchanged(t, dir, "install when the provider fails to resolve")
		})

		t.Run(f.name+"/update", func(t *testing.T) {
			t.Parallel()
			dir := newTeamRepo(t)
			ctx := context.Background()
			p := newFakeProvider()
			p.register("hello", "1.0.0", addonFile("agents/skills/hello/SKILL.md", "v1\n"))
			_, err := Install(ctx, dir, p, "hello", "")
			require.NoError(t, err)
			before := snapshotTeam(t, dir)

			p.err = f.err
			_, err = Update(ctx, dir, p, "hello")
			require.ErrorIs(t, err, f.err)
			before.requireUnchanged(t, dir, "update when the provider fails to resolve")

			// And the installed version must still be usable afterwards: a
			// failed update may not leave the selection in a state where the
			// add-on can no longer be removed.
			_, err = Remove(ctx, dir, "hello")
			require.NoError(t, err, "a failed update must leave the add-on removable")
		})
	}
}

// TestRemove_NeedsNoProvider is the property that makes the section above
// matter: Remove takes no Provider at all, so a team can always retire an
// add-on whose catalog is gone — yanked, offline, or published by a third
// party that no longer exists.
//
// Failure prevented: content a team cannot remove because the thing that
// supplied it is unreachable.
func TestRemove_NeedsNoProvider(t *testing.T) {
	t.Parallel()
	dir := newTeamRepo(t)
	ctx := context.Background()

	// Install through a provider, then discard it entirely — Remove reads the
	// lock, which is the whole point of recording ownership there.
	p := newFakeProvider()
	p.register("hello", "1.0.0",
		addonFile("agents/skills/hello/SKILL.md", "v1\n"),
		addonFile("agents/rules/hello-style.md", "# style\n"),
	)
	_, err := Install(ctx, dir, p, "hello", "")
	require.NoError(t, err)

	result, err := Remove(ctx, dir, "hello")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", result.Version, "remove reports the version it retired, from the lock")
	assert.Equal(t, p.Source(), result.Source)
	assert.Len(t, result.Removed, 2)
	assert.True(t, gitStatusClean(t, dir))
}

// TestInstall_RefusesAProviderWhoseDigestDoesNotMatchItsBytes closes the last
// place a provider claim was taken at face value.
//
// The add-on digest is what the lock records as the team's selection, what a
// reviewer approved, and what `ox addons list` compares to decide whether an
// update is available. It used to be copied into the lock verbatim, so a
// provider could advertise the digest a team reviewed and ship different
// content — and every consumer downstream would then trust the label instead
// of the bytes.
//
// Failure prevented: a team reviewing one add-on and installing another.
func TestInstall_RefusesAProviderWhoseDigestDoesNotMatchItsBytes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		digest func(honest string) string
		want   string
	}{
		{
			name:   "a digest naming content the provider did not ship",
			digest: func(string) string { return "sha256:0000000000000000000000000000000000000000000000000000000000000000" },
			want:   "does not match what it claims to be",
		},
		{
			name:   "no digest at all",
			digest: func(string) string { return "" },
			want:   "cannot be pinned",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := newTeamRepo(t)
			p := newFakeProvider()
			p.register("liar", "1.0.0", addonFile("agents/skills/liar/SKILL.md", "real bytes\n"))

			// register computes an honest digest; overwrite it to lie.
			honest := p.versions["liar"]["1.0.0"].Digest
			r := p.versions["liar"]["1.0.0"]
			r.Digest = tc.digest(honest)
			p.versions["liar"]["1.0.0"] = r

			_, err := Install(context.Background(), dir, p, "liar", "")
			require.Error(t, err, "the installer must verify the advertised digest against the bytes")
			assert.Contains(t, err.Error(), tc.want)

			// Nothing was written, and no lock records the rejected add-on.
			assert.Empty(t, runGit(t, dir, "status", "--porcelain"),
				"a refused install must leave the Team Context untouched")
			lock, lockErr := LoadLock(dir)
			require.NoError(t, lockErr)
			_, installed := lock.Find("liar")
			assert.False(t, installed, "a refused add-on must not appear in the lock")
		})
	}
}
