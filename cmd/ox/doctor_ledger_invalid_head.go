package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
)

// CheckSlugLedgerInvalidHead detects a ledger whose .git/HEAD points at a
// syntactically invalid ref (the ox-baz5.6 shape: refs/heads/.invalid) that
// git itself refuses to resolve.
//
// bd ox-baz5.6: an early-EOF during clone (a large ledger dropping
// mid-transfer) can leave a half-initialized .git with exactly this shape —
// HEAD unresolvable, an invalid ref file present, every tracked file staged
// as new. Every subsequent commit then fails ("cannot lock ref HEAD:
// reference already exists"), and ox doctor's OTHER ledger checks — branch
// status, stranded commits, clean workdir — all assume HEAD resolves, so
// they silently skip or fail on a cryptic git error instead of naming the
// real problem. This check names it, and --fix repairs it by re-cloning
// from origin while preserving any uncommitted sessions/ and data/ content
// per .claude/rules/daemon-git.md ("never discard uncommitted changes").
const CheckSlugLedgerInvalidHead = "ledger-invalid-head"

// invalidHeadCheckTimeout bounds the local git plumbing this check runs
// (rev-parse, check-ref-format, status) — all local, no network.
const invalidHeadCheckTimeout = 30 * time.Second

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugLedgerInvalidHead,
		Name:     "Ledger HEAD integrity",
		Category: "Ledger Git Health",
		// Confirm, never Auto: the repair moves the corrupted clone aside,
		// re-clones from origin, and restores staged content into the fresh
		// clone before committing it. That is real surgery on the user's only
		// local copy of whatever never made it to the ledger — a human should
		// see it happen, matching the stranded-commits check's own reasoning
		// for FixLevelConfirm.
		FixLevel:    FixLevelConfirm,
		Description: "Detects HEAD pointing at an unresolvable ref (e.g. refs/heads/.invalid) and repairs by re-cloning while preserving uncommitted content",
		Run:         func(fix bool) checkResult { return checkLedgerInvalidHead(fix) },
	})
}

// detectInvalidHead reports the ref .git/HEAD names and whether git itself
// refuses to resolve it because the ref NAME is syntactically invalid — the
// ox-baz5.6 shape specifically. Two other shapes must NOT be claimed here,
// because each already has its own doctor check with its own repair:
//   - a raw SHA (detached HEAD, not a symbolic ref) — not this shape at all.
//   - a valid branch name with zero commits (a genuine unborn branch) —
//     unbornLedgerFailure (doctor_ledger_git.go) already owns this; its repair
//     (fetch/checkout from remote, or fail closed on a genuinely empty remote)
//     would be actively wrong to skip in favor of a reclone here.
func detectInvalidHead(ctx context.Context, ledgerPath string) (invalidRef string, corrupted bool) {
	headBytes, err := os.ReadFile(filepath.Join(ledgerPath, ".git", "HEAD"))
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(headBytes))
	const prefix = "ref: "
	if !strings.HasPrefix(line, prefix) {
		return "", false // detached HEAD (raw SHA) — a different, unrelated shape
	}
	ref := strings.TrimPrefix(line, prefix)

	// resolves fine via git's own resolution — nothing wrong here, regardless
	// of what the raw HEAD file's target looks like syntactically.
	if exec.CommandContext(ctx, "git", "-C", ledgerPath, "rev-parse", "--verify", "-q", ref).Run() == nil {
		return "", false
	}

	// Doesn't resolve — but that alone is also true of a plain unborn branch
	// (valid name, zero commits), which unbornLedgerFailure already owns.
	// Only claim it here if the ref NAME ITSELF fails git's own validation.
	branchName := strings.TrimPrefix(ref, "refs/heads/")
	if exec.CommandContext(ctx, "git", "-C", ledgerPath, "check-ref-format", "--branch", branchName).Run() == nil {
		return "", false // valid name, just unborn — not this check's shape
	}
	return ref, true
}

func checkLedgerInvalidHead(fix bool) checkResult {
	return invalidHeadCheck(getLedgerPath(), fix)
}

