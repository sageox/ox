package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/sageox/ox/pkg/sessionsummary"
)

const (
	importDefaultParallel = 3

	// importPushBatch bounds how many committed imports wait for one push. Each
	// push may pull, rebase and autostash; batching keeps a bulk run to a few.
	importPushBatch = 10

	// importInterruptedPushTimeout bounds the push that still publishes the
	// already committed sessions after an interrupt.
	importInterruptedPushTimeout = 2 * time.Minute

	importReadTimeout     = 2 * time.Minute
	importSummaryTimeout  = 5 * time.Minute
	importSummaryAttempts = 3
)

// importArtifacts is the only set of files an imported session may bring into
// the Ledger. Anything else found in staging (a journal, a lock, a marker)
// refuses the rename, so machine-local state never reaches the shared Ledger.
var importArtifacts = map[string]bool{
	"meta.json":                  true,
	"summary.json":               true,
	pipeline.LedgerFileRaw:       true,
	pipeline.LedgerFileSummaryMD: true,
	pipeline.LedgerFileSessionMD: true,
}

// errImportHeld marks a session that was not uploaded and must be retried
// with the command in its report. Nothing from it reached the Ledger.
var errImportHeld = errors.New("held")

// importDeps are the seams a test replaces. Production wires the real
// adapters, isolated vendor runners, LFS client, push and notify.
type importDeps struct {
	readNative  func(agent nativeimport.Agent, path string) ([]adapters.RawEntry, error)
	runner      func(agent nativeimport.Agent) agentwork.Runner
	lfsClient   func() (*lfs.Client, error)
	push        func(ctx context.Context, ledgerPath string) error
	notify      func(meta *lfs.SessionMeta, name string)
	urlFor      func(sessionID string) string
	now         func() time.Time
	usable      func(agent nativeimport.Agent) bool // installed and logged in
	syncLedger  func()                              // best-effort fresh pull
	interactive func() bool                         // a coworker can answer a prompt
	confirm     func(prompt string) (bool, error)
	review      func(context.Context, importDestination, []*importCandidate, importPreviewLoader, bool) (importReviewResult, error)
}

// importEnv is one run's fixed context.
type importEnv struct {
	projectRoot string
	ledgerPath  string
	repoID      string
	endpoint    string
	username    string
	stagingRoot string
	summarizer  nativeimport.Agent // "" means each session's own vendor
	pushBatch   int                // commits per push; 0 means importPushBatch
	progress    io.Writer          // per-session progress while stdout carries JSON; nil discards
	deps        importDeps
	logger      *slog.Logger
	lfs         *lfs.Client
	verdicts    *importVerdicts
}

// readNativeWithAdapter reads the exact discovered file through the session
// adapter, never through a by-ID lookup that could return another session.
func readNativeWithAdapter(agent nativeimport.Agent, path string) ([]adapters.RawEntry, error) {
	adapter, err := adapters.GetAdapter(adapterNameFor(agent))
	if err != nil {
		return nil, err
	}
	if timed, ok := adapter.(interface {
		ReadWithTimeout(string, time.Duration) ([]adapters.RawEntry, error)
	}); ok {
		return timed.ReadWithTimeout(path, importReadTimeout)
	}
	return adapter.Read(path)
}

// preparedImport owns a session's staging directory until the committer has
// published it or decided to hold it. Workers never write to sessions/.
type preparedImport struct {
	staging string
}

// cleanup releases staging after the committer publishes or holds the session.
func (p *preparedImport) cleanup() { _ = os.RemoveAll(p.staging) }

