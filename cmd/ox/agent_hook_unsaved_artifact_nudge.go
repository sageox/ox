package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
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

	// artifactMinBytes skips fragments and test scaffolding. 8 KB, down from
	// 20 KB: a compact CSS-only mockup or review sheet authored in one turn
	// lands at 8–15 KB and was never mentioned. Lowering it is safe only
	// because candidates must now also be written by this session (see
	// filterSessionWritten) — the size gate is no longer the main noise filter.
	artifactMinBytes = 8 * 1024

	// artifactMaxAge bounds the scan to work from roughly this session. An
	// artifact authored last month is not news.
	artifactMaxAge = 12 * time.Hour

	// artifactScanMaxDepth keeps the walk cheap on a large repo.
	artifactScanMaxDepth = 6

	// artifactMaxNudged caps how many artifacts one scan reports: a nudge
	// names a few, never a wall.
	artifactMaxNudged = 3

	// artifactCandidateCap bounds the metadata-only candidate list the walk
	// collects. Head-reading happens after the walk returns, so the walk
	// cannot know which candidates will survive looksAuthoredPage — this cap
	// is what keeps the work bounded on a repo full of large generated HTML
	// that the size/age gates alone do not exclude.
	artifactCandidateCap = 32

	// artifactHeadBytes is how much of a candidate is examined to decide
	// whether it reads as an authored page.
	artifactHeadBytes = 2048
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

// normalizeArtifactPath is the single normalization BOTH sides of the claimed
// check go through — the ledger's stored source path and the path the walk
// found — so the two are compared in one spelling and never as raw strings.
// An unresolvable path yields an error and is dropped rather than compared raw.
func normalizeArtifactPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// savedSourcePaths returns the normalized absolute source paths every saved
// plan already claims, so an artifact that IS in the ledger never nudges.
//
// Artifact identity is the source path and nothing else. Also matching on
// basename looked like a cheap way to recognize a page copied to a temp path
// before being saved, but the authoring contract tells everyone to name the
// file plan.html — so ONE saved plan silenced every future page in the
// project, and `docs/review.html` claimed an unrelated `scratch/review.html`.
// The accepted cost of dropping it: a page saved from a temp copy will nudge
// once about the working-tree original. That is the right trade — a stale
// reminder is recoverable, a permanently silenced one is not. If copies ever
// need to be equivalent, that calls for an explicit stored identity for the
// copy relationship, not a basename guess.
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
		// A RELATIVE stored path is unprovable, so it claims nothing. Save
		// records an absolute path (absSourcePlanPath), but a legacy entry may
		// hold a relative one — and the working directory it was relative TO
		// was never persisted. Resolving it against whatever cwd happens to be
		// current would be a guess, and the failure it buys is the bad one: a
		// guess that lands on an unrelated file silences that file forever. An
		// unclaimed legacy artifact nudges once instead, which is recoverable.
		if !filepath.IsAbs(m.SourcePlanPath) {
			continue
		}
		abs, err := normalizeArtifactPath(m.SourcePlanPath)
		if err != nil {
			continue
		}
		out[abs] = true
	}
	return out
}

// findUnsavedArtifacts walks the project for recently-authored self-contained
// pages that no saved plan claims. Bounded in depth, size and age so it stays
// cheap enough to run on a prompt hook.
//
// The walk collects candidates from directory metadata ONLY; their heads are
// read after it returns. Opening a file from inside a WalkDir callback races
// the walk's own view of the tree — a directory component can be swapped for a
// symlink between the walk's stat and the open — so the read happens once the
// tree is no longer being enumerated.
//
// since, when non-zero, is when this session began (the prime marker's
// PrimedAt): a page last modified before it was not written by this session.
func findUnsavedArtifacts(projectRoot string, now, since time.Time) []string {
	if projectRoot == "" {
		return nil
	}
	claimed := savedSourcePaths(projectRoot)
	ledger := artifactLedgerRoot(projectRoot)
	rootDepth := strings.Count(filepath.Clean(projectRoot), string(os.PathSeparator))

	var candidates []string
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
		if !since.IsZero() && info.ModTime().Before(since) {
			return nil
		}
		abs, err := normalizeArtifactPath(path)
		if err != nil || claimed[abs] || isUnderDir(abs, ledger) {
			return nil
		}
		candidates = append(candidates, abs)
		if len(candidates) >= artifactCandidateCap {
			return filepath.SkipAll
		}
		return nil
	})

	candidates = filterSessionWritten(projectRoot, candidates)
	var found []string
	for _, abs := range candidates {
		if !looksAuthoredPage(artifactHead(abs)) {
			continue
		}
		found = append(found, abs)
		if len(found) >= artifactMaxNudged {
			break
		}
	}
	return found
}

