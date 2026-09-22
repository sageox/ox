//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/manifest"
)

// The bulletin board is the first sync entry that names a subtree rather than
// a top-level directory: bulletin/general/posts/ syncs, and the sibling
// bulletin/general/archive/ never does. These tests pin the doctor check at
// that depth against a real git checkout with the sparse patterns ox itself
// computes.

// fakePostSHA stands in for the content hash the server puts in a post's
// filename. Any 64 lowercase hex characters will do; nothing here parses it.
const fakePostSHA = "7c4a8d09ca3762af61e59520943dc26494f8941b7c4a8d09ca3762af61e59520"

// seedBulletinTeamContext commits the given files as a team context and applies
// the sparse patterns for cfg the same way clone and doctor repair do.
func seedBulletinTeamContext(t *testing.T, files map[string]string, cfg *manifest.ManifestConfig) string {
	t.Helper()
	repo := t.TempDir()
	runGitIn(t, repo, "init", "--initial-branch=main")
	runGitIn(t, repo, "config", "user.email", "t@example.com")
	runGitIn(t, repo, "config", "user.name", "T")
	for rel, content := range files {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	runGitIn(t, repo, "add", "-A")
	runGitIn(t, repo, "commit", "-q", "-m", "seed")
	applySparseSet(t, repo, manifest.SparseSetFor(cfg, manifest.RepoKindTeamContext))
	return repo
}

// applySparseSet mirrors the real clone path: no-cone init, set the computed
// patterns, then checkout HEAD so the working tree matches exactly.
func applySparseSet(t *testing.T, repo string, patterns []string) {
	t.Helper()
	runGitIn(t, repo, "sparse-checkout", "init", "--no-cone")
	runGitIn(t, repo, append([]string{"sparse-checkout", "set", "--no-cone"}, patterns...)...)
	runGitIn(t, repo, "checkout", "-q", "HEAD")
}

func runGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// TestMissingSparseTopLevelDirs_ArchivedOnlyBoardIsNotMissing is the false-FAIL
// regression that was live on main.
//
// Once every post on a board expires, the server moves them to
// bulletin/general/archive/ and posts/ is empty. The archive is correctly not
// synced, so the laptop has no bulletin/ directory at all. Doctor used to
// shorten the include bulletin/general/posts/ to bulletin/, find tracked files
// under it (all archived), find none on disk, and report the board as excluded
// by the sync list — then --fix would reapply the same patterns, change
// nothing, and blame the server. Both messages were wrong.
//
// Failure prevented: doctor blames the server sync list for a board that simply
// has no active posts.
func TestMissingSparseTopLevelDirs_ArchivedOnlyBoardIsNotMissing(t *testing.T) {
	cfg := manifest.FallbackConfigFor(manifest.RepoKindTeamContext)
	repo := seedBulletinTeamContext(t, map[string]string{
		"bulletin/general/archive/ab/old-" + fakePostSHA + ".md":        "# Old\n\nexpired post\n",
		"bulletin/general/archive/ab/old-" + fakePostSHA + ".meta.json": `{"expires_at":"2026-09-01T00:00:00Z"}`,
		"README.md": "root\n",
	}, cfg)

	// Fixture sanity: the archive must not have materialized, so bulletin/ is
	// entirely absent from the working tree — the exact state that misfired.
	if _, err := os.Stat(filepath.Join(repo, "bulletin")); !os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: bulletin/ should be absent from the working tree (err=%v)", err)
	}

	missing := missingSparseTopLevelDirs(repo, cfg)
	if joined := strings.Join(missing, ","); strings.Contains(joined, "bulletin") {
		t.Errorf("a board with only archived posts was reported missing; the archive is never synced. got %v", missing)
	}
}

