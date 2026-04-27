package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/lfs"
)

// SessionManifestCheck verifies that every file recorded in each session's
// meta.json Files manifest actually exists on disk in the ledger. The
// manifest is the canonical contract between ox and the backend / other
// readers — a missing file means the session is in a broken state that
// will surface later as a 404 when something tries to fetch it.
//
// Behavior by storage type:
//
//   - Storage=lfs: file must exist on disk as either a real LFS pointer
//     file (~130 bytes, parseable by ParsePointer) OR as full content (a
//     coworker's locally-recorded session before pointer rewrite). A
//     completely missing file is the failure case we flag.
//   - Storage=git: file must exist as a regular git-tracked blob (not a
//     pointer file). summary.json is the prototypical case: if meta.Files
//     records it but the file is gone, the summary push step never
//     completed for this session.
//
// FixLevel is CheckOnly. The repair action depends on the artifact:
// summary.json gaps could be filled by re-invoking the summarizer (see
// ADR-016), but that's a separate, opt-in repair pathway. This check's
// job is to *find* the gaps and report them clearly.
//
// See bd ox-9mrk for the manifest refactor that motivates this check.
type SessionManifestCheck struct {
	gitRoot string
}

// NewSessionManifestCheck constructs a SessionManifestCheck.
func NewSessionManifestCheck(gitRoot string) *SessionManifestCheck {
	return &SessionManifestCheck{gitRoot: gitRoot}
}

// Name returns the user-facing check name.
func (c *SessionManifestCheck) Name() string { return "session manifest integrity" }

// Category returns the doctor category.
func (c *SessionManifestCheck) Category() string { return "Sessions" }

// ManifestGap describes a single inconsistency between meta.Files and the
// on-disk artifact tree for one session.
type ManifestGap struct {
	SessionName string
	Filename    string
	Storage     string // "lfs" | "git"
	Reason      string // short, human-readable
}

// Run scans every session in the ledger, comparing meta.Files entries
// against on-disk presence. CheckOnly: never modifies the ledger.
func (c *SessionManifestCheck) Run(_ context.Context, _ bool) CheckResult {
	ledgerPath := ledgerPathFromProject(c.gitRoot)
	if ledgerPath == "" {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusSkip,
			Message: "no ledger configured",
		}
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	gaps, scanned, err := findManifestGapsInLedger(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			// no sessions dir yet is a perfectly valid state
			return CheckResult{
				Name:    c.Name(),
				Status:  StatusPass,
				Message: "no sessions yet",
			}
		}
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: fmt.Sprintf("read sessions dir: %v", err),
		}
	}

	if len(gaps) == 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: fmt.Sprintf("%d sessions, manifests consistent", scanned),
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: fmt.Sprintf("%d manifest gaps across %d sessions", len(gaps), scanned),
		Fix:     formatManifestGaps(gaps),
	}
}

// findManifestGapsInLedger walks every session subdir under sessionsDir,
// reads its meta.json, and aggregates per-session gaps. Returns the gap
// list, the count of sessions scanned (those with a readable meta.json),
// and any error reading sessionsDir itself. Sessions without meta.json
// are skipped silently — that case is handled by other doctor checks.
//
// Extracted from Run() so multi-session E2E tests can exercise the ledger
// walk and aggregation without standing up a ProjectConfig + endpoint.
func findManifestGapsInLedger(sessionsDir string) ([]ManifestGap, int, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, 0, err
	}

	var gaps []ManifestGap
	scanned := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionDir := filepath.Join(sessionsDir, e.Name())
		meta, err := lfs.ReadSessionMeta(sessionDir)
		if err != nil {
			// missing meta.json is handled by other doctor checks; not our job
			continue
		}
		scanned++
		gaps = append(gaps, manifestGapsForSession(sessionDir, e.Name(), meta)...)
	}
	return gaps, scanned, nil
}

// manifestGapsForSession returns the per-file gaps for one session. Pure
// function over (sessionDir, meta); easy to unit-test.
func manifestGapsForSession(sessionDir, sessionName string, meta *lfs.SessionMeta) []ManifestGap {
	if meta == nil || len(meta.Files) == 0 {
		return nil
	}
	var gaps []ManifestGap
	for filename, ref := range meta.Files {
		path := filepath.Join(sessionDir, filename)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			gaps = append(gaps, ManifestGap{
				SessionName: sessionName,
				Filename:    filename,
				Storage:     ref.EffectiveStorage(),
				Reason:      "file missing",
			})
			continue
		}
		if err != nil {
			gaps = append(gaps, ManifestGap{
				SessionName: sessionName,
				Filename:    filename,
				Storage:     ref.EffectiveStorage(),
				Reason:      fmt.Sprintf("stat error: %v", err),
			})
			continue
		}
		if ref.IsGit() && lfs.IsPointerFile(path) {
			// a Storage=git artifact has been rewritten as an LFS pointer
			// (likely by a buggy upload path that didn't honor Storage tags)
			gaps = append(gaps, ManifestGap{
				SessionName: sessionName,
				Filename:    filename,
				Storage:     ref.EffectiveStorage(),
				Reason:      "expected git blob, found LFS pointer",
			})
			continue
		}
		if ref.IsGit() && info.Size() == 0 {
			gaps = append(gaps, ManifestGap{
				SessionName: sessionName,
				Filename:    filename,
				Storage:     ref.EffectiveStorage(),
				Reason:      "file empty",
			})
			continue
		}
	}
	return gaps
}

// formatManifestGaps returns a short, scannable summary of the first few
// gaps with a hint about how to investigate further. Doctor's CLI renderer
// truncates long Fix strings; keep this terse.
func formatManifestGaps(gaps []ManifestGap) string {
	if len(gaps) == 0 {
		return ""
	}
	var b strings.Builder
	limit := 5
	if len(gaps) < limit {
		limit = len(gaps)
	}
	b.WriteString("examples: ")
	for i := 0; i < limit; i++ {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s/%s (%s: %s)",
			gaps[i].SessionName, gaps[i].Filename, gaps[i].Storage, gaps[i].Reason)
	}
	if len(gaps) > limit {
		fmt.Fprintf(&b, "; +%d more", len(gaps)-limit)
	}
	b.WriteString(". Run 'ox session view <name>' to inspect; rerun summarization to repair missing summary.json.")
	return b.String()
}
