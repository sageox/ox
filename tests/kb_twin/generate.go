//go:build kb_twin

package kb_twin

// Twin generation: real bare git repos backing each BubbleSpec. We
// never mock git; the value of the twin is that it's E2E. Each
// scenario's bubbles get their own bare repo created on demand, and
// the BubbleSpec.RepoURL is rewritten to a file:// pointer so the
// daemon's cloneBubble path works without network.
//
// No git-lfs shell-outs (per .claude/rules/lfs-no-git-lfs-binary.md):
// kb bubbles are tested as plain git repos. LFS pointer / hydration
// behavior has its own coverage in internal/lfs.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
)

// scenarioRepoDir is the parent under which every bubble's bare repo
// lives for one scenario run. We pass it in from the runner (which
// owns the t.TempDir) so cleanup happens automatically when the
// subtest exits.
type scenarioRepoDir struct {
	root string
}

// makeBareRepoForBubble creates a bare git repo seeded with the
// bubble's SeedFile content and returns the absolute path. The path
// is suitable for use as `file://<path>` in BubbleSpec.RepoURL.
//
// Mirrors the helper in internal/daemon/sync_bubbles_test.go so a
// reader can switch between the two and see the same shape.
func (d *scenarioRepoDir) makeBareRepoForBubble(b BubbleSpec) (string, error) {
	bareDir := filepath.Join(d.root, b.KBID+".bare")
	workDir := filepath.Join(d.root, b.KBID+".work")

	if err := runGit(d.root, "init", "--bare", "-b", "main", bareDir); err != nil {
		return "", fmt.Errorf("git init bare for %s: %w", b.KBID, err)
	}

	if err := runGit(d.root, "clone", bareDir, workDir); err != nil {
		return "", fmt.Errorf("git clone seed for %s: %w", b.KBID, err)
	}

	if err := configGitIdentity(workDir); err != nil {
		return "", fmt.Errorf("git config for %s: %w", b.KBID, err)
	}

	if b.SeedFile != "" {
		seedPath := filepath.Join(workDir, b.SeedFile)
		if err := os.MkdirAll(filepath.Dir(seedPath), 0o755); err != nil {
			return "", fmt.Errorf("create parent dirs for %s: %w", b.KBID, err)
		}
		if err := os.WriteFile(seedPath, []byte(b.SeedContent), 0o644); err != nil {
			return "", fmt.Errorf("write seed file for %s: %w", b.KBID, err)
		}
		if err := runGit(workDir, "add", b.SeedFile); err != nil {
			return "", fmt.Errorf("git add for %s: %w", b.KBID, err)
		}
		if err := runGit(workDir, "commit", "-m", "initial seed"); err != nil {
			return "", fmt.Errorf("git commit for %s: %w", b.KBID, err)
		}
		if err := runGit(workDir, "push", "origin", "HEAD:main"); err != nil {
			return "", fmt.Errorf("git push for %s: %w", b.KBID, err)
		}
	}

	return bareDir, nil
}

// pushExtraCommit pushes a follow-up commit to an existing bare repo
// via a fresh throwaway clone. Used by Phase.PushCommits to drive the
// "incremental pull" scenario.
func (d *scenarioRepoDir) pushExtraCommit(bareDir, fileName, content string) error {
	tmpClone := filepath.Join(d.root, "pusher-"+filepath.Base(bareDir))
	// best-effort cleanup if a previous push left the dir behind.
	_ = os.RemoveAll(tmpClone)
	if err := runGit(d.root, "clone", bareDir, tmpClone); err != nil {
		return fmt.Errorf("clone for push: %w", err)
	}
	if err := configGitIdentity(tmpClone); err != nil {
		return err
	}
	dst := filepath.Join(tmpClone, fileName)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create parent dirs for follow-up file: %w", err)
	}
	if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write follow-up file: %w", err)
	}
	if err := runGit(tmpClone, "add", fileName); err != nil {
		return err
	}
	if err := runGit(tmpClone, "commit", "-m", "follow-up commit"); err != nil {
		return err
	}
	return runGit(tmpClone, "push", "origin", "HEAD:main")
}

// configGitIdentity sets a deterministic identity inside a clone so
// commits don't blow up under test. NEVER touches global config (per
// .claude/rules/testing.md "Git Identity").
func configGitIdentity(dir string) error {
	if err := runGit(dir, "config", "user.email", "kb-twin@test.invalid"); err != nil {
		return err
	}
	return runGit(dir, "config", "user.name", "KB Twin")
}

// runGit invokes git in cwd. Wraps exec.Command so test failures
// surface the combined output (git's error messages are notoriously
// terse without it).
func runGit(cwd string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = isolatedGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("git %v timed out: %w", args, ctx.Err())
		}
		return fmt.Errorf("git %v failed: %s: %w", args, string(out), err)
	}
	return nil
}

// isolatedGitEnv prevents a coworker's signing, hooks, credentials, or prompt
// settings from changing deterministic twin behavior. Keep PATH and other
// ordinary process state, but replace Git's configuration trust boundaries.
func isolatedGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (name == "GIT_CONFIG" || strings.HasPrefix(name, "GIT_CONFIG_") || name == "GIT_TERMINAL_PROMPT") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
}

// resolveRepoURLs walks a scenario's bubbles, materializes a bare
// repo for each (unless BadRepoURL or !ProvideRepoURL), and returns
// a copy of the bubble list with RepoURL filled in. This is the
// glue that connects the scenario's declarative spec to a real
// filesystem.
// twinTeamID is the primary project's team: the one scope the scheduler
// lists and the scope every generated bubble row records. Declared here, in a
// non-test file, because generate.go is compiled without the test files when
// the coverage run instruments this package.
const twinTeamID = "team_twin"

func resolveRepoURLs(d *scenarioRepoDir, bubbles []BubbleSpec) ([]api.KB, error) {
	out := make([]api.KB, 0, len(bubbles))
	for _, b := range bubbles {
		row := api.KB{
			KBID:        b.KBID,
			KBType:      b.KBType,
			Slug:        b.Slug,
			OwnerUserID: b.OwnerUserID,
			ViewerRole:  b.ViewerRole,
			RepoID:      b.RepoID,
			// The scoped list API returns the scope it was asked for on
			// every row; the scheduler lists the primary project's team.
			ScopeType: api.KBScopeTypeTeam,
			ScopeID:   twinTeamID,
		}
		switch {
		case b.BadRepoURL:
			row.RepoURL = "file:///nonexistent/kb/twin/path/that/cannot/be/cloned.git"
		case !b.ProvideRepoURL:
			// leave RepoURL empty — simulates a bubble the server
			// hasn't provisioned yet.
		default:
			bare, err := d.makeBareRepoForBubble(b)
			if err != nil {
				return nil, err
			}
			row.RepoURL = "file://" + bare
		}
		out = append(out, row)
	}
	return out, nil
}