// TestCheckTeamSparseCheckout_ArchivedOnlyBoardPasses drives the doctor check
// itself, not just the helper: the customer promise is that `ox doctor` passes
// on a board with no active posts, in both report and fix mode.
//
// Failure prevented: a permanent red doctor line on every team whose board has
// gone quiet, teaching coworkers to ignore the one check that catches real
// content loss.
func TestCheckTeamSparseCheckout_ArchivedOnlyBoardPasses(t *testing.T) {
	cfg := manifest.FallbackConfigFor(manifest.RepoKindTeamContext)
	teamPath := seedBulletinTeamContext(t, map[string]string{
		".sageox/sync.manifest": "version 1\ninclude .sageox/\ninclude agents/\ninclude memory/\ninclude bulletin/general/posts/\n",
		"agents/rules/team.md":  "team rule\n",
		"memory/MEMORY.md":      "memory\n",
		"bulletin/general/archive/ab/old-" + fakePostSHA + ".md":        "# Old\n\nexpired post\n",
		"bulletin/general/archive/ab/old-" + fakePostSHA + ".meta.json": `{"expires_at":"2026-09-01T00:00:00Z"}`,
		"README.md": "root\n",
	}, cfg)
	if _, err := os.Stat(filepath.Join(teamPath, "bulletin")); !os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: bulletin/ should be absent from the working tree (err=%v)", err)
	}

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()
	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()
	requireSageoxDir(t, gitRoot)
	if err := config.SaveLocalConfig(gitRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{TeamID: "team-bulletin", TeamName: "Engineering", Path: teamPath}},
	}); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}

	if result := checkTeamSparseCheckout(false); !result.passed || result.warning {
		t.Errorf("report mode must pass on an archived-only board: passed=%v warning=%v message=%q detail=%q",
			result.passed, result.warning, result.message, result.detail)
	}
	if result := checkTeamSparseCheckout(true); !result.passed || result.warning {
		t.Errorf("fix mode must pass on an archived-only board: passed=%v warning=%v message=%q detail=%q",
			result.passed, result.warning, result.message, result.detail)
	}
	if _, err := os.Stat(filepath.Join(teamPath, "bulletin")); !os.IsNotExist(err) {
		t.Error("the archive must stay off disk; doctor must not have widened the sparse set")
	}
}

// TestMissingSparseTopLevelDirs_OmittedPostsIncludeIsStillReported is the
// control for the regression above, so the fix does not fail open.
//
// HEAD holds a live post AND archived content. When the sparse spec omits the
// posts include, the live post never materializes and doctor must say so; once
// the posts include is applied and the post is on disk, nothing is reported.
//
// Failure prevented: a detector that stops reporting bulletin/ altogether — the
// archived-only test above would pass while a real omission of active posts
// went silent, which is #862 with a new directory name.
func TestMissingSparseTopLevelDirs_OmittedPostsIncludeIsStillReported(t *testing.T) {
	withPosts := manifest.FallbackConfigFor(manifest.RepoKindTeamContext)
	withoutPosts := &manifest.ManifestConfig{Includes: []string{".sageox/", "agents/", "memory/"}}

	live := "bulletin/general/posts/live-" + fakePostSHA + ".md"
	repo := seedBulletinTeamContext(t, map[string]string{
		live: "# Live\n\nactive post\n",
		"bulletin/general/posts/live-" + fakePostSHA + ".meta.json":     `{"expires_at":"2026-10-05T22:41:07Z"}`,
		"bulletin/general/archive/ab/old-" + fakePostSHA + ".md":        "# Old\n\nexpired post\n",
		"bulletin/general/archive/ab/old-" + fakePostSHA + ".meta.json": `{"expires_at":"2026-09-01T00:00:00Z"}`,
		"agents/rules/team.md": "team rule\n",
		"memory/MEMORY.md":     "memory\n",
		"README.md":            "root\n",
	}, withoutPosts)

	// The sparse spec omits the posts include: the live post is absent.
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(live))); !os.IsNotExist(err) {
		t.Fatalf("fixture is wrong: the live post should be absent before the posts include is applied (err=%v)", err)
	}
	// Doctor evaluates against the expected shape (fallback + manifest), which
	// includes bulletin/general/posts/ — so the missing live post is a finding.
	missing := missingSparseTopLevelDirs(repo, withPosts)
	if !strings.Contains(strings.Join(missing, ","), "bulletin/") {
		t.Errorf("an unmaterialized active post was not detected; the fix must not fail open. got %v", missing)
	}
	if strings.Contains(strings.Join(missing, ","), "agents/") || strings.Contains(strings.Join(missing, ","), "memory/") {
		t.Errorf("materialized directories were reported missing: %v", missing)
	}

	// Apply the posts include: the live post lands, the archive stays out, and
	// the finding clears.
	applySparseSet(t, repo, manifest.SparseSetFor(withPosts, manifest.RepoKindTeamContext))
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(live))); err != nil {
		t.Fatalf("the live post must materialize once the posts include is applied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "bulletin", "general", "archive")); !os.IsNotExist(err) {
		t.Fatalf("the archive must not materialize under the posts include (err=%v)", err)
	}
	if missing := missingSparseTopLevelDirs(repo, withPosts); len(missing) != 0 {
		t.Errorf("a board with its active posts on disk reported missing directories: %v", missing)
	}
}
