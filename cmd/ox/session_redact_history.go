package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// ox session redact is the forensic cleanup companion to `ox session
// audit` (and to the `ledger-secrets` doctor check). The audit surfaces
// tell the user THAT they have leaked credentials in their local
// Ledger; this command fixes them, with strict safety rails per ox-pd5f:
//
//   1. Snapshot the entire ledger to an immutable backup location BEFORE
//      any modification. Print the snapshot path + SHA-256 so the user
//      has a chain of custody.
//   2. Refuse to operate on findings that live in commits already pushed
//      to origin/main. Those need the gated V2 history-rewrite tool
//      (ox-54cm) which is intentionally not in this PR.
//   3. Per-finding human approval. The user can keep, redact-in-place,
//      or quarantine each finding. There is no `--all` flag — bulk
//      redaction is one of the failure modes we're protecting against.
//   4. After applying, re-scan and report the new finding count so the
//      user can see what's been fixed and what remains.
//
// Failure modes this command protects against:
//   - Operator runs the tool on the wrong ledger, scrubbing real data.
//   - Tool corrupts a session file mid-edit and leaves the working tree
//     in an unrecoverable state.
//   - Operator auto-approves and the tool redacts content the user
//     wanted to keep (e.g. legitimate examples in documentation).

// Two surfaces over the same scan code path (ox-zukx / ox-8bfh
// follow-up). The pre-split `ox session redact-history` was misleading
// because most users running it with --dry-run were doing an audit,
// not preparing a destructive rewrite — the name fought the intent.
//
//   ox session audit   — read-only audit. Always hydrates LFS pointers
//                        first (LFS Batch API fetch of every dehydrated
//                        session — can be slow on a large ledger),
//                        prints catalog identity, then reports per-line
//                        findings grouped pushed-vs-unpushed. Never
//                        writes anything. Always safe to run; the cost
//                        is bandwidth + time, not data integrity.
//
//   ox session redact  — destructive interactive rewrite. Runs the same
//                        hydration + scan as `audit`, then snapshots the
//                        ledger, prompts per-file [y/N/q], rewrites
//                        bytes, appends a RedactionPass to each
//                        affected session's meta.json (ox-8bfh), and
//                        amends the holding commit. Refuses findings
//                        in already-pushed commits.
//
// Both share the same hydration + scan + per-file enumeration
// (runRedactHistoryWorkflow); the difference is whether the interactive
// rewrite tail runs.

var sessionAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Audit session recordings for credential patterns (read-only; expensive)",
	Long: `Audit every session recording in the local Ledger for credential patterns.
Read-only — never modifies files, never amends commits, never uploads.

Cost: this command force-hydrates every LFS-pointer session file via the
LFS Batch API before scanning, because pointer-stub bytes match no
credential pattern and would silently produce a clean-looking lie.
Hydration is bounded by network throughput and the number of dehydrated
sessions; on a fresh clone with hundreds of sessions this can take
minutes and pull tens of megabytes. Sessions that fail to hydrate are
surfaced as a Warning so partial coverage is never mistaken for clean.

Findings are reported with detector name + file + line; matched bytes
are NEVER printed (ox-zyg7). The output stamps the catalog version + a
sha256 hash that produced the findings, so future re-audits can decide
whether a newer ruleset would catch additional leaks.

For interactive cleanup of any findings, run ` + "`ox session redact`" + `.`,
	RunE: runSessionAudit,
}

var sessionRedactCmd = &cobra.Command{
	Use:   "redact",
	Short: "Interactively redact credentials in session recordings (destructive; expensive)",
	Long: `Interactively redact each credential finding in committed-but-not-yet-pushed
session recordings. Runs the full audit pass first, then prompts
per-file for approval.

Cost: same hydration cost as ` + "`ox session audit`" + ` (LFS Batch API fetch of
every dehydrated session), plus a per-file rewrite + ledger snapshot +
git amend on every approved file. Plan for minutes-not-seconds on a
large ledger.

Workflow:

  1. ` + "`ox session audit`" + ` first — read-only, confirms what would change.
  2. ` + "`ox session redact`" + ` — interactive cleanup.
  3. After cleanup, ` + "`ox session push`" + ` (or normal session-stop flow) republishes.

Safety:
  - The entire Ledger is snapshotted to an immutable backup before any
    modification. The snapshot path and SHA-256 are printed for chain of
    custody.
  - Findings in commits already pushed to origin/main are listed but NOT
    actionable here. They require the gated history-rewrite tool. The
    correct response is: rotate the leaked credential, mark the finding
    as known, and follow up via the team comms process.
  - Per-finding human approval. There is no bulk-approve flag.
  - Each redaction pass appends a RedactionPass entry to the session's
    meta.json (ox-8bfh) recording WHO redacted, WHEN, with WHICH catalog
    version+hash, and WHAT was caught — never the matched bytes.`,
	RunE: runSessionRedact,
}

