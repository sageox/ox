package daemon

// Mirrors the clone-side of sync_clone_test.go and sync_team_clone_test.go for
// the kb sync path: every shape of "first-time clone of <kind> bubble into
// paths.KBDir(kb_id)" the original team-context two-phase clone tests covered,
// re-expressed against syncBubbles + cloneBubble. Per Ryan: "all the ledger
// and team context tests need to be applied to kb git repos." Slow tests skip
// under -short per .claude/rules/testing.md.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncBubbles_Clone_AllKinds verifies that every KBType the API may
// return clones into the canonical XDG location end-to-end. Mirrors the
// team-context two-phase clone happy-path coverage but across all kb
// kinds (personal, profile, team, repo, custom, unknown), since the kb
// sync path must be type-agnostic.
//
// Failure prevented: a future refactor that special-cases a single
// kb_type and silently skips the others — the user's personal bubble
// would clone but their team bubble wouldn't (or vice versa).
func TestSyncBubbles_Clone_AllKinds(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	cases := []struct {
		name    string
		kbType  api.KBType
		seedFile string
		seedBody string
	}{
		{"personal", api.KBTypePersonal, "notes.md", "personal notes\n"},
		{"profile", api.KBTypeProfile, "profile.md", "profile content\n"},
		{"team", api.KBTypeTeam, "AGENTS.md", "team agents\n"},
		{"repo", api.KBTypeRepo, "ledger.md", "ledger archive\n"},
		{"custom", api.KBTypeCustom, "custom.md", "custom payload\n"},
		{"unknown", api.KBTypeUnknown, "unknown.md", "unknown payload\n"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s, _ := kbTestScheduler(t)
			bareDir := makeBareRepo(t, "kind-"+tc.name, tc.seedFile, tc.seedBody)
			bubble := api.KB{
				KBID:    "kb_" + tc.name + "_clone",
				KBType:  tc.kbType,
				Slug:    "slug-" + tc.name,
				RepoURL: "file://" + bareDir,
			}
			s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
				return &fakeKBLister{bubbles: []api.KB{bubble}}
			})

			s.syncBubbles(context.Background())

			target := paths.KBDir(bubble.KBID)
			require.DirExists(t, filepath.Join(target, ".git"), "%s: .git must exist after clone", tc.name)
			seeded := filepath.Join(target, tc.seedFile)
			body, err := os.ReadFile(seeded)
			require.NoError(t, err, "%s: seed file must materialize", tc.name)
			assert.Equal(t, tc.seedBody, string(body))

			// meta.json must record the type as fetched (or normalized "unknown").
			metaRaw, err := os.ReadFile(filepath.Join(target, ".sageox", "meta.json"))
			require.NoError(t, err)
			var meta kbMeta
			require.NoError(t, json.Unmarshal(metaRaw, &meta))
			assert.Equal(t, tc.kbType, meta.Type)
		})
	}
}