// filterSessionWritten drops candidates git says are tracked AND unmodified.
// Those are pages that were merely present in the tree — a checkout or a new
// worktree stamps every tracked file with a fresh mtime, which is how a
// committed agents/buzz/index.html was reported as "a page you authored".
// Untracked, ignored (.context/ scratch pages), staged, and modified files are
// kept: something wrote them since the last commit.
//
// If git cannot answer (not a repo, git missing) every candidate is kept —
// "could not look" must not silently become "nothing to report".
func filterSessionWritten(projectRoot string, candidates []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	prefixOut, err := exec.Command("git", "-C", projectRoot, "rev-parse", "--show-prefix").Output()
	if err != nil {
		slog.Debug("hook: artifact nudge could not ask git, keeping all candidates", "err", err)
		return candidates
	}
	prefix := strings.TrimSpace(string(prefixOut))
	args := []string{"--literal-pathspecs", "-C", projectRoot, "status", "--porcelain=v1", "-z",
		// traditional + untracked=all lists an ignored FILE by name; "matching"
		// collapses it to its ignored parent dir (".context/"), which the
		// prefix match below also handles.
		"--untracked-files=all", "--ignored=traditional", "--"}
	rels := make(map[string]string, len(candidates)) // repo-relative -> abs
	for _, abs := range candidates {
		rel, rerr := filepath.Rel(projectRoot, abs)
		if rerr != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		rels[prefix+rel] = abs
		args = append(args, rel)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		slog.Debug("hook: artifact nudge git status failed, keeping all candidates", "err", err)
		return candidates
	}
	touched := map[string]bool{}
	for _, rec := range strings.Split(string(out), "\x00") {
		if len(rec) < 4 {
			continue // empty tail, or the source half of a rename record
		}
		p := rec[3:]
		if abs, ok := rels[p]; ok {
			touched[abs] = true
			continue
		}
		if strings.HasSuffix(p, "/") { // a whole untracked/ignored directory
			for rel, abs := range rels {
				if strings.HasPrefix(rel, p) {
					touched[abs] = true
				}
			}
		}
	}
	var kept []string
	for _, abs := range candidates {
		if touched[abs] {
			kept = append(kept, abs)
		}
	}
	slog.Debug("hook: artifact nudge session-written filter", "before", len(candidates), "after", len(kept))
	return kept
}

// artifactLedgerRoot is the project's ledger checkout, or "". Pages inside the
// ledger are saved plans by definition; nudging to save one is always wrong.
func artifactLedgerRoot(projectRoot string) string {
	ctx, err := config.LoadProjectContext(projectRoot)
	if err != nil || ctx == nil {
		return ""
	}
	p := ctx.DefaultLedgerPath()
	if p == "" {
		return ""
	}
	abs, err := normalizeArtifactPath(p)
	if err != nil {
		return ""
	}
	return abs
}

// isUnderDir reports whether path is dir or inside it. Both must already be
// normalized absolute paths; an empty dir contains nothing.
func isUnderDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// artifactHead reads the opening bytes of a candidate. Called only after the
// walk has returned; an unreadable file yields no bytes, which reads as
// not-an-authored-page.
func artifactHead(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	head := make([]byte, artifactHeadBytes)
	n, _ := f.Read(head)
	_ = f.Close()
	return head[:n]
}

// artifactNudgedPath is the marker recording that this artifact was mentioned.
//
// The filename is a hash of the absolute artifact path, not a rewrite of the
// path itself. Flattening separators made distinct artifacts collide —
// /repo/a/b.html and /repo/a_b.html produced the same marker, so the second
// page was silenced by the first's reminder and never nudged at all. Hex also
// preserves the flatten's actual purpose for free: it can hold neither a path
// separator nor "..", so the marker can never escape the cache dir.
func artifactNudgedPath(projectRoot, agentID, artifact string) string {
	base := planUnsavedPath(projectRoot, agentID)
	if base == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(filepath.Dir(base)), artifactNudgeCacheSubdir)
	sum := sha256.Sum256([]byte(artifact))
	return filepath.Join(dir, hex.EncodeToString(sum[:])[:32]+".json")
}