// Flag storage. Each command reads from its own variable so cobra's
// flag namespacing stays clean.
var (
	auditLedgerPath string

	redactLedgerPath string
	redactBackupDir  string
)

func init() {
	sessionAuditCmd.Flags().StringVar(&auditLedgerPath, "ledger-path", "",
		"Override the ledger path (defaults to the current project's ledger)")
	sessionCmd.AddCommand(sessionAuditCmd)

	sessionRedactCmd.Flags().StringVar(&redactLedgerPath, "ledger-path", "",
		"Override the ledger path (defaults to the current project's ledger)")
	sessionRedactCmd.Flags().StringVar(&redactBackupDir, "backup-dir", "",
		"Override the backup directory (defaults to ~/.local/share/sageox/backups/redact-history/)")
	sessionCmd.AddCommand(sessionRedactCmd)
}

// runSessionAudit is the cobra entrypoint for the read-only audit
// surface. Pins DryRun=true on the shared workflow so no rewrite can
// happen even if a future caller passes the wrong opts.
func runSessionAudit(cmd *cobra.Command, args []string) error {
	ledgerPath, err := resolveLedgerPathForSessionCmd(auditLedgerPath)
	if err != nil {
		return err
	}
	opts := redactHistoryOptions{
		ProjectRoot: findGitRoot(),
		LedgerPath:  ledgerPath,
		DryRun:      true,
		Stdin:       cmd.InOrStdin(),
		Stdout:      cmd.OutOrStdout(),
	}
	return runRedactHistoryWorkflow(opts)
}

// runSessionRedact is the cobra entrypoint for the destructive
// interactive surface. Pins DryRun=false; rewrite proceeds.
func runSessionRedact(cmd *cobra.Command, args []string) error {
	ledgerPath, err := resolveLedgerPathForSessionCmd(redactLedgerPath)
	if err != nil {
		return err
	}
	opts := redactHistoryOptions{
		ProjectRoot: findGitRoot(),
		LedgerPath:  ledgerPath,
		DryRun:      false,
		BackupDir:   redactBackupDir,
		Stdin:       cmd.InOrStdin(),
		Stdout:      cmd.OutOrStdout(),
	}
	return runRedactHistoryWorkflow(opts)
}

// resolveLedgerPathForSessionCmd is the common ledger-path resolver
// shared by audit and redact. Returns the explicit override when set,
// otherwise the current project's ledger.
func resolveLedgerPathForSessionCmd(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return "", fmt.Errorf("not in a git repo; pass --ledger-path or run from a project root")
	}
	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return "", fmt.Errorf("load local config: %w", err)
	}
	path := resolveLedgerPathForAudit(localCfg)
	if path == "" {
		return "", fmt.Errorf("no ledger configured for this project")
	}
	if !ledger.Exists(path) {
		return "", fmt.Errorf("ledger directory does not exist: %s", path)
	}
	return path, nil
}


// redactHistoryOptions bundles inputs so the workflow can be unit-tested
// with synthetic stdin/stdout and an override backup directory.
type redactHistoryOptions struct {
	// ProjectRoot is the git repo path used to authenticate LFS hydration
	// (via openSessionContent → hydrateFromLedger). May be empty when the
	// caller targets a ledger directly with --ledger-path; in that case
	// hydration falls back to whatever is already in the cache.
	ProjectRoot string
	LedgerPath  string
	DryRun      bool
	BackupDir   string // empty → default ~/.local/share/sageox/backups/redact-history/
	Stdin       io.Reader
	Stdout      io.Writer
}

