package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/plan"
)

// Unsaved-ARTIFACT nudge — the capture gap the unsaved-PLAN nudge cannot see.
//
// The plan nudge (agent_hook_unsaved_plan_nudge.go) is armed from ONE place:
// `ox plan enrich`. That makes it precise for the drafting flow it was built
// for, and blind to everything else. An agent that authors a self-contained
// HTML page WITHOUT ever running enrich — because it built the page after the
// work rather than before it, and so never thought of it as "a plan" — arms
// nothing, and the page dies in the working tree when the session ends. That is
// exactly what happened: a 168 KB device-capture review sheet sat in .context/
// until a human asked where it was.
//
// So this signal comes from the ARTIFACT, not from having run a command. If a
// self-contained page was authored under the project and no saved plan claims
// it, ox can say so — no transcript scraping, no heuristics about intent.
//
// Delivery is the same UserPromptSubmit channel the plan nudge uses, and for
// the same reason: it is the only hook whose stdout reaches the model. It must
// NOT depend on `ox session stop` — most people never run it; they close the
// terminal and let anti-entropy pick the session up later.
//
// Everything is best-effort and fail-open: any error yields no nudge, never a
// failed command.

const (
	// artifactNudgeCacheSubdir records which artifact paths have already been
	// mentioned, so a page that the human deliberately leaves unsaved does not
	// nag on every prompt.
	artifactNudgeCacheSubdir = "artifact-nudged"

	// artifactMinBytes skips fragments and test scaffolding. An authored,
	// self-contained page with inline CSS carries real weight; the review sheet
	// that motivated this was ~168 KB.
	artifactMinBytes = 20 * 1024

	// artifactMaxAge bounds the scan to work from roughly this session. An
	// artifact authored last month is not news.
	artifactMaxAge = 12 * time.Hour

	// artifactScanMaxDepth keeps the walk cheap on a large repo.
	artifactScanMaxDepth = 6
)

// artifactSkipDirs are never walked: build output and dependency trees hold
// generated HTML that nobody authored.
var artifactSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, ".next": true, "coverage": true,
	".venv": true, "venv": true, "__pycache__": true, ".pio": true,
}

// looksAuthoredPage reports whether the head of a file reads as a
// self-contained authored page rather than a generated fragment or a report.
// Deliberately narrow: a real page declares itself HTML and carries its own
// styling or inlined images, which is what "self-contained" means in the
// authoring contract.
func looksAuthoredPage(head []byte) bool {
	h := strings.ToLower(string(head))
	if !strings.Contains(h, "<!doctype html") && !strings.Contains(h, "<html") {
		return false
	}
	return strings.Contains(h, "<style") || strings.Contains(h, "data:image/") ||
		strings.Contains(h, "data-ox-section")
}

// savedSourcePaths returns the absolute source paths every saved plan already
// claims, so an artifact that IS in the ledger never nudges.
func savedSourcePaths(gitRoot string) map[string]bool {
	out := map[string]bool{}
	infos, err := plan.List(gitRoot)
	if err != nil {
		return out
	}
	for _, i := range infos {
		m, err := plan.LoadMeta(i.Dir)
		if err != nil || m.SourcePlanPath == "" {
			continue
		}
		out[m.SourcePlanPath] = true
		// A page is often copied to a temp path before saving, so also match
		// on basename: the ledger records the copy, the working tree holds the
		// original, and they are the same artifact.
		out["base:"+filepath.Base(m.SourcePlanPath)] = true
	}
	return out
}

// findUnsavedArtifacts walks the project for recently-authored self-contained
// pages that no saved plan claims. Bounded in depth, size and age so it stays
// cheap enough to run on a prompt hook.
func findUnsavedArtifacts(projectRoot string, now time.Time) []string {
	if projectRoot == "" {
		return nil
	}
	claimed := savedSourcePaths(projectRoot)
	rootDepth := strings.Count(filepath.Clean(projectRoot), string(os.PathSeparator))

	var found []string
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, never fail the hook
		}
		if d.IsDir() {
			if artifactSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if strings.Count(path, string(os.PathSeparator))-rootDepth > artifactScanMaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".html") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() < artifactMinBytes {
			return nil
		}
		if now.Sub(info.ModTime()) > artifactMaxAge {
			return nil
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil
		}
		if claimed[abs] || claimed["base:"+filepath.Base(abs)] {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		head := make([]byte, 2048)
		n, _ := f.Read(head)
		_ = f.Close()
		if !looksAuthoredPage(head[:n]) {
			return nil
		}
		found = append(found, abs)
		if len(found) >= 3 {
			return filepath.SkipAll // one nudge names a few, never a wall
		}
		return nil
	})
	return found
}

// artifactNudgedPath is the marker recording that this artifact was mentioned.
func artifactNudgedPath(projectRoot, agentID, artifact string) string {
	base := planUnsavedPath(projectRoot, agentID)
	if base == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(filepath.Dir(base)), artifactNudgeCacheSubdir)
	// The artifact path is attacker-influenced; hash-free but flattened so it
	// can never escape the cache dir.
	safe := strings.NewReplacer(string(os.PathSeparator), "_", ":", "_", "..", "_").Replace(artifact)
	if len(safe) > 180 {
		safe = safe[len(safe)-180:]
	}
	return filepath.Join(dir, safe+".json")
}

// emitUnsavedArtifactNudge tells the model about a self-contained page it
// authored and never saved. At most once per artifact path.
func emitUnsavedArtifactNudge(w io.Writer, projectRoot, agentID string) {
	if projectRoot == "" {
		return
	}
	for _, art := range findUnsavedArtifacts(projectRoot, time.Now()) {
		marker := artifactNudgedPath(projectRoot, agentID, art)
		if marker == "" {
			continue
		}
		if _, err := os.Stat(marker); err == nil {
			continue // already mentioned
		}
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			continue
		}
		// Mark BEFORE speaking: an unheard reminder beats one that repeats on
		// every prompt.
		if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
			slog.Debug("hook: could not mark artifact nudge", "err", err)
			continue
		}
		fmt.Fprintf(w, "<system-reminder>[ox] %s</system-reminder>\n", unsavedArtifactNudgeLine(art))
		return // one artifact per prompt
	}
}

// unsavedArtifactNudgeLine names the artifact, the command, and what is lost
// otherwise. The path is attacker-influenced and crosses into trusted model
// context, so it is sanitized exactly like the plan nudge's target.
func unsavedArtifactNudgeLine(artifact string) string {
	return fmt.Sprintf(
		"You authored a self-contained page (%s) that is not in the ledger. Save it — `ox plan save --file %s --kind mockup|review|evidence` — or it dies with this session's working tree; a mockup or review sheet belongs in the ledger exactly as much as a plan does.",
		reminderSafePlanTarget(artifact), reminderSafePlanTarget(artifact),
	)
}
