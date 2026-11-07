package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/spf13/cobra"
)

var sessionRegenerateCmd = &cobra.Command{
	Use:   "regenerate [session-name]",
	Short: "Regenerate session artifacts or re-redact session data",
	Long: `Regenerate session artifacts from raw data.

By default, regenerates markdown artifacts (summary.md, session.md)
from raw session data.

With --redact, re-applies all current REDACT.md rules (team + repo + user
layers), regenerates all downstream artifacts (session.md, summary.md),
and uploads updated content to LFS.
Old LFS blobs become orphaned after regeneration. Server-side blob purge
will be handled by the /api/v1/git/lfs/purge cloud API endpoint.

The session name supports partial matching (e.g. agent ID suffix).

With --summary, re-generates summary.json using the current prompt template
by invoking Claude. Requires the claude CLI to be installed. Single-session
only (ledgers can contain 10,000+ sessions; each requires an LLM invocation).

Examples:
  ox session regenerate OxK3ZN                          # regenerate artifacts
  ox session regenerate --all                           # regenerate all artifacts
  ox session regenerate OxK3ZN --redact                 # re-redact session
  ox session regenerate --redact --all                  # re-redact all sessions
  ox session regenerate OxK3ZN --redact --dry-run       # preview redaction
  ox session regenerate OxK3ZN --summary                # re-summarize with current prompt`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionRegenerate,
}

func init() {
	sessionRegenerateCmd.Flags().Bool("redact", false, "re-apply current REDACT.md rules to session data")
	sessionRegenerateCmd.Flags().Bool("summary", false, "re-generate summary.json using current prompt template (requires claude CLI)")
	sessionRegenerateCmd.Flags().Bool("all", false, "regenerate all sessions")
	sessionRegenerateCmd.Flags().Bool("dry-run", false, "preview what would change without modifying anything (--redact only)")
	sessionRegenerateCmd.Flags().Bool("force", false, "skip confirmation prompts")
}

func runSessionRegenerate(cmd *cobra.Command, args []string) error {
	redact, _ := cmd.Flags().GetBool("redact")
	summary, _ := cmd.Flags().GetBool("summary")
	regenAll, _ := cmd.Flags().GetBool("all")
	force, _ := cmd.Flags().GetBool("force")

	if summary && redact {
		return fmt.Errorf("--summary and --redact cannot be used together")
	}

	if redact {
		return runSessionRegenerateRedact(cmd, args)
	}

	if summary {
		// Single-session only: ledgers can contain 10,000+ sessions (more for
		// large monorepos). Each summary requires an LLM invocation (~60s + API
		// cost), making batch regeneration prohibitively expensive.
		if len(args) == 0 {
			return fmt.Errorf("specify a session name\nRun 'ox session list' to see available sessions")
		}
		return regenerateSingleSessionSummary(args[0])
	}

	// default mode: artifact regeneration (markdown)
	store, projectRoot, err := newSessionStore()
	if err != nil {
		return err
	}

	if regenAll {
		return regenerateAllSessionsArtifacts(store, projectRoot, force)
	}

	if len(args) == 0 {
		return fmt.Errorf("specify a session name or use --all\nRun 'ox session list' to see available sessions")
	}

	return regenerateSingleSessionArtifacts(store, projectRoot, args[0])
}

// --- Artifact regeneration (default mode) ---

func regenerateSingleSessionArtifacts(store *session.Store, projectRoot, name string) error {
	sessionName, err := store.ResolveSessionName(name)
	if err != nil {
		return fmt.Errorf("resolve session name: %w", err)
	}

	storedSession, err := readSessionViaCache(projectRoot, store, sessionName)
	if err != nil {
		return fmt.Errorf("session %q: %w\nRun 'ox session list' to see available sessions", name, err)
	}

	sourceDir, outDir := artifactDirs(store, sessionName)
	if err := regenerateSessionArtifacts(storedSession, sourceDir, outDir); err != nil {
		return err
	}

	published, err := publishRegeneratedSession(projectRoot, sessionName, outDir)
	if err != nil {
		return fmt.Errorf("regenerated artifacts for %s, but publishing them to the Ledger failed: %w", sessionName, err)
	}

	cli.PrintSuccess(fmt.Sprintf("Regenerated artifacts for %s", sessionName))
	if !published {
		cli.PrintInfo(fmt.Sprintf("%s is not published on the Ledger; the regenerated artifacts stay on this machine", sessionName))
	}
	return nil
}

// artifactDirs returns where a session's regeneration reads summary.json
// (sourceDir) and writes its markdown artifacts (outDir).
//
// A locally-authored session regenerates in its managed-store directory, so an
// in-progress recording's artifacts stay co-located with raw.jsonl. A
// team-synced session only exists in the Ledger: it reads the git-stored
// summary.json there and writes into the Ledger cache, because the tracked
// sessions/<name>/ files must stay LFS pointers (cache-only design).
func artifactDirs(store *session.Store, sessionName string) (sourceDir, outDir string) {
	if store != nil {
		local := store.GetSessionPath(sessionName)
		if _, err := os.Stat(local); err == nil {
			return local, local
		}
	}
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		// last resort — use the local path even if missing; the write fails
		// with a clear error.
		if store != nil {
			local := store.GetSessionPath(sessionName)
			return local, local
		}
		return sessionName, sessionName
	}
	return filepath.Join(ledgerPath, "sessions", sessionName), ledgerSessionCacheDir(ledgerPath, sessionName)
}

// ledgerSessionCacheDir is where a Ledger session's real content lives.
func ledgerSessionCacheDir(ledgerPath, sessionName string) string {
	return filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
}

