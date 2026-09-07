package main

import (
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/skillmanager"
)

// The scoped-ignore policy lives in skillmanager so every materialization path
// can reach it — cmd/ox, the daemon's autofix tick, and `ox agent prime` alike.
// Keeping it in cmd/ox left prime and the daemon writing reserved-prefix files
// with no rule to hide them, which is how a real repository ended up with nine
// untracked ox-cli-* skills one `git add -A` away from being committed.

type ignoreFileResult = skillmanager.IgnoreFileResult

func scopedIgnoreFiles() []skillmanager.ScopedIgnoreFile { return skillmanager.ScopedIgnoreFiles() }

func ensureScopedIgnoreFiles(repoRoot string) ([]ignoreFileResult, error) {
	return skillmanager.EnsureScopedIgnoreFiles(repoRoot)
}

// isReservedManagedPath reports whether an absolute path inside repoRoot is an
// ox-owned, gitignored artifact — a skill directory, rule, or command carrying a
// reserved prefix.
//
// It is the gate that stops `ox init` from staging its own files. Until 0.15.0
// init force-staged everything the adapters wrote, deliberately defeating
// .gitignore (gitAddFilesForce, "force-stage files that may be gitignored") —
// which is the single mechanism that put vendor files into customers' pull
// requests. Ignoring a path is useless while something still force-adds it.
//
// The committed on-ramp (sageox) is NOT reserved, so it keeps being staged: it is
// the one file that must reach teammates and fresh clones.
func isReservedManagedPath(repoRoot, absPath string) bool {
	rel, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 {
		return false
	}
	switch parts[0] + "/" + parts[1] {
	case ".claude/skills", ".agents/skills":
		// <agent>/skills/<name>/SKILL.md — ownership is decided by the directory.
		return skillmanager.IsReservedName(parts[2])
	case ".claude/rules", ".factory/rules", ".agents/rules", ".claude/commands":
		// A rule or command file: ownership is decided by the basename. A nested
		// legacy path (rules/sageox/foo.md) is deliberately NOT reserved — it is
		// retired by the adapter's legacy sweep, not hidden from git.
		if len(parts) != 3 {
			return false
		}
		return skillmanager.IsReservedName(parts[2])
	}
	return false
}

// stageableInstalledPaths filters adapter-written paths down to the ones that
// belong in git.
//
// Reserved ox-cli-* / sageox-team-* artifacts are gitignored working-tree state,
// materialized locally on every machine from the binary's own catalog. Staging
// them is exactly what put vendor files into customers' pull requests on every
// release, and an ignore rule is useless while something still force-adds the
// path. Everything else — hook settings, the committed sageox on-ramp, instruction
// files — still needs to reach the index.
func stageableInstalledPaths(repoRoot string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if isReservedManagedPath(repoRoot, p) {
			continue
		}
		out = append(out, p)
	}
	return out
}
