package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
)

// ledgerSecretsFinding aggregates matches of a single detector pattern
// across all files scanned in a ledger. Per ox-zyg7 we DO NOT store the
// matched bytes — printing them would re-leak the credential into terminal
// scrollback, ledger output, and CI logs. Only metadata.
type ledgerSecretsFinding struct {
	Detector  string    // detector name, e.g. "aws_access_key"
	Count     int       // total matches across files
	FileCount int       // distinct files where this detector fired
	FirstSeen time.Time // earliest file mtime where it fired
	LastSeen  time.Time // latest file mtime where it fired
	Sample    string    // one representative file path (relative to ledger)
}

// ledgerSecretsScanResult is what checkLedgerSecrets passes back. Findings
// are keyed by detector name; FilesScanned is total count for "scanned N
// files, found 0 secrets" reassurance.
type ledgerSecretsScanResult struct {
	LedgerPath   string
	FilesScanned int
	Findings     map[string]*ledgerSecretsFinding
}

// ledgerSecretsScanExts lists file extensions worth scanning. The
// remediation set is dominated by JSONL (sessions), JSON (caches and meta),
// markdown (docs, summaries, memory), txt (transcripts), and VTT
// (audio captions). Binaries are skipped — the same logic the pre-push
// scanner uses (see prePushScannerSkipExts) but inverted to an allowlist
// because we expect the ledger to contain a wider mix of file types.
var ledgerSecretsScanExts = map[string]bool{
	".jsonl": true,
	".json":  true,
	".md":    true,
	".txt":   true,
	".vtt":   true,
}

// ledgerSecretsSkipDirs lists subdirectory names that we never descend into
// during the scan. .git/.dolt/.beads contain pack files and SQL state, not
// user-authored content; .gc-cache and .bak are local-only artifacts.
var ledgerSecretsSkipDirs = map[string]bool{
	".git":      true,
	".dolt":     true,
	".beads":    true,
	".bak":      true,
	".gc-cache": true,
	"node_modules": true,
}

// ledgerSecretsSizeCap matches the pre-push scanner's cap. Files bigger
// than this are skipped with a log warning rather than scanned — a real
// session raw.jsonl is comfortably under this even after a long agent run.
const ledgerSecretsSizeCap = 8 * 1024 * 1024

// checkLedgerSecrets implements `ox doctor --check=ledger-secrets`. It
// scans the current project's local Ledger for credential patterns using
// the same DefaultPatterns as the redactor + pre-push gate, so a finding
// here is exactly what would have been blocked at write or push time if
// the gate had been in place.
//
// Per ox-zyg7: read-only by default; the check NEVER prints matched bytes,
// NEVER uploads findings, NEVER mutates the ledger. The `fix` argument is
// ignored — there's no auto-fix for "credential in committed history".
// The companion `ox session redact-history` tool (ox-pd5f) handles
// per-finding interactive cleanup.
func checkLedgerSecrets(fix bool) checkResult {
	name := "Ledger credential scan"
	_ = fix // intentionally ignored — see doc above.

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in git repo", "")
	}
	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck(name, "config error", "")
	}
	ledgerPath := resolveLedgerPathForAudit(localCfg)
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger configured", "")
	}
	if !ledger.Exists(ledgerPath) {
		return SkippedCheck(name, "ledger directory does not exist", "")
	}

	result, err := scanLedgerForSecrets(ledgerPath)
	if err != nil {
		return FailedCheck(name, fmt.Sprintf("scan error: %v", err), "")
	}

	if len(result.Findings) == 0 {
		return PassedCheck(name,
			fmt.Sprintf("scanned %d files, no credential patterns found", result.FilesScanned))
	}

	// Build the failure message without ever including matched bytes.
	// Detector names are sorted for stable output.
	names := make([]string, 0, len(result.Findings))
	for n := range result.Findings {
		names = append(names, n)
	}
	sort.Strings(names)

	var details strings.Builder
	totalCount := 0
	for _, n := range names {
		f := result.Findings[n]
		totalCount += f.Count
		fmt.Fprintf(&details, "  %s: %d matches across %d file(s); sample: %s\n",
			f.Detector, f.Count, f.FileCount, f.Sample)
	}

	summary := fmt.Sprintf("found %d credential pattern(s) across %d distinct detectors in %d scanned files",
		totalCount, len(result.Findings), result.FilesScanned)

	guidance := "Run `ox session redact-history` for interactive per-finding cleanup. " +
		"For already-pushed commits, see docs/security/credential-redaction.md."

	return FailedCheck(name, summary+"\n"+details.String(), guidance)
}

// resolveLedgerPathForAudit returns the ledger path for a project, preferring the
// explicit local config value and falling back to the canonical default.
// Returns "" when no ledger is reachable.
func resolveLedgerPathForAudit(localCfg *config.LocalConfig) string {
	if localCfg != nil && localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		return localCfg.Ledger.Path
	}
	defaultPath, err := ledger.DefaultPath()
	if err != nil {
		return ""
	}
	return defaultPath
}