// emitUnsavedArtifactNudge tells the model about a self-contained page it
// authored and never saved. At most once per artifact path.
// since is the session start (zero when unknown); see findUnsavedArtifacts.
func emitUnsavedArtifactNudge(w io.Writer, projectRoot, agentID string, since time.Time) {
	if projectRoot == "" {
		return
	}
	for _, art := range findUnsavedArtifacts(projectRoot, time.Now(), since) {
		if !markArtifactNudged(projectRoot, agentID, art) {
			continue
		}
		fmt.Fprintf(w, "<system-reminder>[ox] %s</system-reminder>\n", unsavedArtifactNudgeLine(art))
		return // one artifact per prompt
	}
}

// markArtifactNudged records that art has been mentioned and reports whether
// the caller should speak now (false when already mentioned or unmarkable).
// Mark BEFORE speaking: an unheard reminder beats one that repeats forever.
func markArtifactNudged(projectRoot, agentID, art string) bool {
	marker := artifactNudgedPath(projectRoot, agentID, art)
	if marker == "" {
		return false
	}
	if _, err := os.Stat(marker); err == nil {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return false
	}
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		slog.Debug("hook: could not mark artifact nudge", "err", err)
		return false
	}
	return true
}

// writtenPageTools are the Claude Code tools whose tool_input.file_path names
// a file the model just wrote.
var writtenPageTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true}

// writtenPageFromToolInput returns the .html file a Write/Edit/MultiEdit call
// just wrote, or "" for any other tool or payload.
func writtenPageFromToolInput(toolName string, toolInput []byte) string {
	if !writtenPageTools[toolName] || len(toolInput) == 0 {
		return ""
	}
	var ti struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(toolInput, &ti); err != nil {
		return ""
	}
	if !strings.EqualFold(filepath.Ext(ti.FilePath), ".html") {
		return ""
	}
	return ti.FilePath
}

// emitWrittenPageNudge is the SAME-TURN page nudge. The prompt-hook nudge
// above fires on the human's NEXT prompt — after the agent has already told
// them "done" and often after the session ended. This fires on the
// PostToolUse of the Write itself, while the agent can still act on it.
//
// Plain PostToolUse stdout is discarded by Claude Code (agent_hook.go table),
// so this uses the one PostToolUse channel it does inject: the JSON
// hookSpecificOutput.additionalContext envelope. stdout must then be exactly
// that JSON document, so the caller must write nothing else on this call.
// Returns whether it emitted.
func emitWrittenPageNudge(w io.Writer, projectRoot, agentID, toolName string, toolInput []byte) bool {
	path := writtenPageFromToolInput(toolName, toolInput)
	if path == "" || projectRoot == "" {
		return false
	}
	abs, err := normalizeArtifactPath(path)
	if err != nil {
		return false
	}
	if isUnderDir(abs, artifactLedgerRoot(projectRoot)) || savedSourcePaths(projectRoot)[abs] {
		return false
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() || info.Size() < artifactMinBytes {
		return false
	}
	if !looksAuthoredPage(artifactHead(abs)) {
		return false
	}
	if !markArtifactNudged(projectRoot, agentID, abs) {
		return false
	}
	payload := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolUse",
			"additionalContext": "[ox] " + unsavedArtifactNudgeLine(abs),
		},
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Debug("hook: written-page nudge emit failed", "err", err)
		return false
	}
	slog.Info("hook: written-page nudge emitted", "agent_id", agentID, "bytes", info.Size())
	return true
}

// unsavedArtifactNudgeLine names the artifact, the command, and what is lost
// otherwise. The path is attacker-influenced and crosses into trusted model
// context, so it is sanitized exactly like the plan nudge's target.
func unsavedArtifactNudgeLine(artifact string) string {
	return fmt.Sprintf(
		"Saved nothing yet: `ox plan save --file %s --kind %s`. Teammates can't see it otherwise.",
		reminderSafePlanTarget(artifact), plan.KindsHint(),
	)
}