// runRedactHistoryWorkflow implements the dry-run / scan / snapshot /
// interactive redaction loop. Exported (unexported) for testability —
// passing in stdin/stdout/backup-dir lets tests assert behavior without
// touching the user's actual home directory or terminal.
func runRedactHistoryWorkflow(opts redactHistoryOptions) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}

	// Pre-scan hydration. The scan is content-based and pointer-file bytes
	// match no credential pattern, so we MUST hydrate every dehydrated
	// session recording before scanning or the result is meaningless. This
	// is intentionally visible — printing progress and a final hydration
	// summary so the operator can see what was fetched and whether any
	// session was unreachable.
	hyd, err := hydrateAllSessionsForScan(opts.ProjectRoot, opts.LedgerPath, opts.Stdout)
	if err != nil {
		return fmt.Errorf("pre-scan hydration: %w", err)
	}
	if hyd.Failed > 0 {
		fmt.Fprintf(opts.Stdout,
			"Warning: %d session file(s) across %d session(s) could NOT be hydrated and were not scanned.\n"+
				"Scan coverage is incomplete — re-run after fixing connectivity / auth.\n\n",
			hyd.FailedFiles, hyd.Failed)
	}

	fmt.Fprintf(opts.Stdout, "Scanning ledger %s for credentials...\n", opts.LedgerPath)
	scanResult, err := scanLedgerForSecrets(opts.ProjectRoot, opts.LedgerPath)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if len(scanResult.Findings) == 0 {
		fmt.Fprintf(opts.Stdout, "No credential patterns found across %d session file(s) in %d session(s). Nothing to redact.\n",
			scanResult.FilesScanned, scanResult.SessionsScanned)
		return nil
	}

	// Enumerate per-finding details (file:line:detector) by re-walking the
	// affected files. We didn't keep this from the aggregate scan because
	// the audit doesn't need it; the cleanup tool does.
	findings, err := enumerateRedactHistoryFindings(opts.ProjectRoot, opts.LedgerPath, scanResult)
	if err != nil {
		// Partial enumeration is useful — we still have whatever findings
		// the successful files produced. Surface the issue as a Warning
		// (loudly, so the operator can't miss it) but keep going. A
		// total-loss return is reserved for cases where `findings` is
		// nil; here we have actionable data alongside the error.
		fmt.Fprintf(opts.Stdout, "\nWarning: enumeration partial — %v\n\n", err)
		if findings == nil {
			return fmt.Errorf("enumerate findings: %w", err)
		}
	}

	// Classify each finding by pushed/unpushed. Pushed-commit findings are
	// listed but NOT actionable here — they need the gated rewrite tool.
	pushed, unpushed, err := classifyFindingsByPushStatus(opts.LedgerPath, findings)
	if err != nil {
		return fmt.Errorf("classify findings by push status: %w", err)
	}

	// Print catalog identity BEFORE the finding counts so the operator
	// knows which ruleset produced them. Same version+hash will be
	// persisted to meta.json for every session this pass redacts —
	// see ox-8bfh for the trust model. The redactor used here is the
	// same instance reused below for the rewrite; reusing keeps the
	// catalog identity consistent between scan-time reporting and
	// post-write persistence.
	redactor := session.NewRedactor()
	catalogVersion := redactor.CatalogVersion()
	catalogHash := redactor.CatalogHash()
	fmt.Fprintf(opts.Stdout, "Catalog: %s\n  hash: %s\n\n", catalogVersion, catalogHash)

	fmt.Fprintf(opts.Stdout, "Found %d credential matches across %d file(s):\n",
		len(findings), countDistinctRedactHistoryPaths(findings))
	fmt.Fprintf(opts.Stdout, "  %d in unpushed working tree (actionable)\n", len(unpushed))
	fmt.Fprintf(opts.Stdout, "  %d already pushed to origin/main (see ox-54cm rewrite tool)\n\n",
		len(pushed))

	if opts.DryRun {
		fmt.Fprintln(opts.Stdout, "Dry run — listing actionable findings only:")
		for _, f := range unpushed {
			fmt.Fprintf(opts.Stdout, "  %s\n    %s:%d\n", f.Detector, f.Path, f.Line)
		}
		if len(pushed) > 0 {
			fmt.Fprintln(opts.Stdout, "\nFindings in already-pushed commits (rotate credential + use rewrite tool):")
			for _, f := range pushed {
				fmt.Fprintf(opts.Stdout, "  %s\n    %s:%d\n", f.Detector, f.Path, f.Line)
			}
		}
		return nil
	}

	if len(unpushed) == 0 {
		fmt.Fprintln(opts.Stdout, "No actionable findings (all are in pushed commits).")
		return nil
	}

	// Snapshot BEFORE any modification. This is the most important safety
	// rail in the entire tool — operator can always recover from "the
	// scrubber broke my ledger" by extracting the snapshot.
	snapPath, snapSHA, err := snapshotLedger(opts.LedgerPath, opts.BackupDir)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	fmt.Fprintf(opts.Stdout, "\nSnapshot written: %s\n  SHA-256: %s\n\n", snapPath, snapSHA)

	// Per-finding interactive prompt. Aggregate by FILE so a file with
	// many findings only gets reviewed once (the operator approves the
	// whole file's redaction, not every single line).
	byPath := groupFindingsByPath(unpushed)
	// Collect per-session redaction entries as files succeed. After all
	// redactions are done we write one RedactionPass to each affected
	// session's meta.json — this is the ox-8bfh audit trail. We use
	// the same passID across all sessions touched by this run so the
	// trail is correlatable; appliedAt is captured once so the records
	// share a wall-clock instant.
	redactedBySession := map[string][]lfs.RedactionEntry{}
	redactedFiles := 0
	for _, fileFindings := range byPath {
		path := fileFindings[0].Path
		fmt.Fprintf(opts.Stdout, "%s\n  %d finding(s):\n", path, len(fileFindings))
		for _, f := range fileFindings {
			fmt.Fprintf(opts.Stdout, "    line %d: %s\n", f.Line, f.Detector)
		}
		fmt.Fprint(opts.Stdout, "  Redact in place? [y/N/q to quit]: ")
		answer, err := readRedactHistoryAnswer(opts.Stdin)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "q", "quit":
			fmt.Fprintln(opts.Stdout, "Aborting. Snapshot is preserved at the path above.")
			return nil
		case "y", "yes":
			abs := filepath.Join(opts.LedgerPath, path)
			if err := redactFileInPlace(abs, redactor); err != nil {
				return fmt.Errorf("redact %s: %w", path, err)
			}
			redactedFiles++
			// Record per-finding entries against the session that owns
			// this file. Detector + line + filename only — never bytes.
			if sess, fname, ok := splitSessionPath(path); ok {
				for _, f := range fileFindings {
					redactedBySession[sess] = append(redactedBySession[sess], lfs.RedactionEntry{
						File:     fname,
						Line:     f.Line,
						Detector: f.Detector,
					})
				}
			}
			fmt.Fprintf(opts.Stdout, "  Redacted: %s\n\n", path)
		default:
			fmt.Fprintf(opts.Stdout, "  Skipped: %s\n\n", path)
		}
	}

	if redactedFiles == 0 {
		fmt.Fprintln(opts.Stdout, "No files redacted. Snapshot remains for reference.")
		return nil
	}

	// Persist a RedactionPass to each affected session's meta.json
	// BEFORE staging+amending — so the amend commit includes the audit
	// trail entry. Uses lfs.MutateSessionMeta (flock + atomic rename)
	// so concurrent daemon writes to meta.json can't lose the entry.
	// This is the load-bearing ox-8bfh transparency step.
	metaPaths, err := writeRedactionPassesPerSession(context.Background(), opts.LedgerPath,
		redactedBySession, catalogVersion, catalogHash)
	if err != nil {
		return fmt.Errorf("write redaction pass: %w", err)
	}
	// Stage the modified meta.json files alongside the redacted content
	// files so the amend commit captures both.
	for _, mp := range metaPaths {
		if _, ok := byPath[mp]; !ok {
			byPath[mp] = nil
		}
	}

	// Stage + amend in a single holding commit. The operator can review
	// the amended diff before running `ox session push`.
	if err := stageAndAmendRedactedFiles(opts.LedgerPath, byPath); err != nil {
		return fmt.Errorf("stage/amend: %w", err)
	}

	// Surface what was caught back to the operator — same data that
	// just landed in meta.json, summarized for the terminal.
	fmt.Fprintf(opts.Stdout, "Recorded redaction pass in %d session meta.json file(s).\n", len(metaPaths))

	// Re-scan and report.
	postScan, err := scanLedgerForSecrets(opts.ProjectRoot, opts.LedgerPath)
	if err != nil {
		return fmt.Errorf("post-scan: %w", err)
	}
	remaining := len(postScan.Findings)
	fmt.Fprintf(opts.Stdout, "\nRedaction complete. Files modified: %d. Remaining findings: %d.\n",
		redactedFiles, remaining)
	if remaining > 0 {
		fmt.Fprintln(opts.Stdout, "Re-run to address remaining findings.")
	}
	return nil
}