// prepareImport does the slow independent work. On success the caller owns
// staging; every other exit removes it, leaving nothing to publish later.
func prepareImport(ctx context.Context, env *importEnv, c *importCandidate) (prepared *preparedImport, skip string, err error) {
	s := c.Session
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if reason := justInTimeCheck(env, c); reason != "" {
		return nil, reason, nil
	}
	staging := filepath.Join(env.stagingRoot, c.Name)
	if err := os.RemoveAll(staging); err != nil {
		return nil, "", heldf("prepare staging: %v", err)
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, "", heldf("prepare staging: %v", err)
	}
	defer func() {
		if prepared == nil {
			_ = os.RemoveAll(staging)
		}
	}()

	raw, err := env.deps.readNative(s.Agent, s.Path)
	if err != nil {
		if errors.Is(err, adapters.ErrAdapterOutputLimit) {
			return nil, "", heldf("too large to import: converted output exceeds the adapter's limit")
		}
		return nil, "", heldf("read %s session: %v", s.Agent, err)
	}
	header := nativeimport.RawHeader{
		SessionID: c.SessionID, AgentType: adapterNameFor(s.Agent), RepoID: env.repoID,
		Username: env.username, NativeID: s.NativeID, StartedAt: s.StartedAt, StoppedAt: s.LastActivity,
	}
	rawPath := filepath.Join(staging, pipeline.LedgerFileRaw)
	entryCount, err := nativeimport.WriteRaw(rawPath, env.projectRoot, header, raw)
	if err != nil {
		return nil, "", heldf("convert: %v", err)
	}
	if !session.HasSubstantiveEntries(rawPath) {
		return nil, "no conversation after conversion", nil
	}
	stored, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		return nil, "", heldf("read converted session: %v", err)
	}

	summary, err := summarizeImport(ctx, env, c, stored)
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	// Summarizing can take minutes; a session resumed meanwhile is not final.
	if reason := justInTimeCheck(env, c); reason != "" {
		return nil, reason, nil
	}
	if verdict := importVerdictFor(summary); verdict != "" {
		return nil, rememberVerdict(env, c, verdict, summary.ScoreReason), nil
	}
	// A meta with no title is exactly what the daemon's repair paths rewrite,
	// leaving an uncommitted edit that wedges the next pull (#1105).
	if strings.TrimSpace(summary.Title) == "" {
		return nil, "", heldf("the summary has no title")
	}
	if _, err := session.WriteSessionArtifacts(staging, stored, summary); err != nil {
		return nil, "", heldf("write summary: %v", err)
	}
	err = lfs.MutateSessionMeta(ctx, staging, func(*lfs.SessionMeta) (*lfs.SessionMeta, error) {
		meta := sessionMetaBase(c.Name, env.username, "", header.AgentType, s.StartedAt, env.projectRoot, c.SessionID).
			Title(summary.Title).
			Summary(summary.Summary).
			EntryCount(entryCount).
			NativeSessions(header.NativeSessions()).
			StoppedAt(s.LastActivity).
			Build()
		// Written once, before the first commit, and never rewritten: no retry
		// bookkeeping lives here, so no daemon repair path ever matches (#1105).
		meta.SummaryStatus = sessionsummary.SummaryStatusOK
		return meta, nil
	})
	if err != nil {
		return nil, "", heldf("write meta.json: %v", err)
	}
	return &preparedImport{staging: staging}, "", nil
}

