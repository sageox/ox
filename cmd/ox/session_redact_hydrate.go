package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/lfs"
)

// hydrateSummary describes the outcome of the pre-scan hydration phase.
//
// The credential scan reads file content. Dehydrated session files are
// 140-byte LFS pointer stubs that match no credential pattern, so a scan
// over an un-hydrated ledger is a credible-looking lie — that was the
// pre-fix behavior. The redact-history workflow now forces a hydration
// pass before scanning and surfaces this summary so the operator can see
// (a) how much had to be fetched and (b) whether any session was
// unreachable, which would make the resulting scan incomplete.
type hydrateSummary struct {
	Sessions      int // total sessions inspected
	AlreadyOK     int // sessions where every scannable file was already real content
	Hydrated      int // sessions where we fetched at least one file from LFS
	HydratedFiles int
	Failed        int // sessions where >=1 scannable file could not be hydrated
	FailedFiles   int
}

// hydrateAllSessionsForScan walks <ledger>/sessions/<name>/ and ensures
// every scannable file (per ledgerSecretsScanExts) has hydrated content
// reachable via openSessionContent. Hydration writes to the cache only
// per .claude/rules/cache-only-design.md; in-place pointer files stay
// untouched. Progress is printed to `out` so the operator can see what's
// happening on a real ledger (115 dehydrated sessions on a fresh clone
// is not unusual and the fetch is not instant).
func hydrateAllSessionsForScan(projectRoot, ledgerPath string, out io.Writer) (hydrateSummary, error) {
	var summary hydrateSummary
	sessionsRoot := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return summary, nil
		}
		return summary, fmt.Errorf("read sessions dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	// First pass: identify sessions that have at least one pointer file
	// among scannable files. Sessions that are already fully hydrated skip
	// the network entirely.
	type pendingFile struct {
		sessionName string
		filename    string
		inPlacePath string
	}
	var toHydrate []pendingFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		summary.Sessions++
		sessionName := entry.Name()
		sessionDir := filepath.Join(sessionsRoot, sessionName)
		files, err := os.ReadDir(sessionDir)
		if err != nil {
			// Can't even list this session's directory. Don't silently
			// drop it from coverage — count it as Failed and tell the
			// operator. Otherwise the hydration summary could claim
			// full coverage while a whole session was never inspected.
			summary.Failed++
			fmt.Fprintf(out, "  failed to inspect %s: %v\n", sessionName, err)
			continue
		}
		sessionNeedsAny := false
		for _, fEntry := range files {
			if fEntry.IsDir() {
				continue
			}
			filename := fEntry.Name()
			if !ledgerSecretsScanExts[strings.ToLower(filepath.Ext(filename))] {
				continue
			}
			inPlace := filepath.Join(sessionDir, filename)
			if !lfs.IsPointerFile(inPlace) {
				// already real content in-place — nothing to hydrate
				continue
			}
			cachePath := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName, filename)
			if _, err := os.Stat(cachePath); err == nil {
				// already hydrated to cache from a previous run
				continue
			}
			toHydrate = append(toHydrate, pendingFile{sessionName: sessionName, filename: filename, inPlacePath: inPlace})
			sessionNeedsAny = true
		}
		if !sessionNeedsAny {
			summary.AlreadyOK++
		}
	}

	if len(toHydrate) == 0 {
		fmt.Fprintf(out, "Hydration: all %d session(s) already have local content; no LFS fetch needed.\n\n",
			summary.Sessions)
		return summary, nil
	}

	fmt.Fprintf(out, "Hydration: %d file(s) across dehydrated session(s) must be fetched from LFS before scanning...\n",
		len(toHydrate))

	// Track per-session success/failure so the summary is session-scoped
	// (one failed file in a session still means the scan of THAT session
	// is incomplete).
	sessionFailed := map[string]bool{}
	sessionHydrated := map[string]bool{}
	for i, pf := range toHydrate {
		// progress every 25 files to avoid spamming terminals on huge fetches
		if i%25 == 0 && i > 0 {
			fmt.Fprintf(out, "  ... %d / %d hydrated\n", i, len(toHydrate))
		}
		if _, err := openSessionContent(projectRoot, ledgerPath, pf.sessionName, pf.filename); err != nil {
			sessionFailed[pf.sessionName] = true
			summary.FailedFiles++
			continue
		}
		sessionHydrated[pf.sessionName] = true
		summary.HydratedFiles++
	}
	for s := range sessionHydrated {
		if !sessionFailed[s] {
			summary.Hydrated++
		}
	}
	summary.Failed = len(sessionFailed)
	fmt.Fprintf(out, "Hydration complete: %d file(s) fetched across %d session(s).\n\n",
		summary.HydratedFiles, summary.Hydrated)
	return summary, nil
}