// readSessionViaCache reads raw.jsonl through the cache-only resolver,
// hydrating on demand. Replaces direct calls to store.ReadSession in code
// paths that need to handle ledger-side LFS-pointer stubs (sessions
// authored by other team members).
//
// store.ReadSession would error with ErrSessionNotHydrated for stubs;
// this helper triggers a Batch-API hydrate-to-cache and returns the
// real bytes from the cache path. The in-place git-tracked file is
// untouched (see openSessionContent for the cache-only invariant).
func readSessionViaCache(projectRoot string, store *session.Store, sessionName string) (*session.StoredSession, error) {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		// Fall back to store.ReadSession when no ledger is available
		// (e.g. tests or detached project state).
		return store.ReadSession(sessionName)
	}
	rawPath, err := openSessionContent(projectRoot, ledgerPath, sessionName, ledgerFileRaw)
	if err != nil {
		return nil, err
	}
	// A cached raw.jsonl can predate a redaction synced from a coworker.
	// Regenerating from it would republish the removed content.
	if stale, err := staleCachedRaw(ledgerPath, sessionName, rawPath); err != nil {
		return nil, err
	} else if stale {
		if err := os.Remove(rawPath); err != nil {
			return nil, fmt.Errorf("drop stale cached %s: %w", ledgerFileRaw, err)
		}
		if rawPath, err = openSessionContent(projectRoot, ledgerPath, sessionName, ledgerFileRaw); err != nil {
			return nil, err
		}
		if stale, err := staleCachedRaw(ledgerPath, sessionName, rawPath); err != nil {
			return nil, err
		} else if stale {
			return nil, fmt.Errorf("cached %s for %s does not match meta.json after rehydrating", ledgerFileRaw, sessionName)
		}
	}
	return session.ReadSessionFromPath(rawPath)
}

// staleCachedRaw reports whether rawPath is a cached raw.jsonl whose bytes no
// longer match the LFS ref meta.json records for it.
func staleCachedRaw(ledgerPath, sessionName, rawPath string) (bool, error) {
	if filepath.Dir(rawPath) != ledgerSessionCacheDir(ledgerPath, sessionName) {
		return false, nil
	}
	meta, err := lfs.ReadSessionMeta(filepath.Join(ledgerPath, "sessions", sessionName))
	if err != nil {
		return false, fmt.Errorf("read meta.json for %s: %w", sessionName, err)
	}
	ref, ok := meta.Files[ledgerFileRaw]
	if !ok || !ref.IsLFS() || ref.OID == "" {
		return false, nil
	}
	data, err := os.ReadFile(rawPath)
	if err != nil {
		return false, fmt.Errorf("read cached %s: %w", ledgerFileRaw, err)
	}
	return lfs.ComputeOID(data) != ref.BareOID(), nil
}

func regenerateAllSessionsArtifacts(store *session.Store, projectRoot string, force bool) error {
	sessions, err := store.ListAllSessions()
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	if !force {
		confirmed, confirmErr := cli.ConfirmYesNoRequired(fmt.Sprintf("Regenerate artifacts for %d session(s)?", len(sessions)), false, false)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Println("Canceled.")
			return nil
		}
	}

	var skipped int
	var regenerated []regeneratedSession
	for _, info := range sessions {
		sessionName := info.SessionName
		if sessionName == "" {
			skipped++
			continue
		}

		storedSession, readErr := readSessionViaCache(projectRoot, store, sessionName)
		if readErr != nil {
			slog.Warn("skipping unreadable session", "session", sessionName, "error", readErr)
			skipped++
			continue
		}

		sourceDir, outDir := artifactDirs(store, sessionName)
		if regenErr := regenerateSessionArtifacts(storedSession, sourceDir, outDir); regenErr != nil {
			slog.Warn("failed to regenerate session", "session", sessionName, "error", regenErr)
			skipped++
			continue
		}

		regenerated = append(regenerated, regeneratedSession{name: sessionName, dir: outDir})
	}

	published, publishErr := publishRegeneratedSessions(projectRoot, regenerated)

	cli.PrintSuccess(fmt.Sprintf("Regenerated %d session(s)", len(regenerated)))
	if skipped > 0 {
		cli.PrintWarning(fmt.Sprintf("Skipped %d session(s) (unreadable or missing raw data)", skipped))
	}
	if published > 0 {
		cli.PrintInfo(fmt.Sprintf("Published %d session(s) to the Ledger", published))
	}
	return publishErr
}

// regeneratedSession is a session whose artifacts were regenerated into dir.
type regeneratedSession struct {
	name, dir string
}

// publishRegeneratedSessions publishes the sessions regenerated by --all in
// one Ledger commit. Sessions not published on the Ledger are skipped; any
// session that cannot be prepared, and a failed commit or push, is an error.
func publishRegeneratedSessions(projectRoot string, sessions []regeneratedSession) (int, error) {
	if len(sessions) == 0 {
		return 0, nil
	}
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		slog.Debug("no Ledger; regenerated artifacts stay local", "error", err)
		return 0, nil
	}

	var names []string
	var errs []error
	for _, s := range sessions {
		ok, prepErr := prepareRegeneratedArtifacts(projectRoot, ledgerPath, s.name, s.dir)
		if prepErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.name, prepErr))
			continue
		}
		if ok {
			names = append(names, s.name)
		}
	}
	if len(names) > 0 {
		if err := commitAndPushLedgerBatch(ledgerPath, fmt.Sprintf("session: regenerate %d sessions", len(names)), names); err != nil {
			return 0, errors.Join(append(errs, fmt.Errorf("publish regenerated sessions to the Ledger: %w", err))...)
		}
	}
	if len(errs) > 0 {
		return len(names), fmt.Errorf("publish regenerated artifacts to the Ledger: %w", errors.Join(errs...))
	}
	return len(names), nil
}

// regenerateSessionArtifacts regenerates markdown artifacts for a session.
func regenerateSessionArtifacts(storedSession *session.StoredSession, sourceDir, outDir string) error {
	if err := regenerateArtifactsInto(sourceDir, outDir, storedSession); err != nil {
		return fmt.Errorf("regenerate artifacts: %w", err)
	}

	slog.Debug("regenerated session artifacts", "path", outDir)
	return nil
}