// publishPreparedImport is owned by the single committer: it checks again
// after any wait in the pool, scans, uploads and commits under the repo lock.
func publishPreparedImport(ctx context.Context, env *importEnv, c *importCandidate, prepared *preparedImport) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if reason := justInTimeCheck(env, c); reason != "" {
		return reason, nil
	}
	staging := prepared.staging
	var meta *lfs.SessionMeta

	if file, err := residualSecret(env.projectRoot, staging); err != nil {
		return "", heldf("scan: %v", err)
	} else if file != "" {
		return "", heldf("a possible secret remains in %s after redaction; add a REDACT.md rule or redact the native session, then retry", file)
	}

	// Before anything leaves the machine: a pull during the summary may have
	// brought another machine's import of this session, or left the clone
	// mid-rebase. commitImport checks both again, under the clone lock.
	if _, err := gitutil.RunGit(ctx, env.ledgerPath, "cat-file", "-e", "HEAD:sessions/"+c.Name); err == nil {
		return "imported from another machine meanwhile", nil
	}
	if err := prepareDraftLedgerWrite(env.ledgerPath, c.Name); err != nil {
		return "", heldf("the Ledger is not safe to write: %v", err)
	}
	refs, err := lfs.UploadSessionFilesContext(ctx, env.lfs, staging, env.logger)
	if err != nil {
		return "", heldf("upload to LFS: %v", err)
	}
	files := map[string]lfs.FileRef{}
	for name, ref := range refs {
		files[name] = ref
	}
	if info, err := os.Stat(filepath.Join(staging, "summary.json")); err == nil {
		files["summary.json"] = lfs.NewGitFileRef(info.Size())
	}
	err = lfs.MutateSessionMeta(ctx, staging, func(cur *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if cur == nil {
			return nil, fmt.Errorf("meta.json disappeared from staging")
		}
		cur.Files = files
		meta = cur
		return cur, nil
	})
	if err != nil {
		return "", heldf("record LFS files: %v", err)
	}
	if _, err := lfs.WritePointerFiles(staging, lfs.AssertUploadedManifest(refs)); err != nil {
		return "", heldf("write pointers: %v", err)
	}
	if err := checkImportStaging(staging, refs); err != nil {
		return "", heldf("%v", err)
	}
	if reason, err := commitImport(ctx, env, c, staging); err != nil || reason != "" {
		return reason, err
	}
	c.meta = meta
	return "", nil
}

func heldf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errImportHeld, fmt.Sprintf(format, args...))
}

// importVerdictFor applies live recording's routing to the summarizer's
// verdict: a "skip" session is discarded there and a "local_only" one never
// leaves the machine, so the import leaves both where they are. The brief-
// session prefilter returns "skip" too.
func importVerdictFor(summary *session.SummarizeResponse) string {
	switch session.EvaluateQualityCategory(summary.QualityCategory) {
	case session.QualityDiscard:
		return "not worth sharing"
	case session.QualityLocalOnly:
		return "kept local"
	}
	return ""
}

// rememberVerdict records a session left local, so a rerun lists it as such
// instead of summarizing it again, and returns the reason to report.
func rememberVerdict(env *importEnv, c *importCandidate, verdict, reason string) string {
	reason = clipImportText(strings.TrimSpace(reason), 160)
	if env.verdicts != nil {
		v := importVerdict{Size: c.Session.Size, Verdict: verdict, Reason: reason, At: env.deps.now().UTC()}
		if err := env.verdicts.record(c.Session, v); err != nil {
			env.logger.Warn("session import: could not remember a verdict", "error", err)
		}
	}
	if reason == "" {
		return verdict
	}
	return verdict + ": " + reason
}

func clipImportText(s string, limit int) string {
	if runes := []rune(s); len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return s
}

// justInTimeCheck repeats the in-progress checks right before publishing: the
// file must be unchanged since it was inspected, and still quiet.
func justInTimeCheck(env *importEnv, c *importCandidate) string {
	info, err := os.Stat(c.Session.Path)
	if err != nil {
		return "native file is gone"
	}
	if info.Size() != c.Session.Size || !info.ModTime().Equal(c.Session.ModTime) {
		return "became active since the preview"
	}
	if env.deps.now().Sub(info.ModTime()) < importQuietPeriod {
		return "became active since the preview"
	}
	return ""
}