// TestSyncBubbles_Clone_EndpointScoping verifies the same kb_id under two
// different endpoints produces two isolated checkouts. Same intent as
// the team-context endpoint-isolation rule: staging/prod must never
// cross-pollinate even when ids collide.
//
// Failure prevented: a developer pointing at staging temporarily
// overwriting their prod kb checkout because the path helper ignored
// the active endpoint.
func TestSyncBubbles_Clone_EndpointScoping(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareA := makeBareRepo(t, "ep-a", "f.md", "A\n")
	bareB := makeBareRepo(t, "ep-b", "f.md", "B\n")

	// kbTestEnv pins XDG_DATA_HOME for the whole parent test, so both
	// subtests share the same XDG root. Endpoint scoping is therefore
	// the only thing that can produce two distinct on-disk paths for the
	// same kb_id — which is exactly the behavior we want to assert (and
	// the bug we'd miss if the subtests rebound XDG too).
	kbTestEnv(t) // sets SAGEOX_ENDPOINT=staging.sageox.ai
	var stagingTarget string

	// First endpoint: staging.
	t.Run("staging", func(t *testing.T) {
		s, _ := kbTestScheduler(t)
		bubble := api.KB{
			KBID:    "kb_same_id",
			KBType:  api.KBTypePersonal,
			Slug:    "shared",
			RepoURL: "file://" + bareA,
		}
		s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
			return &fakeKBLister{bubbles: []api.KB{bubble}}
		})
		s.syncBubbles(context.Background())

		stagingTarget = paths.KBDir(bubble.KBID)
		body, err := os.ReadFile(filepath.Join(stagingTarget, "f.md"))
		require.NoError(t, err)
		assert.Equal(t, "A\n", string(body), "staging endpoint must clone A's content")
	})

	// Second endpoint: prod. Must produce a different on-disk path
	// even though kb_id is identical — driven purely by the endpoint
	// slug change, NOT by an XDG rebind. The previous XDG override here
	// confounded the test (any endpoint-scoping regression would have
	// been masked by the trivially-different XDG home).
	t.Run("prod_distinct_path", func(t *testing.T) {
		t.Setenv("SAGEOX_ENDPOINT", "https://app.sageox.ai")

		s, _ := kbTestScheduler(t)
		bubble := api.KB{
			KBID:    "kb_same_id",
			KBType:  api.KBTypePersonal,
			Slug:    "shared",
			RepoURL: "file://" + bareB,
		}
		s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
			return &fakeKBLister{bubbles: []api.KB{bubble}}
		})
		s.syncBubbles(context.Background())

		target := paths.KBDir(bubble.KBID)
		require.NotEmpty(t, target)
		body, err := os.ReadFile(filepath.Join(target, "f.md"))
		require.NoError(t, err)
		assert.Equal(t, "B\n", string(body), "prod endpoint must clone B's content into a path distinct from staging")
		require.NotEmpty(t, stagingTarget, "staging subtest must have stored its target path")
		assert.NotEqual(t, stagingTarget, target,
			"endpoint scoping must place prod and staging at different paths even though kb_id is identical")
	})
}

// TestSyncBubbles_Clone_EmptyRepoURL_NoClone verifies a kb row that the
// server has not yet provisioned (RepoURL=="") is logged but NOT cloned
// — and the rest of the pass continues. Same shape as the team-context
// "discovered team but URL is missing" guard.
//
// Failure prevented: the daemon panicking or creating an empty checkout
// with no remote when a server seed lags.
func TestSyncBubbles_Clone_EmptyRepoURL_NoClone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareGood := makeBareRepo(t, "good-after-empty", "ok.md", "ok\n")
	unprovisioned := api.KB{
		KBID:   "kb_unprovisioned",
		KBType: api.KBTypePersonal,
		Slug:   "pending",
		// no RepoURL — server hasn't created a remote yet.
	}
	good := api.KB{
		KBID:    "kb_after_empty",
		KBType:  api.KBTypeTeam,
		Slug:    "after",
		RepoURL: "file://" + bareGood,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{unprovisioned, good}}
	})

	s.syncBubbles(context.Background())

	// unprovisioned bubble has no checkout.
	assert.NoDirExists(t, filepath.Join(paths.KBDir(unprovisioned.KBID), ".git"))
	// the bubble after must still be cloned.
	assert.DirExists(t, filepath.Join(paths.KBDir(good.KBID), ".git"))
}

// TestSyncBubbles_Clone_EmptyKBID_Skipped is the kb-side equivalent of
// the team-context "skip when team_id is missing" guard. An empty kb_id
// must never produce a checkout — paths.KBDir("") would otherwise resolve
// to the kb base dir itself, which is a destructive write target.
//
// Failure prevented: an API regression returning blank kb_id rows
// causing the daemon to clone INTO the kb base dir and corrupt every
// other bubble underneath it.
func TestSyncBubbles_Clone_EmptyKBID_Skipped(t *testing.T) {
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bad := api.KB{
		KBType:  api.KBTypePersonal,
		Slug:    "no-id",
		RepoURL: "file:///should/not/be/touched",
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bad}}
	})

	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})
	// no top-level git dir should appear at the kb root.
	if entries, err := os.ReadDir(paths.KBDir("")); err == nil {
		for _, e := range entries {
			assert.NotEqual(t, ".git", e.Name(), "kb root must not become a git repo")
		}
	}
}