// publishRegeneratedSession publishes one session's regenerated artifacts to
// the Ledger. It returns false, with no error, when there is nothing on the
// Ledger to update (no Ledger, an unpublished or draft session, or a session
// held on this machine).
func publishRegeneratedSession(projectRoot, sessionName, artifactDir string) (bool, error) {
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		slog.Debug("no Ledger; regenerated artifacts stay local", "session", sessionName, "error", err)
		return false, nil
	}
	ok, err := prepareRegeneratedArtifacts(projectRoot, ledgerPath, sessionName, artifactDir)
	if err != nil || !ok {
		return false, err
	}
	if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
		return false, fmt.Errorf("commit and push: %w", err)
	}
	return true, nil
}

// regeneratedArtifactFiles are the LFS artifacts regenerateArtifacts writes.
var regeneratedArtifactFiles = []string{ledgerFileSessionMD, ledgerFileSummaryMD}

// prepareRegeneratedArtifacts readies a Ledger commit for the artifacts
// regenerated into artifactDir: it uploads them, keeps their bytes in the
// Ledger cache, records the new refs in meta.json, and writes pointers into
// the tracked sessions/<name>/ directory. Real content is never written there,
// so a failed upload or commit cannot leave plain content where a pointer
// belongs. A failed pointer write restores the tracked files and the refs.
//
// Returns false, with no error, when the session has nothing on the Ledger to
// update: no meta.json, a draft placeholder, or a session held on this machine.
func prepareRegeneratedArtifacts(projectRoot, ledgerPath, sessionName, artifactDir string) (bool, error) {
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	meta, err := lfs.ReadSessionMeta(sessionDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read meta.json: %w", err)
	}
	if meta.IsDraft() {
		return false, nil
	}
	if sessionHeldByName(artifactDir) || sessionHeldByName(sessionDir) {
		slog.Info("regenerated artifacts not published; session is held on this machine", "session", sessionName)
		return false, nil
	}

	content := make(map[string][]byte)
	for _, name := range regeneratedArtifactFiles {
		path := filepath.Join(artifactDir, name)
		if lfs.IsPointerFile(path) {
			continue
		}
		data, readErr := os.ReadFile(path)
		if errors.Is(readErr, fs.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return false, fmt.Errorf("read regenerated %s: %w", name, readErr)
		}
		content[name] = data
	}
	if len(content) == 0 {
		return false, nil
	}

	refs, err := uploadSessionLFS(projectRoot, artifactDir)
	if err != nil {
		return false, fmt.Errorf("LFS upload: %w", err)
	}
	uploaded := make(map[string]lfs.FileRef, len(content))
	for name, data := range content {
		ref, ok := refs[name]
		if !ok || ref.OID != lfs.NewFileRef(data).OID {
			return false, fmt.Errorf("LFS upload returned no ref for %s", name)
		}
		uploaded[name] = ref
	}

	cacheDir := ledgerSessionCacheDir(ledgerPath, sessionName)
	if filepath.Clean(artifactDir) != filepath.Clean(cacheDir) {
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			return false, fmt.Errorf("create Ledger cache: %w", err)
		}
		for name, data := range content {
			if err := fileutil.AtomicWriteBytes(filepath.Join(cacheDir, name), data, 0o600); err != nil {
				return false, fmt.Errorf("cache regenerated %s: %w", name, err)
			}
		}
	}

	// prior holds what meta.Files named before this update; a missing entry
	// was absent.
	prior := make(map[string]*lfs.FileRef, len(uploaded))
	if err := lfs.MutateSessionMeta(context.Background(), sessionDir, func(current *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if current == nil {
			return nil, errors.New("session metadata disappeared during regeneration")
		}
		if current.Files == nil {
			current.Files = make(map[string]lfs.FileRef, len(uploaded))
		}
		for name, ref := range uploaded {
			if old, ok := current.Files[name]; ok {
				prior[name] = &old
			} else {
				prior[name] = nil
			}
			current.Files[name] = ref
		}
		return current, nil
	}); err != nil {
		return false, fmt.Errorf("update meta.json with LFS refs: %w", err)
	}
	setAside, err := setAsideCommittedArtifacts(sessionDir, uploaded)
	if err != nil {
		err = fmt.Errorf("replace committed artifacts: %w", err)
		if revertErr := revertRegeneratedRefs(sessionDir, uploaded, prior); revertErr != nil {
			err = errors.Join(err, revertErr)
		}
		return false, err
	}
	// AssertUploaded: uploadSessionLFS uploaded these exact bytes above.
	// WritePointerFiles restores the files it touched when it fails.
	if _, err := lfs.WritePointerFiles(sessionDir, lfs.AssertUploadedManifest(uploaded)); err != nil {
		err = fmt.Errorf("write LFS pointer files: %w", err)
		if restoreErr := setAside.restore(); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
		if revertErr := revertRegeneratedRefs(sessionDir, uploaded, prior); revertErr != nil {
			err = errors.Join(err, revertErr)
		}
		return false, err
	}
	return true, nil
}

// committedArtifacts holds the plain artifacts removed from dir so pointers
// can replace them.
type committedArtifacts struct {
	dir   string
	files map[string]committedArtifact
}

type committedArtifact struct {
	data []byte
	perm os.FileMode
}

// restore writes the removed artifacts back, except where another writer has
// created the file since.
func (c committedArtifacts) restore() error {
	var errs []error
	for name, f := range c.files {
		path := filepath.Join(c.dir, name)
		if _, err := os.Lstat(path); err == nil {
			continue
		}
		if err := fileutil.AtomicWriteBytes(path, f.data, f.perm); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// setAsideCommittedArtifacts removes the plain (non-pointer) artifacts a
// legacy session tracks in git, so their regenerated pointers can replace
// them. Only a file whose worktree and index both match HEAD is removed: git
// keeps that copy. Anything else is left for WritePointerFiles to refuse.
func setAsideCommittedArtifacts(sessionDir string, uploaded map[string]lfs.FileRef) (committedArtifacts, error) {
	setAside := committedArtifacts{dir: sessionDir, files: map[string]committedArtifact{}}
	for name := range uploaded {
		path := filepath.Join(sessionDir, name)
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return setAside, errors.Join(err, setAside.restore())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return setAside, errors.Join(err, setAside.restore())
		}
		if _, _, perr := lfs.ParsePointer(string(data)); perr == nil || !unchangedSinceHEAD(sessionDir, name) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return setAside, errors.Join(err, setAside.restore())
		}
		setAside.files[name] = committedArtifact{data: data, perm: info.Mode().Perm()}
	}
	return setAside, nil
}