// summarizeImport writes the summary on this machine before anything is
// uploaded. A brief session gets the deterministic summary with no LLM call.
// Otherwise the isolated summarizer gets up to three attempts: a runner
// failure holds the session; output the validators keep rejecting gets the
// deterministic fallback, since those sessions would fail on every retry.
func summarizeImport(ctx context.Context, env *importEnv, c *importCandidate, stored *session.StoredSession) (*session.SummarizeResponse, error) {
	entries := sessionsummary.EntriesFromRaw(stored.Entries)
	if brief, ok := sessionsummary.MaybeBuildSkipSummary(entries); ok {
		return brief, nil
	}
	runner := env.deps.runner(c.Summarizer)
	if runner == nil || !runner.Available() {
		return nil, heldf("the %s CLI is not available to summarize; retry with --summarizer", c.Summarizer)
	}
	workDir, err := os.MkdirTemp("", "ox-import-summary-*")
	if err != nil {
		return nil, heldf("summary workspace: %v", err)
	}
	defer os.RemoveAll(workDir)

	prompt := sessionsummary.BuildInlineSummaryPrompt(sessionsummary.TrimEntriesForBudget(entries, sessionsummary.ImportPromptBudget))
	model := ""
	if c.Summarizer == nativeimport.AgentClaude {
		model = sessionsummary.DefaultSummaryModel()
	}
	var rejected, runnerErr error
	for attempt := 1; attempt <= importSummaryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req := agentwork.RunRequest{
			Prompt: prompt, WorkDir: workDir, Model: model, Isolated: true, TimeoutOverride: importSummaryTimeout,
		}
		if rejected != nil {
			req.Prompt += "\n\nYour previous answer was rejected: " + rejected.Error() +
				". Write a summary that avoids this, and output only the JSON object."
		}
		result, err := runner.Run(ctx, req)
		if err == nil && result.ExitCode != 0 {
			err = fmt.Errorf("%s exited with code %d%s", c.Summarizer, result.ExitCode, cliMessage(result.Output))
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err() // interrupted: a retry or the fallback summary would mask it
			}
			runnerErr = err
			continue
		}
		summary, verr := sessionsummary.ParseAndValidate(result.Output, len(stored.Entries))
		if verr == nil {
			return summary, nil
		}
		rejected = verr
	}
	if rejected != nil {
		return sessionsummary.ImportFallbackSummary(entries, importLabel(c.Session.Agent), rejected), nil
	}
	return nil, heldf("summary: %v", runnerErr)
}

// cliMessage is the first line a failed summarizer CLI printed, such as
// "Not logged in · Please run /login", so the report names the real cause.
func cliMessage(output string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	if line == "" {
		return ""
	}
	return ": " + clipImportText(line, 160)
}

func importLabel(agent nativeimport.Agent) string {
	if agent == nativeimport.AgentCodex {
		return "Imported Codex session"
	}
	return "Imported Claude Code session"
}

// residualSecret scans every staged artifact after redaction, with the same
// rules the writer used and no size cap, and names the first file in which a
// secret pattern still matches.
func residualSecret(projectRoot, dir string) (string, error) {
	redactor, problems := session.NewRedactorWithCustomRules(projectRoot)
	if len(problems) > 0 {
		return "", fmt.Errorf("invalid redaction policy (%d errors)", len(problems))
	}
	for _, pattern := range session.DefaultExtraDetectors() {
		redactor.AddPattern(pattern)
	}
	names := make([]string, 0, len(importArtifacts))
	for name := range importArtifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		found, err := containsSecret(redactor, name, data)
		if err != nil {
			return "", err
		}
		if found {
			return name, nil
		}
	}
	return "", nil
}

// containsSecret checks decoded text, as the writer redacted it, so escaping
// cannot hide or fake a match: a JSON artifact as one document (meta.json and
// summary.json are indented, so their lines are fragments), a JSONL artifact
// record by record. An artifact that does not decode cannot be scanned, and
// is an error, so the session is held rather than published unscanned.
func containsSecret(r *session.Redactor, name string, data []byte) (bool, error) {
	switch {
	case strings.HasSuffix(name, ".jsonl"):
		for i, line := range bytes.Split(data, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			found, err := jsonContainsSecret(r, line)
			if err != nil {
				return false, fmt.Errorf("cannot scan %s line %d: %w", name, i+1, err)
			}
			if found {
				return true, nil
			}
		}
		return false, nil
	case strings.HasSuffix(name, ".json"):
		found, err := jsonContainsSecret(r, data)
		if err != nil {
			return false, fmt.Errorf("cannot scan %s: %w", name, err)
		}
		return found, nil
	default:
		return r.ContainsSecrets(string(data)), nil
	}
}