// invalidHeadCheck is the testable core: it takes the ledger path as a
// parameter instead of reading it from ambient config, the same split
// checkLedgerStrandedCommits/strandedCommitsCheck uses and for the same
// reason — a test should be able to point this at a fixture directly.
func invalidHeadCheck(ledgerPath string, fix bool) checkResult {
	const name = "Ledger HEAD integrity"
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger found", "")
	}
	if !isGitRepo(ledgerPath) {
		return SkippedCheck(name, "ledger not a git repo", "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), invalidHeadCheckTimeout)
	defer cancel()

	invalidRef, corrupted := detectInvalidHead(ctx, ledgerPath)
	if !corrupted {
		return PassedCheck(name, "HEAD resolves")
	}

	staged := 0
	if out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "status", "--porcelain").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			staged = len(strings.Split(s, "\n"))
		}
	}

	if !fix {
		r := CriticalCheck(name,
			fmt.Sprintf("HEAD points at an unresolvable ref %q — every commit fails", invalidRef),
			fmt.Sprintf("%d file(s) likely staged but nothing can commit while HEAD is unresolvable.\n       "+
				"Run `ox doctor --fix-slug=%s` to repair by re-cloning from origin while preserving any "+
				"uncommitted sessions/ and data/ content.", staged, CheckSlugLedgerInvalidHead))
		r.fixLevel = FixLevelConfirm
		return r
	}

	return fixLedgerInvalidHead(ctx, ledgerPath, invalidRef)
}

// fixLedgerInvalidHead repairs an unresolvable HEAD: move the corrupted
// clone aside (never delete it), clone fresh from origin, restore any
// uncommitted sessions/ and data/ content from the backup, and commit it.
// The backup is left on disk — not cleaned up — so a failure at any step
// leaves the original recoverable exactly where it was.
func fixLedgerInvalidHead(ctx context.Context, ledgerPath, invalidRef string) checkResult {
	const name = "Ledger HEAD integrity"

	remoteURLOut, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return FailedCheck(name, "cannot determine remote URL to re-clone from",
			fmt.Sprintf("git remote get-url origin: %v", err))
	}
	remoteURL := strings.TrimSpace(string(remoteURLOut))

	gitRoot := findGitRoot()
	projectEndpoint := endpoint.GetForProject(gitRoot)

	backupPath := fmt.Sprintf("%s.corrupt-backup-%d", ledgerPath, time.Now().UnixNano())
	if err := os.Rename(ledgerPath, backupPath); err != nil {
		return FailedCheck(name, "could not move the corrupted clone aside", err.Error())
	}

	if err := gitserver.CloneFromURLWithEndpoint(ctx, remoteURL, ledgerPath, projectEndpoint, nil); err != nil {
		// non-destructive on failure: put the original back exactly where it was.
		_ = os.RemoveAll(ledgerPath)
		if restoreErr := os.Rename(backupPath, ledgerPath); restoreErr != nil {
			return FailedCheck(name, "reclone failed AND could not restore the original — nothing was lost, but manual recovery is needed",
				fmt.Sprintf("reclone error: %v; restore error: %v; the corrupted clone is intact at %s", err, restoreErr, backupPath))
		}
		return FailedCheck(name, "reclone failed; original left untouched", err.Error())
	}

	restoredDirs := 0
	for _, dir := range []string{"sessions", "data"} {
		src := filepath.Join(backupPath, dir)
		info, statErr := os.Stat(src)
		if statErr != nil || !info.IsDir() {
			continue
		}
		dst := filepath.Join(ledgerPath, dir)
		if err := copyLedgerDir(src, dst); err != nil {
			return FailedCheck(name,
				fmt.Sprintf("re-cloned, but failed to restore %s/ from the backup", dir),
				fmt.Sprintf("%v — the original content is untouched at %s", err, backupPath))
		}
		restoredDirs++
	}

	statusOut, _ := exec.CommandContext(ctx, "git", "-C", ledgerPath, "status", "--porcelain").Output()
	fileCount := 0
	if s := strings.TrimSpace(string(statusOut)); s != "" {
		fileCount = len(strings.Split(s, "\n"))
	}

	if restoredDirs == 0 || fileCount == 0 {
		return WarningCheck(name,
			fmt.Sprintf("re-cloned a healthy ledger (HEAD was %q); nothing to restore", invalidRef),
			fmt.Sprintf("The corrupted clone had no sessions/ or data/ content beyond what the fresh clone already "+
				"has. Backup kept at %s — delete it once you've confirmed nothing else needs recovering.", backupPath))
	}

	result := fixLedgerDirtyWorkdir(ledgerPath, fileCount)
	result.name = name
	if !result.passed {
		// surface the exact commit failure; the reclone itself already
		// succeeded and the backup is still there.
		result.detail = fmt.Sprintf("%s (backup at %s)", result.detail, backupPath)
		return result
	}

	return WarningCheck(name,
		fmt.Sprintf("repaired: re-cloned from origin, restored and committed %d file(s)", fileCount),
		fmt.Sprintf("The corrupted clone (HEAD -> %s) is preserved at %s — delete it once you've confirmed "+
			"nothing else needs recovering.", invalidRef, backupPath))
}

// copyLedgerDir recursively copies a directory tree from src to dst,
// preserving file modes. Used only by fixLedgerInvalidHead to restore
// sessions/ and data/ from a backed-up corrupted clone.
func copyLedgerDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return err
		}
		return copyFile(path, dstPath)
	})
}