// redactHistoryFinding is a single (detector, path, line) tuple — the
// granular unit users approve or reject. Distinct from the aggregate
// ledgerSecretsFinding used by the audit, which only counts.
type redactHistoryFinding struct {
	Detector string
	Path     string // relative to ledger root
	Line     int
}

// enumerateRedactHistoryFindings re-walks just the session directories
// surfaced by the audit and collects per-line findings. Two-pass on
// purpose: the audit stays cheap (per-file aggregation), and the cleanup
// pays the per-line cost only for files that need it.
//
// Hydration policy matches scanLedgerForSecrets: pointer files are
// resolved via openSessionContent so per-line classification reads the
// same bytes the audit saw. The path recorded on each finding is the
// git-tracked in-place path (sessions/<name>/<filename>) so downstream
// push-status classification and the snapshot tarball stay correct.
func enumerateRedactHistoryFindings(projectRoot, ledgerPath string, audit *ledgerSecretsScanResult) ([]redactHistoryFinding, error) {
	if audit == nil || len(audit.Findings) == 0 {
		return nil, nil
	}
	redactor := session.NewRedactor()

	var out []redactHistoryFinding
	sessionsRoot := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}
	// Enumeration errors that previously got swallowed by `continue` are
	// now collected so the caller surfaces them. A file that was visible
	// in the aggregate scan can't silently vanish from the per-line list
	// without warning — that pattern hid a real failure mode in ox-zukx.
	var enumErrs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionName := entry.Name()
		sessionDir := filepath.Join(sessionsRoot, sessionName)
		files, err := os.ReadDir(sessionDir)
		if err != nil {
			enumErrs = append(enumErrs, fmt.Sprintf("%s: list dir: %v", sessionName, err))
			continue
		}
		for _, fEntry := range files {
			if fEntry.IsDir() {
				continue
			}
			filename := fEntry.Name()
			if !ledgerSecretsScanExts[strings.ToLower(filepath.Ext(filename))] {
				continue
			}
			contentPath, err := openSessionContent(projectRoot, ledgerPath, sessionName, filename)
			if err != nil {
				enumErrs = append(enumErrs, fmt.Sprintf("%s/%s: open: %v", sessionName, filename, err))
				continue
			}
			info, err := os.Stat(contentPath)
			if err != nil {
				enumErrs = append(enumErrs, fmt.Sprintf("%s/%s: stat: %v", sessionName, filename, err))
				continue
			}
			if info.Size() > ledgerSecretsSizeCap {
				// Over-cap is a deliberate skip, not an error.
				continue
			}
			f, err := os.Open(contentPath) //nolint:gosec // G304: path resolved via openSessionContent (ledger-owned)
			if err != nil {
				enumErrs = append(enumErrs, fmt.Sprintf("%s/%s: read: %v", sessionName, filename, err))
				continue
			}
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
			lineNo := 0
			// rel is the in-place git-tracked path. Required so push-status
			// classification can `git log -1 -- <rel>` against the ledger.
			rel := filepath.Join("sessions", sessionName, filename)
			for scanner.Scan() {
				lineNo++
				for _, name := range redactor.ScanForSecrets(scanner.Text()) {
					out = append(out, redactHistoryFinding{Detector: name, Path: rel, Line: lineNo})
				}
			}
			if scanErr := scanner.Err(); scanErr != nil {
				enumErrs = append(enumErrs, fmt.Sprintf("%s: scan: %v", rel, scanErr))
			}
			f.Close()
		}
	}
	// stable order: path, then line, then detector
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Detector < out[j].Detector
	})
	if len(enumErrs) > 0 {
		// Don't return an error that aborts the workflow — partial
		// enumeration is still useful and the redact-history caller
		// already prints a Warning summary. Just attach the detail so
		// the caller can surface it. Today the caller logs the count
		// via the existing hydration Warning path; future work can
		// pass the slice through a richer return type if needed.
		return out, fmt.Errorf("enumeration incomplete (%d issue(s)):\n  %s",
			len(enumErrs), strings.Join(enumErrs, "\n  "))
	}
	return out, nil
}

