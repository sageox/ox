package index

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrAlternatesUnsupported reports that a repo uses
// `.git/objects/info/alternates`, a layout go-git v6 does not honor. ox's
// codedb indexing path (plainOpenTolerant + Log walk) silently fails on
// such repos with bare "object not found" errors. The right caller
// behavior is to soft-skip: log + suggest `git repack -a -d --local` or
// re-clone without --shared/--reference, but do not treat as fatal.
//
// Pinned by TestPlainOpenTolerant_AlternatesUpstreamLimitation. When
// upstream go-git adds alternates support and that test starts failing,
// remove this gate.
var ErrAlternatesUnsupported = errors.New("git alternates not supported by go-git v6 (codedb indexing skipped)")

// hasAlternates reports whether repoPath has a non-empty
// `.git/objects/info/alternates`. Resolves via `git rev-parse
// --git-common-dir` so linked worktrees inherit the main repo's state.
func hasAlternates(repoPath string) bool {
	commonDir, err := resolveCommonDir(repoPath)
	if err != nil {
		// Fall back to the conservative direct check. If rev-parse fails
		// we'd rather miss the alternate (codedb fails with the usual
		// "object not found" message later) than block indexing on a
		// transient git error.
		alts := filepath.Join(repoPath, ".git", "objects", "info", "alternates")
		return nonEmptyFile(alts)
	}
	alts := filepath.Join(commonDir, "objects", "info", "alternates")
	return nonEmptyFile(alts)
}

func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

func resolveCommonDir(repoPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--git-common-dir")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repoPath, dir)
	}
	return dir, nil
}
