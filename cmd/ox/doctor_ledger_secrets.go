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
// are keyed by detector name; FilesScanned/SessionsScanned describe scope
// for "scanned N session recordings, found 0 secrets" reassurance.
// SessionsHydrated, SessionsHydrationFailed, and SessionsEmpty report on
// the LFS auto-hydration loop. A non-zero SessionsHydrationFailed means
// coverage is incomplete and the result MUST NOT be reported as a clean
// bill of health. SessionsEmpty is benign — it just means a session dir
// existed but had no allowlisted files (e.g. legacy session with only
// .html artifacts).
//
// SessionsSkipped is retained as the legacy alias and equals
// SessionsHydrationFailed + SessionsEmpty for callers that don't need to
// distinguish the two. The check itself uses the finer-grained fields so
// "scan failed" and "nothing to scan" don't get conflated in the
// PassedCheck / FailedCheck decision.
type ledgerSecretsScanResult struct {
	LedgerPath              string
	FilesScanned            int
	SessionsScanned         int
	SessionsHydrated        int // sessions where at least one pointer was fetched from LFS during this call
	SessionsHydrationFailed int // sessions where hydration or read errored — coverage is incomplete
	SessionsEmpty           int // sessions where no allowlisted file was readable (no failure, just nothing to scan)
	SessionsSkipped         int // legacy alias: SessionsHydrationFailed + SessionsEmpty
	Findings                map[string]*ledgerSecretsFinding
}

// ledgerSecretsScanExts lists file extensions worth scanning inside a
// session directory. JSONL is the recording itself; JSON covers meta and
// summary; MD covers any per-session notes; TXT and VTT cover legacy
// transcripts and audio captions. Anything else inside a session dir is
// either binary or not a recording artifact, so we don't pay regex cost.
var ledgerSecretsScanExts = map[string]bool{
	".jsonl": true,
	".json":  true,
	".md":    true,
	".txt":   true,
	".vtt":   true,
}