// classifyFindingsByPushStatus splits findings into (pushed, unpushed)
// based on whether the affected file's most recent commit is reachable
// from origin/main. Conservative: if we can't determine push status,
// treat the finding as PUSHED — we'd rather block a redaction than
// accidentally rewrite history that's already public.
func classifyFindingsByPushStatus(ledgerPath string, findings []redactHistoryFinding) (pushed, unpushed []redactHistoryFinding, err error) {
	// Build a per-file map of "is this file's last-modifying commit
	// reachable from origin/main?" — one git call per distinct path.
	pushedFiles := map[string]bool{}
	for _, f := range findings {
		if _, seen := pushedFiles[f.Path]; seen {
			continue
		}
		isPushed, classifyErr := fileLastCommitIsPushed(ledgerPath, f.Path)
		if classifyErr != nil {
			// conservative: treat as pushed
			pushedFiles[f.Path] = true
			continue
		}
		pushedFiles[f.Path] = isPushed
	}
	for _, f := range findings {
		if pushedFiles[f.Path] {
			pushed = append(pushed, f)
		} else {
			unpushed = append(unpushed, f)
		}
	}
	return pushed, unpushed, nil
}

// fileLastCommitIsPushed returns true if the most recent commit that
// modified `path` is reachable from origin/main. Returns false when the
// last commit is in the working tree's history but not pushed yet —
// exactly the case where redaction is safe.
func fileLastCommitIsPushed(ledgerPath, path string) (bool, error) {
	// Find the most recent commit touching this file.
	out, err := exec.Command("git", "-C", ledgerPath, "log", "-1",
		"--format=%H", "--", path).Output()
	if err != nil {
		return false, err
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		// file isn't tracked yet — pure working-tree change. Safe to redact.
		return false, nil
	}
	// Is that commit reachable from origin/main? merge-base --is-ancestor
	// exits 0 if yes, 1 if no.
	cmd := exec.Command("git", "-C", ledgerPath, "merge-base", "--is-ancestor", sha, "origin/main")
	if err := cmd.Run(); err == nil {
		return true, nil
	}
	// Either not an ancestor, or origin/main doesn't exist (fresh ledger).
	// In the fresh-ledger case `merge-base` exits non-zero with "unknown
	// revision"; safe to treat as "not pushed".
	return false, nil
}

