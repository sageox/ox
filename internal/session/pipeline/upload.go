package pipeline

import (
	"fmt"
	"log/slog"
	"path/filepath"
)

// CopySessionToLedger copies raw.jsonl and secondary artifacts from the local
// cache to the ledger session directory. raw.jsonl is the critical source of
// truth — its copy must succeed. Secondary artifacts are best-effort.
//
// Returns an error only if the critical raw.jsonl copy fails.
func CopySessionToLedger(fs FileSystem, result *Result, ledgerPath, sessionName string) error {
	if result.EntryCount == 0 {
		slog.Info("skipping copy: zero entries", "session", sessionName)
		return nil
	}
	if result.RawPath == "" {
		return fmt.Errorf("copy %s to ledger: source path is empty", LedgerFileRaw)
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionDir := filepath.Join(sessionsDir, sessionName)
	if err := fs.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	// raw.jsonl is critical — must succeed
	dstPath := filepath.Join(sessionDir, LedgerFileRaw)
	data, err := fs.ReadFile(result.RawPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", LedgerFileRaw, err)
	}
	if err := fs.WriteFile(dstPath, data, 0644); err != nil {
		return fmt.Errorf("copy %s to ledger: %w", LedgerFileRaw, err)
	}

	// secondary artifacts — best-effort
	for name, srcPath := range result.SecondaryArtifacts() {
		if srcPath == "" {
			continue
		}
		dstPath := filepath.Join(sessionDir, name)
		data, err := fs.ReadFile(srcPath)
		if err != nil {
			slog.Debug("skip secondary artifact", "file", name, "error", err)
			continue
		}
		if err := fs.WriteFile(dstPath, data, 0644); err != nil {
			slog.Debug("skip secondary artifact", "file", name, "error", err)
		}
	}

	return nil
}

// RewriteLedgerPaths rewrites all cache paths on result to their ledger equivalents.
// Called after a successful upload so JSON output references the canonical location.
// Uses fs to verify secondary artifacts exist before rewriting.
//
// NOTE: This rewrites RawPath to the ledger copy, which becomes an LFS stub
// after push. Use RewriteSecondaryPaths instead when the caller needs to keep
// RawPath pointing to the local cache for subsequent processing (e.g., push-summary).
func RewriteLedgerPaths(fsys FileSystem, result *Result) {
	if result.LedgerSessionDir == "" {
		return
	}

	// raw is always present after successful upload
	result.RawPath = filepath.Join(result.LedgerSessionDir, LedgerFileRaw)

	rewriteSecondary(fsys, result)
}

// RewriteSecondaryPaths rewrites secondary artifact paths (summary.md, session.md,
// plan.md) to their ledger equivalents, but keeps RawPath unchanged. This preserves
// the local cache path for raw.jsonl so agents can read it after upload — the ledger
// copy becomes an LFS stub after push.
func RewriteSecondaryPaths(fsys FileSystem, result *Result) {
	rewriteSecondary(fsys, result)
}

func rewriteSecondary(fsys FileSystem, result *Result) {
	if result.LedgerSessionDir == "" {
		return
	}

	rewriteIfExists := func(field *string, name string) {
		if *field == "" {
			return
		}
		p := filepath.Join(result.LedgerSessionDir, name)
		if _, err := fsys.Stat(p); err == nil {
			*field = p
		} else {
			*field = "" // didn't make it to ledger
		}
	}
	rewriteIfExists(&result.SummaryMDPath, LedgerFileSummaryMD)
	rewriteIfExists(&result.SessionMDPath, LedgerFileSessionMD)
	rewriteIfExists(&result.PlanPath, LedgerFilePlan)
	rewriteIfExists(&result.ContextTracePath, LedgerFileContextTrace)
}