// unchangedSinceHEAD reports whether HEAD tracks dir/name and neither the
// index nor the worktree changed it. git hashes the worktree file itself, so
// its filters (such as core.autocrlf) apply and index flags like
// assume-unchanged or skip-worktree cannot hide an edit.
func unchangedSinceHEAD(dir, name string) bool {
	rel := "./" + name
	head, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "-q", "HEAD:"+rel).Output()
	if err != nil {
		return false
	}
	worktree, err := exec.Command("git", "-C", dir, "hash-object", "--path="+name, "--", name).Output()
	if err != nil || !bytes.Equal(bytes.TrimSpace(head), bytes.TrimSpace(worktree)) {
		return false
	}
	return exec.Command("git", "-C", dir, "diff", "--quiet", "--cached", "--", rel).Run() == nil
}

// revertRegeneratedRefs puts back the meta.Files entries prepareRegeneratedArtifacts
// replaced, under the metadata lock, leaving entries another writer changed since.
func revertRegeneratedRefs(sessionDir string, uploaded map[string]lfs.FileRef, prior map[string]*lfs.FileRef) error {
	err := lfs.MutateSessionMeta(context.Background(), sessionDir, func(current *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if current == nil || current.Files == nil {
			return nil, nil
		}
		for name, ref := range uploaded {
			if current.Files[name].OID != ref.OID {
				continue
			}
			if old := prior[name]; old != nil {
				current.Files[name] = *old
			} else {
				delete(current.Files, name)
			}
		}
		return current, nil
	})
	if err != nil {
		return fmt.Errorf("revert meta.json LFS refs: %w", err)
	}
	return nil
}

// hasSubstantiveTurns reports whether the session has at least one user OR
// assistant turn. A session with only header/system entries (e.g., a
// recording that never proceeded past "Loaded coworker: <name>") has nothing
// for the summarizer to work on. Without this preflight the LLM either
// refuses ("there's nothing to summarize"), narrates the absence, or
// fabricates content — all three produce validation-failing output that
// gets persisted as a failure-marker stub on the ledger. Cleaner to fail
// fast with a clear error.
//
// Filed as bd ox-o45g.
func hasSubstantiveTurns(entries []map[string]any) bool {
	for _, e := range entries {
		t, _ := e["type"].(string)
		if t == "user" || t == "assistant" {
			return true
		}
	}
	return false
}

// --- Summary regeneration (--summary) ---

// newRegenerateSummaryRunner is the summary worker for --summary; tests swap it.
var newRegenerateSummaryRunner = func() interface {
	agentwork.Runner
	Available() bool
} {
	return agentwork.NewClaudeRunner(slog.Default())
}

// regenerateSingleSessionSummary re-generates summary.json for a session with
// an isolated Claude Code run over the inline summary prompt. The raw.jsonl is
// read (downloaded from LFS if needed) and ox publishes the parsed summary. Downstream artifacts
// (summary.md, session.md) are regenerated from the result.
func regenerateSingleSessionSummary(nameArg string) error {
	projectRoot, err := requireProjectRoot()
	if err != nil {
		return err
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return err
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")
	sessionName, err := resolveSessionInDir(sessionsDir, nameArg)
	if err != nil {
		return err
	}

	sessionPath := filepath.Join(sessionsDir, sessionName)

	// Cache-only resolution — see openSessionContent for the load-bearing invariant.
	rawPath, err := openSessionContent(projectRoot, ledgerPath, sessionName, ledgerFileRaw)
	if err != nil {
		return err
	}

	rawSession, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		return fmt.Errorf("read raw.jsonl: %w", err)
	}

	if !hasSubstantiveTurns(rawSession.Entries) {
		// ox-o45g: skip sessions with no user/assistant turns. The LLM has
		// nothing to summarize and would either refuse, narrate, or generate
		// fabricated content. Surface as a clean exit, not an LLM call.
		return fmt.Errorf("session %s has no substantive user/assistant turns; nothing to summarize", sessionName)
	}

	// The transcript is untrusted text, so the summary is generated the way the
	// daemon and import do it: the content goes inline, the worker runs
	// isolated with no tools in an empty directory, and ox parses and publishes
	// the JSON itself. A Claude Code too old to isolate fails here; a person is
	// at the terminal to see it.
	entries := sessionsummary.EntriesFromRaw(rawSession.Entries)
	prompt := sessionsummary.BuildInlineSummaryPrompt(sessionsummary.TrimEntriesForBudget(entries, sessionsummary.ImportPromptBudget))

	cli.PrintInfo(fmt.Sprintf("Generating summary for %s...", sessionName))

	runner := newRegenerateSummaryRunner()
	if !runner.Available() {
		return fmt.Errorf("claude binary not found in PATH — ensure Claude Code CLI is installed")
	}
	// No WorkDir: the runner makes a fresh empty one under the user's cache,
	// never the project, whose settings (including env overrides like
	// ANTHROPIC_BASE_URL) Claude Code would load.
	result, err := runner.Run(context.Background(), agentwork.RunRequest{
		Prompt:   prompt,
		Model:    sessionsummary.DefaultSummaryModel(),
		Isolated: true,
	})
	if err != nil {
		return fmt.Errorf("claude invocation: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("claude exited with code %d%s", result.ExitCode, cliMessage(result.Output))
	}
	freshSummary, err := sessionsummary.ParseAndValidate(result.Output, len(entries))
	if err != nil {
		return fmt.Errorf("summary for %s rejected: %w", sessionName, err)
	}

	// quality_score is omitempty, so a model's 0 would vanish and
	// pushSummaryToLedger would read the summary as unscored and publish it.
	// A model's answer is always scored, as in the daemon's finalize.
	fields := map[string]any{}
	if data, err := json.Marshal(freshSummary); err == nil {
		_ = json.Unmarshal(data, &fields)
	}
	fields["quality_score"] = freshSummary.QualityScore
	data, _ := json.Marshal(fields)
	summaryFile, err := os.CreateTemp("", "ox-regenerate-summary-*.json")
	if err != nil {
		return fmt.Errorf("summary temp file: %w", err)
	}
	defer func() { _ = os.Remove(summaryFile.Name()) }()
	_, writeErr := summaryFile.Write(data)
	if err := errors.Join(writeErr, summaryFile.Close()); err != nil {
		return fmt.Errorf("write summary temp file: %w", err)
	}
	pushed := pushSummaryToLedger(summaryFile.Name(), sessionPath)
	if !pushed.Success {
		return fmt.Errorf("push summary for %s: %s", sessionName, pushed.Error)
	}
	// A discarded or draft session succeeds without writing summary.json;
	// the artifacts below would then be rebuilt from the old summary.
	if pushed.SummaryPath == "" {
		return fmt.Errorf("summary for %s not saved: %s", sessionName, pushed.Message)
	}

	// regenerate downstream artifacts (summary.md, session.md) into the
	// Ledger cache; the tracked files must stay pointers.
	cacheDir := ledgerSessionCacheDir(ledgerPath, sessionName)
	if err := regenerateArtifactsInto(sessionPath, cacheDir, rawSession); err != nil {
		slog.Warn("artifact regeneration partially failed", "session", sessionName, "error", err)
	}

	// push-summary already ran git add/commit/push for summary.json +
	// meta.json, but not for the regenerated .md files. Publish them too.
	if ok, err := prepareRegeneratedArtifacts(projectRoot, ledgerPath, sessionName, cacheDir); err != nil {
		return fmt.Errorf("summary for %s was pushed, but publishing its regenerated artifacts failed: %w", sessionName, err)
	} else if ok {
		if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
			return fmt.Errorf("summary for %s was pushed, but publishing its regenerated artifacts failed: commit and push: %w", sessionName, err)
		}
	}

	cli.PrintSuccess(fmt.Sprintf("Regenerated summary for %s (quality: %.2f)", sessionName, freshSummary.QualityScore))
	return nil
}