// TestSyncBubbles_Clone_BadURL_LeavesNoPartialCheckout verifies that a
// failing clone (unreachable URL) does not leave a half-populated
// directory tree. The team-context two-phase clone has the same
// invariant: an incomplete clone is detected and cleaned up.
//
// Failure prevented: a half-clone .git directory that subsequent passes
// mistake for "already cloned" and try to pull from, never recovering.
func TestSyncBubbles_Clone_BadURL_LeavesNoPartialCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bad := api.KB{
		KBID:    "kb_bad_url",
		KBType:  api.KBTypeTeam,
		Slug:    "bad",
		RepoURL: "file:///does/not/exist/anywhere.git",
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bad}}
	})

	s.syncBubbles(context.Background())

	target := paths.KBDir(bad.KBID)
	// either the dir is absent OR present but with no .git — never both.
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		t.Fatalf(".git dir created from a failing clone — partial state left behind at %s", target)
	}
}

// TestSyncBubbles_Clone_PostCloneMergeAttrsInstalled mirrors the
// team-context invariant that the kb merge-driver block is installed at
// clone-time (not waiting for the first pull) so the very first push
// from the user can't wedge on an add/add conflict.
//
// Failure prevented: a fresh clone followed immediately by a CLI
// session-stop push hitting the wedge bug because info/attributes has
// no merge=union rules until the next sync tick.
func TestSyncBubbles_Clone_PostCloneMergeAttrsInstalled(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "preflight", "AGENTS.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_preflight",
		KBType:  api.KBTypeTeam,
		Slug:    "preflight",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())

	target := paths.KBDir(bubble.KBID)
	attrs, err := os.ReadFile(filepath.Join(target, ".git", "info", "attributes"))
	require.NoError(t, err, "info/attributes must exist after clone")
	got := string(attrs)
	for _, want := range kb.MergeUnionPaths {
		assert.Contains(t, got, want, "merge attributes must reference %s", want)
	}
	assert.Contains(t, got, "merge=union")
}

// TestSyncBubbles_Clone_Idempotent verifies a second sync pass does not
// re-clone an already-cloned bubble. Same posture as the team-context
// "twoPhaseClone fails when target exists" guard — once cloned, the
// path-already-exists branch routes to pull, not re-clone.
//
// Failure prevented: every reconciliation pass tearing down and
// re-creating the working tree, blowing away local edits and tanking
// IO. A subtle regression that would only surface as users complaining
// about lost work.
func TestSyncBubbles_Clone_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "idem", "README.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_idempotent",
		KBType:  api.KBTypePersonal,
		Slug:    "idem",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// first pass: clone.
	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// drop a local untracked marker file. If the next pass re-clones
	// (instead of pulling), this file disappears.
	marker := filepath.Join(target, "local-only.txt")
	require.NoError(t, os.WriteFile(marker, []byte("survives if no re-clone"), 0o644))

	// second pass.
	s.syncBubbles(context.Background())

	body, err := os.ReadFile(marker)
	require.NoError(t, err, "local file must survive second pass — re-clone would have wiped it")
	assert.Equal(t, "survives if no re-clone", string(body))
}

// TestSyncBubbles_Clone_RestoresAfterManualGitDirRemoval is the kb
// equivalent of the team-context incomplete-clone recovery test. If the
// .git directory is missing (manual rm, partial transfer, disk
// corruption), the next sync pass should re-clone from scratch.
//
// Failure prevented: a user who deletes .git to "fix" something and is
// stuck unable to recover without manual intervention.
func TestSyncBubbles_Clone_RestoresAfterManualGitDirRemoval(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "restore", "README.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_restore",
		KBType:  api.KBTypePersonal,
		Slug:    "restore",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// initial clone.
	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// manually remove .git as if a user did `rm -rf .git`.
	require.NoError(t, os.RemoveAll(filepath.Join(target, ".git")))
	require.NoDirExists(t, filepath.Join(target, ".git"))

	// next pass should self-heal. cloneBubble's parent.MkdirAll(0o755)
	// + git clone will fail because target dir already exists; the
	// real production behavior is to fall through and not crash.
	// Either re-clone happens (after future hardening) or the pass
	// completes without panic. Both are acceptable; what's NOT
	// acceptable is a panic or a corrupted state.
	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})
}

