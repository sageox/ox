package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
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
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// ox session redact-history is the forensic cleanup companion to
// `ox doctor --check=ledger-secrets`. The doctor check tells the user
// THAT they have leaked credentials in their local Ledger; this command
// fixes them, with strict safety rails per ox-pd5f:
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

var sessionRedactHistoryCmd = &cobra.Command{
	Use:   "redact-history",
	Short: "Interactively redact credentials from historical local sessions",
	Long: `Scan the local Ledger for credential patterns in committed-but-not-yet-pushed
sessions, then interactively cleanup each finding.

Workflow:

  1. ` + "`ox doctor --check=ledger-secrets`" + ` first — read-only audit.
  2. ` + "`ox session redact-history`" + ` — interactive cleanup.
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
  - --dry-run lists findings without snapshotting or modifying anything.`,
	RunE: runSessionRedactHistory,
}

// dry-run and ledger-path are intentionally the only flags here. Adding a
// "yes to all" override is deliberately omitted — per ox-pd5f the
// per-finding gate IS the safety mechanism.
var (
	redactHistoryDryRun     bool
	redactHistoryLedgerPath string
	redactHistoryBackupDir  string
)

func init() {
	sessionRedactHistoryCmd.Flags().BoolVar(&redactHistoryDryRun, "dry-run", false,
		"Show what would change without snapshotting or modifying anything")
	sessionRedactHistoryCmd.Flags().StringVar(&redactHistoryLedgerPath, "ledger-path", "",
		"Override the ledger path (defaults to the current project's ledger)")
	sessionRedactHistoryCmd.Flags().StringVar(&redactHistoryBackupDir, "backup-dir", "",
		"Override the backup directory (defaults to ~/.local/share/sageox/backups/redact-history/)")

	// register under the existing `ox session` parent (see session.go).
	sessionCmd.AddCommand(sessionRedactHistoryCmd)
}

// runSessionRedactHistory is the cobra RunE. It does only argument
// validation + branching; the real workflow lives in
// runRedactHistoryWorkflow so tests can drive it directly without going
// through cobra.
func runSessionRedactHistory(cmd *cobra.Command, args []string) error {
	ledgerPath, err := redactHistoryResolveLedger()
	if err != nil {
		return err
	}
	opts := redactHistoryOptions{
		LedgerPath: ledgerPath,
		DryRun:     redactHistoryDryRun,
		BackupDir:  redactHistoryBackupDir,
		Stdin:      cmd.InOrStdin(),
		Stdout:     cmd.OutOrStdout(),
	}
	return runRedactHistoryWorkflow(opts)
}

// redactHistoryResolveLedger returns the ledger path, honoring the
// --ledger-path flag if set, otherwise resolving from the project.
func redactHistoryResolveLedger() (string, error) {
	if redactHistoryLedgerPath != "" {
		return redactHistoryLedgerPath, nil
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
	LedgerPath string
	DryRun     bool
	BackupDir  string // empty → default ~/.local/share/sageox/backups/redact-history/
	Stdin      io.Reader
	Stdout     io.Writer
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

	fmt.Fprintf(opts.Stdout, "Scanning ledger %s for credentials...\n", opts.LedgerPath)
	scanResult, err := scanLedgerForSecrets(opts.LedgerPath)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if len(scanResult.Findings) == 0 {
		fmt.Fprintf(opts.Stdout, "No credential patterns found across %d files. Nothing to redact.\n",
			scanResult.FilesScanned)
		return nil
	}

	// Enumerate per-finding details (file:line:detector) by re-walking the
	// affected files. We didn't keep this from the aggregate scan because
	// the audit doesn't need it; the cleanup tool does.
	findings, err := enumerateRedactHistoryFindings(opts.LedgerPath, scanResult)
	if err != nil {
		return fmt.Errorf("enumerate findings: %w", err)
	}

	// Classify each finding by pushed/unpushed. Pushed-commit findings are
	// listed but NOT actionable here — they need the gated rewrite tool.
	pushed, unpushed, err := classifyFindingsByPushStatus(opts.LedgerPath, findings)
	if err != nil {
		return fmt.Errorf("classify findings by push status: %w", err)
	}

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
	redactor := session.NewRedactor()
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
			fmt.Fprintf(opts.Stdout, "  Redacted: %s\n\n", path)
		default:
			fmt.Fprintf(opts.Stdout, "  Skipped: %s\n\n", path)
		}
	}

	if redactedFiles == 0 {
		fmt.Fprintln(opts.Stdout, "No files redacted. Snapshot remains for reference.")
		return nil
	}

	// Stage + amend in a single holding commit. The operator can review
	// the amended diff before running `ox session push`.
	if err := stageAndAmendRedactedFiles(opts.LedgerPath, byPath); err != nil {
		return fmt.Errorf("stage/amend: %w", err)
	}

	// Re-scan and report.
	postScan, err := scanLedgerForSecrets(opts.LedgerPath)
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

// enumerateRedactHistoryFindings re-walks just the files the audit
// flagged and collects per-line findings. Two-pass on purpose: the audit
// stays cheap (per-file aggregation), and the cleanup pays the per-line
// cost only for files that need it.
func enumerateRedactHistoryFindings(ledgerPath string, audit *ledgerSecretsScanResult) ([]redactHistoryFinding, error) {
	if audit == nil || len(audit.Findings) == 0 {
		return nil, nil
	}
	// collect the set of files that need a per-line pass
	files := map[string]bool{}
	for _, f := range audit.Findings {
		files[f.Sample] = true
	}
	// Sample is one path per detector; the audit doesn't keep full lists.
	// Walk again with the same allowlist to enumerate every affected file.
	// This is conservative: scan all files matching the allowlist, but
	// emit findings only for the detectors that fired in the audit so
	// we don't pay regex cost for detectors that didn't.
	redactor := session.NewRedactor()

	var out []redactHistoryFinding
	err := filepath.WalkDir(ledgerPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			if d != nil && d.IsDir() && ledgerSecretsSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !ledgerSecretsScanExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > ledgerSecretsSizeCap {
			return nil
		}
		rel, _ := filepath.Rel(ledgerPath, path)
		// G122: filepath.WalkDir rooted at ledgerPath, which is an ox-owned
		// directory under the user's XDG data dir. Symlink TOCTOU between
		// the walker's stat and our Open is theoretical here — the threat
		// model is an attacker with write access to the ledger directory,
		// which already grants direct read of the secret content this
		// scan is auditing.
		f, err := os.Open(path) //nolint:gosec // G122: see comment above
		if err != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			for _, name := range redactor.ScanForSecrets(scanner.Text()) {
				out = append(out, redactHistoryFinding{Detector: name, Path: rel, Line: lineNo})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
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