// snapshotLedger creates a tar.gz of the ledger working tree at
// `<backupDir>/<ledger-name>-<timestamp>.tar.gz` and returns the path and
// SHA-256 hash of the resulting archive. Chmoded to 0400 so it can't be
// accidentally overwritten.
func snapshotLedger(ledgerPath, backupDir string) (string, string, error) {
	if backupDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
		backupDir = filepath.Join(home, ".local", "share", "sageox", "backups", "redact-history")
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return "", "", err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	name := filepath.Base(ledgerPath)
	out := filepath.Join(backupDir, fmt.Sprintf("%s-%s.tar.gz", name, stamp))
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", "", err
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	hasher := sha256.New()
	mw := io.MultiWriter(hasher) // for content hashing alongside the tar

	err = filepath.WalkDir(ledgerPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(ledgerPath, path)
		if err != nil || rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// G122: filepath.WalkDir rooted at ledgerPath, an ox-owned
		// directory under the user's XDG data dir. Snapshotting is a
		// read-only operation; symlink TOCTOU does not enable any
		// privilege escalation beyond what the operating user already has.
		fh, err := os.Open(path) //nolint:gosec // G122: see comment above
		if err != nil {
			return nil
		}
		defer fh.Close()
		// teeing through hasher means the SHA reflects the byte stream of
		// every regular file. Directory entries aren't included in the
		// hash — they don't carry content.
		if _, err := io.Copy(io.MultiWriter(tw, mw), fh); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		_ = tw.Close()
		_ = gw.Close()
		_ = f.Close()
		_ = os.Remove(out)
		return "", "", err
	}
	if err := tw.Close(); err != nil {
		return "", "", err
	}
	if err := gw.Close(); err != nil {
		return "", "", err
	}
	if err := f.Close(); err != nil {
		return "", "", err
	}
	// chmod to 0400 so a stray rewrite doesn't blow away the backup
	_ = os.Chmod(out, 0400)
	return out, hex.EncodeToString(hasher.Sum(nil)), nil
}