// --- Redaction mode (--redact) ---

type regenerateResult struct {
	SessionName     string
	EntriesRedacted int
	PatternsFound   []string
	Skipped         bool
}

func runSessionRegenerateRedact(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")

	if len(args) > 0 && all {
		return fmt.Errorf("cannot use both a session name and --all")
	}
	if len(args) == 0 && !all {
		return fmt.Errorf("provide a session name or use --all")
	}

	projectRoot, err := requireProjectRoot()
	if err != nil {
		return err
	}

	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return err
	}

	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// build redactor with all 3 REDACT.md layers
	redactor, parseErrs := session.NewRedactorWithCustomRules(projectRoot)
	if len(parseErrs) > 0 {
		for _, pe := range parseErrs {
			fmt.Fprintf(os.Stderr, "warning: REDACT.md parse error in %s line %d: %s\n", pe.Path, pe.Line, pe.Message)
		}
	}

	// confirm before proceeding (skip for dry-run since it's read-only)
	if !dryRun && !force {
		var prompt string
		if all {
			prompt = "This will re-redact ALL sessions in the ledger and re-upload to LFS. Continue?"
		} else {
			prompt = fmt.Sprintf("This will re-redact session %q and re-upload to LFS. Continue?", args[0])
		}
		confirmed, confirmErr := cli.ConfirmYesNoRequired(prompt, false, false)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Println("Canceled.")
			return nil
		}
	}

	if all {
		return regenerateAllSessionsRedact(projectRoot, ledgerPath, sessionsDir, redactor, dryRun)
	}

	result, err := regenerateSessionRedact(projectRoot, ledgerPath, sessionsDir, args[0], redactor, dryRun, false)
	if err != nil {
		return err
	}

	if dryRun {
		if result.EntriesRedacted == 0 {
			fmt.Printf("Dry run: %s — no secrets found with current rules\n", result.SessionName)
		} else {
			fmt.Printf("Dry run: %s — %d entries would be redacted (patterns: %s)\n",
				result.SessionName, result.EntriesRedacted, strings.Join(result.PatternsFound, ", "))
		}
		return nil
	}

	if result.Skipped {
		fmt.Printf("%s: no changes needed\n", result.SessionName)
	} else {
		fmt.Printf("%s: %d entries redacted, artifacts regenerated\n",
			result.SessionName, result.EntriesRedacted)
	}
	return nil
}