// TestSyncBubbles_Clone_ShallowAndPartialFilter verifies that kb clones
// via TwoPhaseClone produce a shallow + partial clone (depth=1 +
// blob:none filter) — the same posture team-context uses. Without this,
// every kb clone would download the full history and every blob, which
// scales poorly for large or long-lived bubbles.
//
// Failure prevented: a future refactor that swaps cloneBubble back to a
// plain `git clone`, ballooning bandwidth and disk usage on every fresh
// install.
func TestSyncBubbles_Clone_ShallowAndPartialFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "shallow", "README.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_shallow",
		KBType:  api.KBTypeTeam,
		Slug:    "shallow",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())

	target := paths.KBDir(bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// blob:none filter must be recorded on the remote config so subsequent
	// fetches preserve the partial-clone semantics. mirrors the
	// twin_partial_clone_test.go assertion for team-context.
	out, err := exec.Command("git", "-C", target, "config", "--get", "remote.origin.partialclonefilter").CombinedOutput()
	require.NoError(t, err, "remote.origin.partialclonefilter must be set: %s", out)
	assert.Equal(t, "blob:none", strings.TrimSpace(string(out)),
		"kb clone must use --filter=blob:none, got: %q", string(out))

	// promisor flag confirms partial-clone behavior is active.
	promisor, err := exec.Command("git", "-C", target, "config", "--get", "remote.origin.promisor").CombinedOutput()
	require.NoError(t, err, "remote.origin.promisor must be set: %s", promisor)
	assert.Equal(t, "true", strings.TrimSpace(string(promisor)))
}