// scanLedgerForSecrets walks the ledger working tree (skipping .git,
// .beads, etc.) and runs DefaultPatterns against every line of every
// allowlisted-extension file. Aggregates matches per detector. Never
// retains the matched bytes — only counts, file paths, and timestamps.
//
// Exposed (unexported) for use by `ox session redact-history` so both
// surfaces share the same definition of "what counts as a finding."
func scanLedgerForSecrets(ledgerPath string) (*ledgerSecretsScanResult, error) {
	redactor := session.NewRedactor()
	result := &ledgerSecretsScanResult{
		LedgerPath: ledgerPath,
		Findings:   map[string]*ledgerSecretsFinding{},
	}

	err := filepath.WalkDir(ledgerPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// transient stat errors (deleted-mid-walk, permission) — log
			// implicitly by continuing; this is a best-effort audit, not
			// a transactional read.
			return nil
		}
		if d.IsDir() {
			if ledgerSecretsSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !ledgerSecretsScanExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > ledgerSecretsSizeCap {
			return nil
		}
		result.FilesScanned++
		rel, _ := filepath.Rel(ledgerPath, path)
		return scanLedgerFileForSecrets(redactor, path, rel, info.ModTime(), result)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// scanLedgerFileForSecrets reads a single file line-by-line, runs every
// detector pattern, and updates result.Findings in place. Streams via
// bufio so memory cost is O(line length) instead of O(file size).
func scanLedgerFileForSecrets(r *session.Redactor, abs, rel string,
	mtime time.Time, result *ledgerSecretsScanResult,
) error {
	f, err := os.Open(abs)
	if err != nil {
		return nil // skip unreadable files; audit is best-effort
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	hitsInFile := map[string]int{}
	for scanner.Scan() {
		for _, name := range r.ScanForSecrets(scanner.Text()) {
			hitsInFile[name]++
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	for name, count := range hitsInFile {
		f, ok := result.Findings[name]
		if !ok {
			f = &ledgerSecretsFinding{
				Detector:  name,
				FirstSeen: mtime,
				LastSeen:  mtime,
				Sample:    rel,
			}
			result.Findings[name] = f
		}
		f.Count += count
		f.FileCount++
		if mtime.Before(f.FirstSeen) {
			f.FirstSeen = mtime
		}
		if mtime.After(f.LastSeen) {
			f.LastSeen = mtime
		}
	}
	return nil
}

// --- ox-yeae: embedded-PAT check ---

// checkLedgerEmbeddedCreds implements `ox doctor --check=ledger-embedded-creds`.
// It runs extractPATFromRemote against the current project's ledger and
// reports if the origin URL has an embedded oauth2:TOKEN. With `--fix` (which
// the doctor harness threads in as the `fix` arg), it calls
// MigrateLedgerCredentials to strip + install the helper — same primitive
// the daemon's startup sweep uses, so behavior is identical and idempotent.
//
// Per ox-yeae: depends on the credential helper subcommand (ox-eeqi) being
// in place, otherwise the daemon's normal sync flow would re-embed the PAT
// the moment we stripped it. ox-eeqi is closed; both directions of the
// migration land together.
func checkLedgerEmbeddedCreds(fix bool) checkResult {
	name := "Ledger embedded credentials"
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "not in git repo", "")
	}
	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck(name, "config error", "")
	}
	ledgerPath := resolveLedgerPathForAudit(localCfg)
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger configured", "")
	}
	if !ledger.Exists(ledgerPath) {
		return SkippedCheck(name, "ledger directory does not exist", "")
	}

	hasEmbedded, err := ledgerOriginHasEmbeddedPAT(ledgerPath)
	if err != nil {
		return SkippedCheck(name, fmt.Sprintf("origin check error: %v", err), "")
	}
	if !hasEmbedded {
		return PassedCheck(name, "origin URL is bare; credentials live in the credential store")
	}

	if !fix {
		return FailedCheck(name,
			"origin URL contains embedded oauth2:TOKEN — visible to backups, `git remote -v`, and GIT_TRACE",
			"Run `ox doctor --check=ledger-embedded-creds --fix` to strip the PAT and install the credential helper.")
	}

	changed, migErr := gitserver.MigrateLedgerCredentials(ledgerPath, gitserver.DefaultHelperCommand())
	if migErr != nil {
		return FailedCheck(name, fmt.Sprintf("migration failed: %v", migErr), "")
	}
	if !changed {
		return PassedCheck(name, "no embedded PAT found (re-check)")
	}
	return PassedCheck(name, "stripped embedded PAT and installed ox credential helper")
}

// ledgerOriginHasEmbeddedPAT returns true if the ledger's origin URL has an
// oauth2:TOKEN@ prefix. Mirrors the check in gitserver.extractPATFromRemote
// but lives here because that function is unexported and this check has
// project-context-specific path resolution.
func ledgerOriginHasEmbeddedPAT(ledgerPath string) (bool, error) {
	out, err := exec.Command("git", "-C", ledgerPath, "remote", "get-url", "origin").Output()
	if err != nil {
		// no origin = nothing to leak
		return false, nil
	}
	remote := strings.TrimSpace(string(out))
	if remote == "" {
		return false, nil
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.User == nil {
		return false, nil
	}
	if parsed.User.Username() != "oauth2" {
		return false, nil
	}
	pw, hasPW := parsed.User.Password()
	return hasPW && pw != "", nil
}