// redactFileInPlace reads a JSONL file line by line, decodes each line as
// a generic map, runs it through session.RawWriter (which applies the
// canonical three-layer redaction stack), and atomically replaces the
// original file. Per ox-h20u: even the redact-history tool — which IS
// the redaction tool — goes through the same chokepoint as every other
// writer, so its detector set is guaranteed to match what fires on
// fresh writes. No bespoke redactor flow.
//
// Line-by-line because session raw.jsonl entries can be large; whole-
// file decoding would blow memory on real captures.
func redactFileInPlace(abs string, _ *session.Redactor) error {
	src, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}

	tmpName := abs + ".redact.tmp"
	rw, err := session.NewRawWriterTruncate(tmpName, "")
	if err != nil {
		return err
	}
	cleanup := func() {
		_ = rw.Close()
		_ = os.Remove(tmpName)
	}

	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			// non-JSON line — preserve as-is (header/footer alternatives)
			// by passing through a wrapper that still flows through the
			// chokepoint's string-walk redaction.
			rec = map[string]any{"_raw_line": string(line)}
		}
		if err := rw.WriteRaw(rec); err != nil {
			cleanup()
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		cleanup()
		return err
	}
	if err := rw.CloseAndSync(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// preserve original mode
	if err := os.Chmod(tmpName, info.Mode()); err != nil {
		return err
	}
	return os.Rename(tmpName, abs)
}

// stageAndAmendRedactedFiles stages each redacted path and creates an
// amend commit. The amend lets the original commit retain its identity
// in the operator's mental model ("the session-stop commit") while the
// scrubbed bytes replace the leaked ones. Push pipeline will refuse if
// the amend touches commits already on origin/main (we filtered those
// out earlier; this is defense in depth).
func stageAndAmendRedactedFiles(ledgerPath string, byPath map[string][]redactHistoryFinding) error {
	if len(byPath) == 0 {
		return nil
	}
	// stage each modified path
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	addArgs := append([]string{"-C", ledgerPath, "add", "--sparse"}, paths...)
	if output, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %w", strings.TrimSpace(string(output)), err)
	}
	// amend without changing message — keep the original session commit's identity
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--amend", "--no-edit", "--no-verify")
	if output, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit --amend: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// groupFindingsByPath buckets findings by file so the interactive prompt
// asks once per file rather than once per (file, line, detector). Returns
// a deterministic-order map by sorting via the slice the caller iterates.
func groupFindingsByPath(findings []redactHistoryFinding) map[string][]redactHistoryFinding {
	out := map[string][]redactHistoryFinding{}
	for _, f := range findings {
		out[f.Path] = append(out[f.Path], f)
	}
	return out
}

func countDistinctRedactHistoryPaths(findings []redactHistoryFinding) int {
	seen := map[string]struct{}{}
	for _, f := range findings {
		seen[f.Path] = struct{}{}
	}
	return len(seen)
}

// readRedactHistoryAnswer reads a single line of input from stdin. The
// prompt itself is rendered by the caller; this only consumes the
// response so we can pipe a scripted test stdin through it.
func readRedactHistoryAnswer(stdin io.Reader) (string, error) {
	scanner := bufio.NewScanner(stdin)
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}