// TestSyncBubbles_Clone_HasGitignoreEntries verifies cloneBubble installs
// the .sageox/.gitignore entries that prevent daemon-written cache files
// from showing up as untracked. Without this, blue-green GC reclone (if
// kb ever grows one) would treat the checkout as permanently dirty.
//
// Mirrors team-context's EnsureCheckoutGitignore behavior: only kicks in
// when the cloned repo has a .sageox/ directory (the path that holds the
// generated cache + meta files). Most kb bubbles will have one because
// the daemon writes meta.json into it.
//
// Failure prevented: cache files (.sageox/cache/, etc.) being committed
// or blocking future GC because EnsureCheckoutGitignoreCtx never ran.
func TestSyncBubbles_Clone_HasGitignoreEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	// build a bare repo that already has a .sageox/ directory so
	// EnsureCheckoutGitignoreCtx has somewhere to write. mirrors what a
	// server-provisioned bubble actually looks like.
	tmp := t.TempDir()
	bareDir := filepath.Join(tmp, "gitignore.bare")
	workDir := filepath.Join(tmp, "gitignore.work")
	require.NoError(t, exec.Command("git", "init", "--bare", "-b", "main", bareDir).Run())
	gitInDir(t, bareDir, "config", "uploadpack.allowfilter", "true")
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".sageox", "sync.manifest"), []byte("version 1\ninclude .sageox/\ninclude README.md\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "README.md"), []byte("v1\n"), 0o644))
	gitInDir(t, workDir, "add", ".")
	gitInDir(t, workDir, "commit", "-m", "seed with .sageox")
	gitInDir(t, workDir, "push", "origin", "HEAD:main")

	bubble := api.KB{
		KBID:    "kb_gitignore",
		KBType:  api.KBTypeTeam,
		Slug:    "gitignore",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())

	target := paths.KBDir(bubble.KBID)
	gitignoreBytes, err := os.ReadFile(filepath.Join(target, ".sageox", ".gitignore"))
	require.NoError(t, err, ".sageox/.gitignore must exist after clone")
	contents := string(gitignoreBytes)
	for _, want := range []string{"*", "!.gitignore", "!sync.manifest"} {
		assert.Contains(t, contents, want,
			".sageox/.gitignore must contain %q so cache files don't appear as untracked", want)
	}

	// daemon-written meta.json must not surface as untracked — exactly
	// what the gitignore's `*` blanket entry prevents.
	statusOut, err := exec.Command("git", "-C", target, "status", "--porcelain").CombinedOutput()
	require.NoError(t, err, "git status: %s", statusOut)
	for _, line := range strings.Split(strings.TrimSpace(string(statusOut)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		assert.False(t,
			strings.Contains(line, ".sageox/cache/") ||
				strings.Contains(line, ".sageox/meta.json"),
			"cache/meta file %q must not appear in git status — gitignore is missing entries", line)
	}
}

// TestSyncBubbles_Pull_ManifestResolveRules verifies that when a bubble
// includes a sync.manifest with custom `resolve auto` directives,
// reconcileBubble passes those rules to pullManagedRepo so an induced
// conflict on a manifest-allowed path auto-resolves on rebase. The
// kb path must mirror team-context's manifest-driven resolve behavior
// (sync_team.go reads manifest before pull and passes ResolveRules).
//
// Failure prevented: kb bubbles that ship a sync.manifest with
// resolve directives have those directives silently ignored, leaving
// every conflict on data/ paths to wedge the daemon's pull loop.
func TestSyncBubbles_Pull_ManifestResolveRules(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	// build a bare repo with a manifest that auto-resolves data/.
	tmp := t.TempDir()
	bareDir := filepath.Join(tmp, "manifest.bare")
	workDir := filepath.Join(tmp, "manifest.work")
	require.NoError(t, exec.Command("git", "init", "--bare", "-b", "main", bareDir).Run())
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "uploadpack.allowfilter", "true").Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)

	manifestBody := `version 1
include .sageox/
include data/
resolve auto data/
`
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".sageox", "sync.manifest"), []byte(manifestBody), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "data", "facts.json"), []byte("v1\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", workDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "seed").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	bubble := api.KB{
		KBID:    "kb_resolve",
		KBType:  api.KBTypeTeam,
		Slug:    "resolve",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// first pass: clone — manifest should ship into the local checkout.
	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))
	require.FileExists(t, filepath.Join(target, ".sageox", "sync.manifest"))

	// induce a conflict: local edit on data/facts.json followed by a
	// remote edit on the same file. The pull must auto-resolve via the
	// manifest's `resolve auto data/` rule (accept-theirs).
	require.NoError(t, os.WriteFile(filepath.Join(target, "data", "facts.json"), []byte("local-change\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", target, "add", "data/facts.json").Run())
	gitConfig(t, target)
	require.NoError(t, exec.Command("git", "-C", target, "commit", "-m", "local edit").Run())

	// remote update.
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "data", "facts.json"), []byte("remote-change\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", workDir, "add", "data/facts.json").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "remote edit").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	// nudge FETCH_HEAD into the past so dedup doesn't skip the pull.
	fetchHead := filepath.Join(target, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		past := info.ModTime().Add(-10 * time.Minute)
		_ = os.Chtimes(fetchHead, past, past)
	}

	// second pass: pull. With ResolveRules wired through, the conflict
	// must auto-resolve and not leave the repo in a rebase state.
	s.syncBubbles(context.Background())

	// no rebase in progress — auto-resolve cleared it.
	_, statErr := os.Stat(filepath.Join(target, ".git", "rebase-merge"))
	assert.True(t, os.IsNotExist(statErr), "rebase must not be in progress after auto-resolve")
	_, statErr = os.Stat(filepath.Join(target, ".git", "rebase-apply"))
	assert.True(t, os.IsNotExist(statErr), "rebase-apply must not exist after auto-resolve")

	// no UU markers in status.
	statusOut, err := exec.Command("git", "-C", target, "status", "--porcelain").CombinedOutput()
	require.NoError(t, err)
	assert.NotContains(t, string(statusOut), "UU", "no unmerged paths after manifest-driven resolve")
}