func regenerateAllSessionsRedact(projectRoot, ledgerPath, sessionsDir string, redactor *session.Redactor, dryRun bool) error {
	dirEntries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return fmt.Errorf("read sessions directory: %w", err)
	}

	// collect session dirs that have meta.json
	var sessionNames []string
	for _, e := range dirEntries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(sessionsDir, e.Name(), "meta.json")
		if _, err := os.Stat(metaPath); err != nil {
			continue
		}
		sessionNames = append(sessionNames, e.Name())
	}

	if len(sessionNames) == 0 {
		fmt.Println("No sessions found in ledger")
		return nil
	}

	fmt.Fprintf(os.Stderr, "Re-redacting all sessions (%d found)...\n", len(sessionNames))

	var (
		processed     int
		redactedCount int
		failedCount   int
		modifiedNames []string
	)

	for i, name := range sessionNames {
		result, err := regenerateSessionRedact(projectRoot, ledgerPath, sessionsDir, name, redactor, dryRun, true)
		processed++

		if err != nil {
			failedCount++
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s: error: %s\n", i+1, len(sessionNames), name, err)
			continue
		}

		if dryRun {
			if result.EntriesRedacted > 0 {
				fmt.Fprintf(os.Stderr, "  [%d/%d] %s: %d entries would be redacted\n",
					i+1, len(sessionNames), name, result.EntriesRedacted)
				redactedCount++
			} else {
				fmt.Fprintf(os.Stderr, "  [%d/%d] %s: no changes\n", i+1, len(sessionNames), name)
			}
			continue
		}

		if !result.Skipped {
			redactedCount++
			modifiedNames = append(modifiedNames, name)
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s: %d entries redacted\n",
				i+1, len(sessionNames), name, result.EntriesRedacted)
		} else {
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s: no changes\n", i+1, len(sessionNames), name)
		}
	}

	if dryRun {
		fmt.Printf("\nDry run summary: %d sessions scanned, %d would be affected, %d failed\n",
			processed, redactedCount, failedCount)
		return nil
	}

	// batch commit + push for all modified sessions
	if len(modifiedNames) > 0 {
		fmt.Fprintf(os.Stderr, "\nCommitting and pushing %d updated sessions...\n", len(modifiedNames))
		if err := commitAndPushLedgerBatch(ledgerPath, fmt.Sprintf("session: re-redact %d sessions", len(modifiedNames)), modifiedNames); err != nil {
			return fmt.Errorf("commit/push failed: %w", err)
		}
	}

	fmt.Printf("\nSummary: %d processed, %d redacted, %d failed\n",
		processed, redactedCount, failedCount)
	return nil
}

// regenerateSessionRedact re-redacts a single session and optionally uploads/commits.
// When batchMode is true, skips per-session commit/push (caller handles batch commit).
func regenerateSessionRedact(projectRoot, ledgerPath, sessionsDir, nameArg string, redactor *session.Redactor, dryRun, batchMode bool) (*regenerateResult, error) {
	sessionName, err := resolveSessionInDir(sessionsDir, nameArg)
	if err != nil {
		return nil, err
	}

	sessionPath := filepath.Join(sessionsDir, sessionName)
	result := &regenerateResult{SessionName: sessionName}

	// read meta.json
	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("read meta.json for %s: %w", sessionName, err)
	}

	// ensure raw.jsonl is hydrated locally.
	//
	// Note: --redact MUST hydrate in-place (unlike --summary which uses the
	// cache resolver). Redaction is a controlled overwrite-and-reupload
	// cycle: we replace the in-place file with redacted bytes, compute a
	// new OID, re-upload the new content to LFS, then commit a fresh
	// pointer with the new OID. Reading from the cache wouldn't work — the
	// downstream commit needs to write a NEW pointer to the in-place path.
	rawPath := filepath.Join(sessionPath, ledgerFileRaw)
	needHydrate := false
	if _, err := os.Stat(rawPath); err != nil {
		needHydrate = true
	} else if lfs.IsPointerFile(rawPath) {
		needHydrate = true
	}
	if needHydrate {
		if err := downloadFileFromLFS(projectRoot, sessionPath, meta, ledgerFileRaw); err != nil {
			return nil, fmt.Errorf("download %s for %s: %w", ledgerFileRaw, sessionName, err)
		}
	}

	// read raw.jsonl entries as maps (preserves original JSONL structure)
	rawSession, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		return nil, fmt.Errorf("read %s for %s: %w", ledgerFileRaw, sessionName, err)
	}

	if len(rawSession.Entries) == 0 {
		result.Skipped = true
		return result, nil
	}

	// scan or redact entries
	entriesRedacted := 0
	patternsFound := make(map[string]bool)

	for i := range rawSession.Entries {
		if dryRun {
			entryHit := false
			for _, value := range rawSession.Entries[i] {
				if s, ok := value.(string); ok && s != "" {
					found := redactor.ScanForSecrets(s)
					if len(found) > 0 {
						entryHit = true
						for _, p := range found {
							patternsFound[p] = true
						}
					}
				}
			}
			if entryHit {
				entriesRedacted++
			}
		} else {
			if redactor.RedactMap(rawSession.Entries[i]) {
				entriesRedacted++
			}
		}
	}

	result.EntriesRedacted = entriesRedacted
	for p := range patternsFound {
		result.PatternsFound = append(result.PatternsFound, p)
	}

	if dryRun {
		return result, nil
	}

	// redact summary.json if it exists (before early return so summary-only secrets are caught)
	summaryRedacted := false
	summaryPath := filepath.Join(sessionPath, "summary.json")
	if _, err := os.Stat(summaryPath); err == nil {
		var sumErr error
		summaryRedacted, sumErr = redactSummaryJSON(summaryPath, redactor)
		if sumErr != nil {
			slog.Warn("summary.json redaction failed", "session", sessionName, "error", sumErr)
		}
		// Refresh manifest entry: redaction can change summary.json's size,
		// so meta.Files[summary.json].Size would otherwise drift. Also lifts
		// the entry to Storage=git for legacy meta.json that predates the
		// manifest refactor.
		if summaryRedacted {
			if info, statErr := os.Stat(summaryPath); statErr == nil {
				_ = backfillGitArtifactInMeta(sessionPath, "summary.json", info.Size())
			}
		}
	}

	if entriesRedacted == 0 && !summaryRedacted {
		result.Skipped = true
		return result, nil
	}

	// re-write raw.jsonl with redacted content
	if entriesRedacted > 0 {
		if err := rewriteRawJSONL(rawPath, rawSession); err != nil {
			return nil, fmt.Errorf("rewrite raw.jsonl for %s: %w", sessionName, err)
		}
	}

	// regenerate downstream artifacts
	if err := regenerateArtifacts(sessionPath, rawSession); err != nil {
		slog.Warn("artifact regeneration partially failed", "session", sessionName, "error", err)
	}

	// upload all content files to LFS
	fileRefs, err := uploadSessionLFS(projectRoot, sessionPath)
	if err != nil {
		return nil, fmt.Errorf("LFS upload for %s: %w", sessionName, err)
	}

	// update meta.json with new OIDs (old blobs orphaned; purge via /api/v1/git/lfs/purge)
	meta.Files = fileRefs
	if err := lfs.WriteSessionMeta(sessionPath, meta); err != nil {
		return nil, fmt.Errorf("write meta.json for %s: %w", sessionName, err)
	}

	// single-session mode: commit + push immediately
	if !batchMode {
		if err := commitAndPushLedger(ledgerPath, sessionName); err != nil {
			return nil, fmt.Errorf("commit/push for %s: %w", sessionName, err)
		}
	}

	return result, nil
}

