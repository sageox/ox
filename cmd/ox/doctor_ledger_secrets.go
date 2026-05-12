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

// checkLedgerSecrets is the doctor-side credential audit for the local
// Ledger (slug `ledger-secrets`). It scans for credential patterns using
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

// --- ox-yeae: embedded-PAT + credential-helper check ---

// checkLedgerEmbeddedCreds is the post-ox-eeqi invariant guard for the
// ledger's git auth state. It fires on two distinct failures:
//
//  1. The origin URL contains an embedded oauth2:TOKEN (pre-eeqi leftover).
//     The PAT leaks via `git remote -v`, GIT_TRACE, Time Machine, etc.
//  2. The origin URL is bare (post-eeqi) but the ox credential helper isn't
//     installed in this ledger's .git/config. Fetch/push will fail at auth.
//     This is the silent-success trap that the deleted "Ledger remote URL
//     match" check accidentally papered over before this fix.
//
// With `fix=true` it calls MigrateLedgerCredentials — same primitive the
// daemon's startup sweep uses — which both strips any embedded PAT and
// installs/refreshes the helper. Idempotent.
//
// Per ox-yeae: depends on ox-eeqi (the credential helper subcommand) being
// in place, otherwise the daemon's normal sync would re-embed the PAT the
// moment we stripped it. Both directions of the migration land together.
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

	hasEmbedded, host, err := ledgerOriginState(ledgerPath)
	if err != nil {
		return SkippedCheck(name, fmt.Sprintf("origin check error: %v", err), "")
	}
	// host == "" means there's no https origin we'd manage (SSH, file://,
	// no remote yet, non-oauth2 deploy token). Nothing for this check to
	// assert.
	if host == "" {
		return SkippedCheck(name, "no managed https origin", "")
	}

	helperInstalled, helperErr := ledgerHasCredentialHelper(ledgerPath, host)
	if helperErr != nil {
		return SkippedCheck(name, fmt.Sprintf("helper check error: %v", helperErr), "")
	}

	if !hasEmbedded && helperInstalled {
		return PassedCheck(name, "origin URL is bare; ox credential helper installed")
	}

	if !fix {
		if hasEmbedded {
			return FailedCheck(name,
				"origin URL contains embedded oauth2:TOKEN — visible to backups, `git remote -v`, and GIT_TRACE",
				"Run `ox doctor --fix-slug=ledger-embedded-creds` to strip the PAT and install the credential helper.")
		}
		// bare URL, helper missing
		return FailedCheck(name,
			fmt.Sprintf("ox credential helper not configured for %s — fetch/push will fail without it", host),
			"Run `ox doctor --fix-slug=ledger-embedded-creds` to install the credential helper.")
	}

	if _, migErr := gitserver.MigrateLedgerCredentials(ledgerPath, gitserver.DefaultHelperCommand()); migErr != nil {
		return FailedCheck(name, fmt.Sprintf("migration failed: %v", migErr), "")
	}

	// Re-verify post-fix — defense in depth. If MigrateLedgerCredentials
	// reports success but the helper still isn't present (e.g. its host
	// guard rejected our repo, or someone hand-edited .git/config
	// concurrently), fail loudly rather than report a green check that
	// the next push will contradict.
	postHelper, postErr := ledgerHasCredentialHelper(ledgerPath, host)
	if postErr != nil || !postHelper {
		return FailedCheck(name,
			"credential helper still missing after migration attempt",
			fmt.Sprintf("Inspect: git -C %s config --get credential.https://%s.helper", ledgerPath, host))
	}

	if hasEmbedded {
		return PassedCheck(name, "stripped embedded PAT and installed ox credential helper")
	}
	return PassedCheck(name, "installed ox credential helper")
}

// ledgerOriginState returns (hasEmbeddedPAT, host) for the ledger's origin URL.
// host is "" when there is no remote, the remote isn't https, or it carries
// a non-oauth2 userinfo (deploy token) ox shouldn't touch. A non-empty host
// means "this is a remote whose credential state we own."
func ledgerOriginState(ledgerPath string) (hasPAT bool, host string, err error) {
	out, gitErr := exec.Command("git", "-C", ledgerPath, "remote", "get-url", "origin").Output()
	if gitErr != nil {
		// no origin = nothing to manage
		return false, "", nil
	}
	remote := strings.TrimSpace(string(out))
	if remote == "" {
		return false, "", nil
	}
	parsed, parseErr := url.Parse(remote)
	if parseErr != nil || parsed.Scheme != "https" {
		return false, "", nil
	}
	host = parsed.Hostname()
	if host == "" {
		return false, "", nil
	}
	if parsed.User != nil && parsed.User.Username() != "" && parsed.User.Username() != "oauth2" {
		// third-party deploy token — leave alone
		return false, "", nil
	}
	if parsed.User != nil && parsed.User.Username() == "oauth2" {
		if pw, ok := parsed.User.Password(); ok && pw != "" {
			hasPAT = true
		}
	}
	return hasPAT, host, nil
}

// ledgerOriginHasEmbeddedPAT returns whether the ledger's origin URL has an
// embedded oauth2:TOKEN. Thin wrapper over ledgerOriginState for tests that
// only care about the embedded-PAT axis.
func ledgerOriginHasEmbeddedPAT(ledgerPath string) (bool, error) {
	hasPAT, _, err := ledgerOriginState(ledgerPath)
	return hasPAT, err
}

// ledgerHasCredentialHelper returns true when the ledger's .git/config has
// a non-empty `credential.https://<host>.helper` entry. We deliberately do
// NOT verify the helper command's exact value or that the referenced binary
// exists — a user-installed third-party helper is a legitimate override,
// and resolving binary paths under `!ox …` would race against $PATH state.
// The narrow assertion is: there is *some* helper configured for this host.
func ledgerHasCredentialHelper(ledgerPath, host string) (bool, error) {
	out, err := exec.Command("git", "-C", ledgerPath, "config", "--get",
		"credential.https://"+host+".helper").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			// key absent — git's documented signal for "not configured"
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}