// ledgerSecretsSkipDirs is retained for the snapshot tarball walk in
// session_redact_history.go (which still archives the whole ledger). The
// credential-scan path no longer uses it — that path iterates session
// directories explicitly rather than filtering a global walk.
var ledgerSecretsSkipDirs = map[string]bool{
	".git":         true,
	".dolt":        true,
	".beads":       true,
	".bak":         true,
	".gc-cache":    true,
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
// The companion `ox session redact` tool (ox-pd5f) handles
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

	// Pre-scan hydration. Without this, dehydrated session files would be
	// scanned as 140-byte LFS pointer stubs and return "clean" regardless
	// of what's inside. The doctor harness suppresses incremental stdout
	// during check execution, so we pass io.Discard for the progress
	// writer — the result counters carry the same information.
	hyd, hydErr := hydrateAllSessionsForScan(gitRoot, ledgerPath, io.Discard)
	if hydErr != nil {
		return FailedCheck(name, fmt.Sprintf("pre-scan hydration error: %v", hydErr), "")
	}

	result, err := scanLedgerForSecrets(gitRoot, ledgerPath)
	if err != nil {
		return FailedCheck(name, fmt.Sprintf("scan error: %v", err), "")
	}
	// Surface hydration outcome on the result so the report line below
	// is accurate. SessionsHydrated tracks "we fetched something during
	// this scan call"; merge with the pre-pass for the cleaner number.
	if hyd.Hydrated > result.SessionsHydrated {
		result.SessionsHydrated = hyd.Hydrated
	}
	if hyd.Failed > 0 {
		result.SessionsSkipped += hyd.Failed
	}

	// Identify the catalog that produced these findings. ox-8bfh: the
	// version+hash is the audit anchor that lets future re-runs decide
	// "this session was last scanned under N7-aaaa, current ruleset is
	// N8-bbbb; the new detectors might catch new findings — re-scan."
	catalogRedactor := session.NewRedactor()
	catalogTag := fmt.Sprintf("catalog=%s (hash %s…)",
		catalogRedactor.CatalogVersion(), catalogRedactor.CatalogHash()[:8])

	scope := fmt.Sprintf("scanned %d session recording file(s) across %d session(s); %s",
		result.FilesScanned, result.SessionsScanned, catalogTag)
	if result.SessionsHydrated > 0 {
		scope += fmt.Sprintf("; auto-hydrated %d session(s) from LFS", result.SessionsHydrated)
	}
	if result.SessionsHydrationFailed > 0 {
		scope += fmt.Sprintf("; %d session(s) unscannable (hydration failed)", result.SessionsHydrationFailed)
	}
	if result.SessionsEmpty > 0 {
		scope += fmt.Sprintf("; %d session(s) had no allowlisted content", result.SessionsEmpty)
	}

	// Decision matrix (ox-zukx + ox-8bfh): a credible audit must not
	// report a clean bill of health when coverage is incomplete.
	// Hydration failures are the broken-coverage signal; empty sessions
	// (no allowlisted content / over-cap files) are benign.
	switch {
	case len(result.Findings) == 0 && result.SessionsHydrationFailed == 0:
		return PassedCheck(name, scope+"; no credential patterns found")
	case len(result.Findings) == 0 && result.SessionsHydrationFailed > 0:
		return FailedCheck(name,
			scope+"; no findings reported BUT scan coverage is incomplete",
			"Re-run after fixing connectivity / auth; until then this result is not a clean bill of health.")
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

	summary := fmt.Sprintf("found %d credential pattern(s) across %d distinct detectors (%s)",
		totalCount, len(result.Findings), scope)

	guidance := "Run `ox session audit` to see per-line findings, then `ox session redact` for interactive cleanup. " +
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

// scanLedgerForSecrets enumerates session-recording files inside
// <ledger>/sessions/<name>/, hydrates LFS pointers to the cache via the
// canonical openSessionContent path, and runs DefaultPatterns against
// every line of every allowlisted-extension file. Aggregates matches per
// detector. Never retains the matched bytes — only counts, file paths,
// and timestamps.
//
// Scope is intentionally session-only: the previous global ledger walk
// matched fixture/test-fixture bytes inside data/github/.../pr/*.json
// (PR-review archives containing the very example credentials these
// detectors are designed to spot) and inflated counts by an order of
// magnitude with non-actionable findings. Anything outside sessions/
// is not a "session recording" and belongs to other audits if needed.
//
// Hydration is mandatory: dehydrated session files are ~140-byte LFS
// pointer stubs that match no credential pattern. Pre-fix, 17.5 % of
// the on-disk file set on a real ledger were pointer stubs returning
// "scanned, clean" — a credible audit cannot have that blind spot.
// openSessionContent writes hydrated bytes to .sageox/cache/sessions/
// per .claude/rules/cache-only-design.md, never in-place.
//
// Exposed (unexported) for use by `ox session audit` and `ox session redact` so all
// surfaces share the same definition of "what counts as a finding."
func scanLedgerForSecrets(projectRoot, ledgerPath string) (*ledgerSecretsScanResult, error) {
	redactor := session.NewRedactor()
	result := &ledgerSecretsScanResult{
		LedgerPath: ledgerPath,
		Findings:   map[string]*ledgerSecretsFinding{},
	}

	sessionsRoot := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil // ledger has no sessions yet — vacuously clean
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionName := entry.Name()
		result.SessionsScanned++

		sessionDir := filepath.Join(sessionsRoot, sessionName)
		files, err := os.ReadDir(sessionDir)
		if err != nil {
			// Couldn't even list the session dir. Treat as a hard failure
			// (not "nothing to scan") so the check downgrades the result.
			result.SessionsHydrationFailed++
			result.SessionsSkipped++
			continue
		}

		hydratedThisSession := false
		anyAllowlistedSeen := false
		anyFileRead := false
		anyFileFailed := false
		for _, fEntry := range files {
			if fEntry.IsDir() {
				continue
			}
			filename := fEntry.Name()
			if !ledgerSecretsScanExts[strings.ToLower(filepath.Ext(filename))] {
				continue
			}
			anyAllowlistedSeen = true

			contentPath, hydratedNow, err := resolveSessionContentForScan(projectRoot, ledgerPath, sessionName, filename)
			if err != nil {
				// Hydration failed (network, missing LFS object, etc.).
				// That's a real failure, not "no content to scan" —
				// flag it so the check downgrades the result.
				anyFileFailed = true
				continue
			}
			if hydratedNow {
				hydratedThisSession = true
			}
			info, err := os.Stat(contentPath)
			if err != nil {
				anyFileFailed = true
				continue
			}
			if info.Size() > ledgerSecretsSizeCap {
				// Over-cap is a deliberate skip, not a failure — don't
				// flag it as hydration-failed, but don't count the file
				// as "read" either.
				continue
			}
			anyFileRead = true
			result.FilesScanned++
			relForReport := filepath.Join("sessions", sessionName, filename)
			if err := scanLedgerFileForSecrets(redactor, contentPath, relForReport, info.ModTime(), result); err != nil {
				anyFileFailed = true
				continue
			}
		}
		if hydratedThisSession {
			result.SessionsHydrated++
		}
		switch {
		case anyFileFailed:
			// At least one file in this session couldn't be hydrated or
			// read. Coverage is incomplete — surface as a hydration
			// failure so the doctor check downgrades the result.
			result.SessionsHydrationFailed++
			result.SessionsSkipped++
		case !anyFileRead && anyAllowlistedSeen:
			// Allowlisted files existed but all were over the size cap.
			// Benign — nothing to scan, but no failure either.
			result.SessionsEmpty++
			result.SessionsSkipped++
		case !anyFileRead && !anyAllowlistedSeen:
			// Session dir has no allowlisted content at all (e.g.
			// legacy session with only .html). Benign skip.
			result.SessionsEmpty++
			result.SessionsSkipped++
		}
	}
	return result, nil
}

// resolveSessionContentForScan is a thin wrapper around openSessionContent
// that also tells the caller whether hydration happened during this call
// (so the audit can report "auto-hydrated N sessions"). The signal is
// approximate — we treat "cache file didn't exist before the call but
// does after" as "hydrated by us." Pre-existing cached files don't
// count, which is what the user wants for the scope summary.
func resolveSessionContentForScan(projectRoot, ledgerPath, sessionName, filename string) (string, bool, error) {
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	cachePath := filepath.Join(cacheDir, filename)
	_, cacheExistedBefore := os.Stat(cachePath)

	resolved, err := openSessionContent(projectRoot, ledgerPath, sessionName, filename)
	if err != nil {
		return "", false, err
	}
	// hydrated by this call iff the cache file did not exist before and
	// the resolved path is the cache path.
	hydratedNow := cacheExistedBefore != nil && resolved == cachePath
	return resolved, hydratedNow, nil
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