// --- Shared helpers ---

// rewriteRawJSONL writes the modified StoredSession back to raw.jsonl
// atomically via the session.RawWriter chokepoint (ox-h20u). Atomicity
// is preserved by writing to a temp file via the chokepoint, then
// renaming. The chokepoint guarantees the three-layer redaction stack
// runs on every line — entries reaching here have typically already
// been redacted upstream, but the writer is the load-bearing guarantee.
func rewriteRawJSONL(path string, sess *session.StoredSession) error {
	tmpPath := path + ".tmp"
	rw, err := session.NewRawWriterTruncate(tmpPath, "")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	cleanup := func() {
		_ = rw.Close()
		os.Remove(tmpPath) // no-op if already renamed
	}

	// write header if present (StoreMeta serialized as JSON; RawWriter's
	// WriteRaw applies the same redaction sweep, idempotent on already-
	// safe metadata fields)
	if sess.Meta != nil {
		metaBytes, err := json.Marshal(sess.Meta)
		if err != nil {
			cleanup()
			return fmt.Errorf("marshal header metadata: %w", err)
		}
		var metaMap map[string]any
		if err := json.Unmarshal(metaBytes, &metaMap); err != nil {
			cleanup()
			return fmt.Errorf("unmarshal header metadata: %w", err)
		}
		if err := rw.WriteRaw(map[string]any{
			"type":     "header",
			"metadata": metaMap,
		}); err != nil {
			cleanup()
			return fmt.Errorf("write header: %w", err)
		}
	}

	// write entries via the chokepoint
	for _, entry := range sess.Entries {
		if err := rw.WriteRaw(entry); err != nil {
			cleanup()
			return fmt.Errorf("write entry: %w", err)
		}
	}

	// write footer if present
	if sess.Footer != nil {
		footerBytes, err := json.Marshal(sess.Footer)
		if err != nil {
			cleanup()
			return fmt.Errorf("marshal footer: %w", err)
		}
		var footerMap map[string]any
		if err := json.Unmarshal(footerBytes, &footerMap); err != nil {
			cleanup()
			return fmt.Errorf("unmarshal footer: %w", err)
		}
		if err := rw.WriteRaw(footerMap); err != nil {
			cleanup()
			return fmt.Errorf("write footer: %w", err)
		}
	}

	if err := rw.CloseAndSync(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close: %w", err)
	}
	return os.Rename(tmpPath, path)
}

// regenerateArtifacts regenerates session.md and summary.md from raw session data.
func regenerateArtifacts(sessionPath string, rawSession *session.StoredSession) error {
	return regenerateArtifactsInto(sessionPath, sessionPath, rawSession)
}