func jsonContainsSecret(r *session.Redactor, data []byte) (bool, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return false, err
	}
	found := false
	nativeimport.WalkStrings(value, func(s string) {
		if !found && r.ContainsSecrets(s) {
			found = true
		}
	})
	return found, nil
}

// checkImportStaging refuses anything but the known artifacts, and any LFS
// content file that is not the pointer for the object just uploaded.
func checkImportStaging(dir string, refs map[string]lfs.FileRef) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !importArtifacts[e.Name()] {
			return fmt.Errorf("staging holds %q, which is not a session artifact", e.Name())
		}
	}
	for name, ref := range refs {
		pointer, err := lfs.ReadPointerFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s is not an LFS pointer: %w", name, err)
		}
		if pointer.OID != ref.OID || pointer.Size != ref.Size {
			return fmt.Errorf("%s does not point at the uploaded object", name)
		}
	}
	return nil
}

// commitImport moves a finished, pointer-only session into sessions/<name>/
// and commits exactly that directory, under the same clone lock the daemon's
// pull takes. A failure inside the lock rolls the directory back.
func commitImport(ctx context.Context, env *importEnv, c *importCandidate, staging string) (string, error) {
	if err := prepareDraftLedgerWrite(env.ledgerPath, c.Name); err != nil {
		return "", heldf("the Ledger is not safe to write: %v", err)
	}
	rel := filepath.ToSlash(filepath.Join("sessions", c.Name))
	dest := filepath.Join(env.ledgerPath, "sessions", c.Name)
	landed := false
	err := gitutil.WithRepoLock(ctx, env.ledgerPath, func() error {
		if err := gitutil.IsSafeForGitOps(env.ledgerPath); err != nil {
			return err
		}
		if unmerged, err := gitutil.HasUnmergedEntries(ctx, env.ledgerPath); err != nil || unmerged {
			return fmt.Errorf("the Ledger has unmerged paths")
		}
		if _, err := gitutil.RunGit(ctx, env.ledgerPath, "cat-file", "-e", "HEAD:"+rel); err == nil {
			landed = true // another machine's import of the same session arrived
			return nil
		}
		// An own leftover from a crashed run is replaced, never merged.
		_, _ = gitutil.RunGit(ctx, env.ledgerPath, "rm", "-r", "-q", "--cached", "--ignore-unmatch", "--sparse", "--", rel)
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.Rename(staging, dest); err != nil {
			return err
		}
		rollback := func(cause error) error {
			_, _ = gitutil.RunGit(ctx, env.ledgerPath, "rm", "-r", "-q", "--cached", "--ignore-unmatch", "--sparse", "--", rel)
			_ = os.RemoveAll(dest)
			return cause
		}
		if _, err := gitutil.RunGit(ctx, env.ledgerPath, "add", "--sparse", "--", rel+"/"); err != nil {
			return rollback(err)
		}
		if _, err := gitutil.CommitLedgerSnapshot(ctx, env.ledgerPath, "session import: "+c.Name, rel+"/"); err != nil {
			return rollback(err)
		}
		return nil
	})
	if err != nil {
		if gitutil.IsRepoLockBusy(err) {
			return "", heldf("the Ledger is busy syncing; retry in a moment")
		}
		return "", heldf("commit: %v", err)
	}
	if landed {
		return "imported from another machine meanwhile", nil
	}
	return "", nil
}