// regenerateArtifactsInto regenerates session.md and summary.md into outDir,
// reading summary.json from sourceDir.
func regenerateArtifactsInto(sourceDir, outDir string, rawSession *session.StoredSession) error {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", outDir, err)
	}
	var errs []string

	// session.md
	mdGen := session.NewMarkdownGenerator()
	mdPath := filepath.Join(outDir, ledgerFileSessionMD)
	if err := mdGen.GenerateToFile(rawSession, mdPath); err != nil {
		errs = append(errs, fmt.Sprintf("%s: %s", ledgerFileSessionMD, err))
	}

	// summary.md — regenerate from summary.json if available
	summaryJSONPath := filepath.Join(sourceDir, "summary.json")
	if data, err := os.ReadFile(summaryJSONPath); err == nil {
		var summaryResp session.SummarizeResponse
		if json.Unmarshal(data, &summaryResp) == nil {
			summaryView := session.SummarizeResponseToSummaryView(&summaryResp)
			summaryMdGen := session.NewSummaryMarkdownGenerator()
			summaryMdBytes, err := summaryMdGen.Generate(rawSession.Meta, summaryView, rawSession.Entries)
			if err == nil {
				summaryMdPath := filepath.Join(outDir, ledgerFileSummaryMD)
				if writeErr := os.WriteFile(summaryMdPath, summaryMdBytes, 0644); writeErr != nil {
					errs = append(errs, fmt.Sprintf("%s: %s", ledgerFileSummaryMD, writeErr))
				}
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("partial failures: %s", strings.Join(errs, "; "))
	}
	return nil
}

// mapEntriesToTyped converts map-based entries to typed Entry structs
// for use with event extraction and other typed APIs.
func mapEntriesToTyped(mapEntries []map[string]any) []session.Entry {
	entries := make([]session.Entry, 0, len(mapEntries))
	for _, m := range mapEntries {
		var entry session.Entry

		if t, ok := m["type"].(string); ok {
			entry.Type = session.SessionEntryType(t)
		}
		if c, ok := m["content"].(string); ok {
			entry.Content = c
		}
		if tn, ok := m["tool_name"].(string); ok {
			entry.ToolName = tn
		}
		if ti, ok := m["tool_input"].(string); ok {
			entry.ToolInput = ti
		}
		if to, ok := m["tool_output"].(string); ok {
			entry.ToolOutput = to
		}
		if id, ok := m["call_id"].(string); ok {
			entry.CallID = id
		}
		if isError, ok := m["is_error"].(bool); ok {
			entry.IsError = isError
		}

		entries = append(entries, entry)
	}
	return entries
}

// downloadFileFromLFS downloads a single file from LFS by its OID in the meta manifest.
func downloadFileFromLFS(projectRoot, sessionPath string, meta *lfs.SessionMeta, filename string) error {
	ref, ok := meta.Files[filename]
	if !ok {
		return fmt.Errorf("%s not found in session manifest", filename)
	}

	client, err := getLFSClient(projectRoot)
	if err != nil {
		return hydrateHint(err)
	}

	bareOID := ref.BareOID()
	resp, err := client.BatchDownload([]lfs.BatchObject{{OID: bareOID, Size: ref.Size}})
	if err != nil {
		return hydrateHint(err)
	}

	results := lfs.DownloadAll(resp, 1)
	if len(results) == 0 {
		return fmt.Errorf("no download results for %s", filename)
	}

	r := results[0]
	if r.Error != nil {
		return r.Error
	}

	// verify integrity
	computedOID := lfs.ComputeOID(r.Content)
	if computedOID != r.OID {
		return fmt.Errorf("SHA256 mismatch for %s: expected %s got %s", filename, r.OID, computedOID)
	}

	filePath := filepath.Join(sessionPath, filename)
	return os.WriteFile(filePath, r.Content, 0644)
}

// redactSummaryJSON reads summary.json, redacts text fields, and re-writes it.
// Returns true if any changes were made.
func redactSummaryJSON(path string, redactor *session.Redactor) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	var summary session.SummarizeResponse
	if err := json.Unmarshal(data, &summary); err != nil {
		return false, fmt.Errorf("parse summary.json: %w", err)
	}

	changed := false

	if out, found := redactor.RedactString(summary.Title); len(found) > 0 {
		summary.Title = out
		changed = true
	}
	if out, found := redactor.RedactString(summary.Summary); len(found) > 0 {
		summary.Summary = out
		changed = true
	}
	if out, found := redactor.RedactString(summary.Outcome); len(found) > 0 {
		summary.Outcome = out
		changed = true
	}
	if out, found := redactor.RedactString(summary.FinalPlan); len(found) > 0 {
		summary.FinalPlan = out
		changed = true
	}

	for i, action := range summary.KeyActions {
		if out, found := redactor.RedactString(action); len(found) > 0 {
			summary.KeyActions[i] = out
			changed = true
		}
	}
	for i, topic := range summary.TopicsFound {
		if out, found := redactor.RedactString(topic); len(found) > 0 {
			summary.TopicsFound[i] = out
			changed = true
		}
	}
	for i := range summary.AhaMoments {
		if out, found := redactor.RedactString(summary.AhaMoments[i].Highlight); len(found) > 0 {
			summary.AhaMoments[i].Highlight = out
			changed = true
		}
		if out, found := redactor.RedactString(summary.AhaMoments[i].Why); len(found) > 0 {
			summary.AhaMoments[i].Why = out
			changed = true
		}
	}
	for i, diagram := range summary.Diagrams {
		if out, found := redactor.RedactString(diagram); len(found) > 0 {
			summary.Diagrams[i] = out
			changed = true
		}
	}
	for i, title := range summary.ChapterTitles {
		if out, found := redactor.RedactString(title); len(found) > 0 {
			summary.ChapterTitles[i] = out
			changed = true
		}
	}
	for i := range summary.SageoxInsights {
		if out, found := redactor.RedactString(summary.SageoxInsights[i].Topic); len(found) > 0 {
			summary.SageoxInsights[i].Topic = out
			changed = true
		}
		if out, found := redactor.RedactString(summary.SageoxInsights[i].Insight); len(found) > 0 {
			summary.SageoxInsights[i].Insight = out
			changed = true
		}
		if out, found := redactor.RedactString(summary.SageoxInsights[i].Impact); len(found) > 0 {
			summary.SageoxInsights[i].Impact = out
			changed = true
		}
	}

	if !changed {
		return false, nil
	}

	outData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal summary.json: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, outData, 0644); err != nil {
		return false, err
	}
	return true, os.Rename(tmpPath, path)
}

// commitAndPushLedgerBatch commits the named sessions in one commit and pushes.
//
// Staging strategy mirrors commitAndPushLedger: derive the per-session
// artifact list from meta.Files (manifest-driven), with the historical
// glob as fallback for legacy sessions or torn writes. summary.json is
// staged automatically when meta.Files records it (Storage=git).
//
// Like commitAndPushLedger it always pushes: an identical retry has no new
// commit but may still owe the remote one from an earlier failed push.
func commitAndPushLedgerBatch(ledgerPath, message string, sessionNames []string) error {
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	var filesToAdd []string
	for _, name := range sessionNames {
		sessionDir := filepath.Join(sessionsDir, name)
		filesToAdd = append(filesToAdd, filepath.Join(sessionDir, "meta.json"))
		filesToAdd = append(filesToAdd, sessionArtifactsToStage(sessionDir)...)
	}
	filesToAdd = append(filesToAdd, filepath.Join(sessionsDir, ".gitignore"))

	err := gitutil.WithRepoLock(context.Background(), ledgerPath, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := gitutil.IsSafeForGitOps(ledgerPath); err != nil {
			return err
		}
		if unmerged, err := gitutil.HasUnmergedEntries(ctx, ledgerPath); err != nil {
			return err
		} else if unmerged {
			return fmt.Errorf("cannot commit Ledger with unresolved conflicts (run 'ox doctor --fix')")
		}
		priorIndex, err := ledgerIndexEntries(ledgerPath, filesToAdd)
		if err != nil {
			return err
		}
		if _, err := gitutil.RunGit(ctx, ledgerPath, append([]string{"add", "--sparse", "--"}, filesToAdd...)...); err != nil {
			restoreLedgerIndex(ledgerPath, filesToAdd, priorIndex)
			return fmt.Errorf("git add: %w", err)
		}
		// Keep other staged sessions, but leave plans for their own save.
		// A large snapshot can take longer than the staging timeout to validate.
		if _, err := commitLedgerSnapshot(context.WithoutCancel(ctx), ledgerPath, message); err != nil {
			restoreLedgerIndex(ledgerPath, filesToAdd, priorIndex)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	// pushLedger acquires the same lock, so push only after releasing it.
	return pushLedger(context.Background(), ledgerPath)
}
