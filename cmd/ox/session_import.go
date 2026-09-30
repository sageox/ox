package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var sessionImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Upload past Claude Code and Codex sessions from this repo to the Ledger",
	Long: `Upload the Claude Code and Codex sessions you ran in this repo before SageOx,
or with recording off, to the team's Ledger.

The import shows a preview first: which sessions are ready, and which are
skipped and why. Nothing is uploaded until you confirm. Each session is
summarized on this computer by your own claude or codex CLI, in an isolated
mode with no tools, and then uploaded once. A session the summarizer judges
not worth sharing stays on this computer, as it would had ox recorded it.
Running the import again uploads nothing twice: sessions already imported,
and sessions ox recorded live, are recognized and skipped.

This is not 'ox agent <id> session import', which adds a planning
conversation to the current recording.`,
	Example: `  ox session import                    # preview, then confirm
  ox session import --dry-run --json   # preview only, machine-readable
  ox session import --agent codex --since 30d
  ox session import --session 3f9a1c2b --yes`,
	Args: cobra.NoArgs,
	RunE: runSessionImport,
}

func init() {
	sessionCmd.AddCommand(sessionImportCmd)
	addSessionImportFlags(sessionImportCmd.Flags())
}

func addSessionImportFlags(f *pflag.FlagSet) {
	f.String("agent", "", "only sessions from this tool: claude or codex")
	f.String("since", "", "only sessions active within this window (7d, 48h) or since a date (2026-09-01)")
	f.StringSlice("session", nil, "import exactly these sessions, by native session ID or a unique prefix of 8+ characters")
	f.String("summarizer", "", "summarize with this CLI instead of each session's own: claude or codex")
	f.Bool("dry-run", false, "preview only; never upload")
}

// importCandidate is one native session, its classification and, after a
// run, what happened to it.
type importCandidate struct {
	Session    nativeimport.Session
	State      importState
	Reason     string
	Covered    string
	Name       string
	SessionID  string
	Summarizer nativeimport.Agent
	Selected   bool
	Outcome    string
	Detail     string
	Retry      string
	URL        string
	meta       *lfs.SessionMeta
}

// importIgnored counts native sessions that are not this repo's history.
type importIgnored struct {
	OtherFolders    int `json:"other_folders"`
	OxRuns          int `json:"ox_runs"`
	SubagentThreads int `json:"subagent_threads"`
	InternalThreads int `json:"codex_internal_threads"`
	Unreadable      int `json:"unreadable"`
}

type importOptions struct {
	agent      nativeimport.Agent
	since      time.Time
	sessions   []string
	summarizer nativeimport.Agent
	dryRun     bool
	yes        bool
	jsonOut    bool
	agentCtx   bool
}

type importDestination struct {
	Team       string `json:"team,omitempty"`
	RepoID     string `json:"repo_id"`
	Visibility string `json:"visibility"`
	Ledger     string `json:"ledger"`
	readOnly   bool
	verified   bool
}

// Preflight refusal codes. Each stops the run before any LLM call or write.
const (
	importErrNotInitialized    = "not_initialized"
	importErrNotLoggedIn       = "not_logged_in"
	importErrNoLedger          = "no_ledger"
	importErrRecordingDisabled = "recording_disabled"
	importErrReadOnly          = "read_only"
	importErrRedactionRules    = "redaction_rules_invalid"
	importErrUnverified        = "destination_unverified"
	importErrInProgress        = "import_in_progress"
	importErrLedgerWedged      = "ledger_wedged"
	importErrLedgerUnreadable  = "ledger_unreadable"
	importErrNativeUnreadable  = "native_store_unreadable"
	importErrBadFlag           = "invalid_flag"
)

type importFailure struct {
	Code     string
	Message  string
	Guidance string
}

func (f importFailure) Error() string { return f.Code + ": " + f.Message }

const (
	importAgentGuidance = "This is a preview; nothing was uploaded. Show the coworker the destination, " +
		"its visibility, and the sessions listed as ready, and ask whether to upload them. " +
		"Rerun with --yes only after they confirm. Never add --yes on your own."
	importNothingGuidance = "Nothing to upload. Tell the coworker why each session was skipped; " +
		"a session in progress can be imported once it finishes."
)

func runSessionImport(cmd *cobra.Command, _ []string) error {
	opts, failure := parseImportOptions(cmd)
	out := cmd.OutOrStdout()
	if failure != nil {
		return renderImportFailure(out, opts.jsonOut, *failure)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	env, dest, failure := importPreflight(ctx, opts)
	if failure != nil {
		return renderImportFailure(out, opts.jsonOut, *failure)
	}
	env.deps = productionImportDeps(env)
	return runSessionImportFlow(ctx, out, opts, env, dest)
}

func parseImportOptions(cmd *cobra.Command) (importOptions, *importFailure) {
	var o importOptions
	o.jsonOut, _ = cmd.Root().PersistentFlags().GetBool("json")
	jsonSet := cmd.Root().PersistentFlags().Changed("json")
	o.agentCtx = agentx.IsAgentContext()
	if o.agentCtx && !jsonSet {
		o.jsonOut = true
	}
	o.yes = cli.AssumeYes()
	o.dryRun, _ = cmd.Flags().GetBool("dry-run")
	o.sessions, _ = cmd.Flags().GetStringSlice("session")
	bad := func(msg string) (importOptions, *importFailure) {
		return o, &importFailure{Code: importErrBadFlag, Message: msg}
	}
	agent, _ := cmd.Flags().GetString("agent")
	var ok bool
	if o.agent, ok = parseImportAgent(agent); !ok {
		return bad("--agent must be claude or codex")
	}
	summarizer, _ := cmd.Flags().GetString("summarizer")
	if o.summarizer, ok = parseImportAgent(summarizer); !ok {
		return bad("--summarizer must be claude or codex")
	}
	if since, _ := cmd.Flags().GetString("since"); since != "" {
		t, err := parseImportSince(since, time.Now())
		if err != nil {
			return bad(err.Error())
		}
		o.since = t
	}
	for _, id := range o.sessions {
		if len(id) < 8 {
			return bad(fmt.Sprintf("--session %q: use at least 8 characters of the session ID", id))
		}
	}
	return o, nil
}

func parseImportAgent(value string) (nativeimport.Agent, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", true
	case "claude", "claude-code":
		return nativeimport.AgentClaude, true
	case "codex":
		return nativeimport.AgentCodex, true
	}
	return "", false
}

// parseImportSince accepts a window (7d, 48h, 90m) or a date (2006-01-02).
func parseImportSince(value string, now time.Time) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return t, nil
	}
	if strings.HasSuffix(value, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(value, "d")); err == nil && days > 0 {
			return now.Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(value); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("--since %q: use a window like 7d or 48h, or a date like 2026-09-01", value)
}

// importPreflight refuses, with a named code, anything that would make the run
// unsafe or pointless, before any session is read or summarized.
func importPreflight(ctx context.Context, opts importOptions) (*importEnv, importDestination, *importFailure) {
	var dest importDestination
	fail := func(code, msg, guidance string) (*importEnv, importDestination, *importFailure) {
		return nil, dest, &importFailure{Code: code, Message: msg, Guidance: guidance}
	}
	root, err := requireProjectRoot()
	if err != nil || !config.IsInitialized(root) {
		return fail(importErrNotInitialized, "this directory is not in a SageOx project", "Run ox init in the repo first.")
	}
	pctx, err := config.LoadProjectContext(root)
	if err != nil || pctx == nil || pctx.RepoID() == "" {
		return fail(importErrNoLedger, "this repo has no SageOx repo ID", "Run ox init, then ox sync.")
	}
	repoID, ep := pctx.RepoID(), pctx.Endpoint()
	ledgerPath := importLedgerPath(root)
	if ledgerPath == "" {
		return fail(importErrNoLedger, "the Ledger is not cloned on this machine", "Run ox sync, then retry.")
	}
	dest = importDestination{RepoID: repoID, Ledger: ledgerPath, Visibility: "unknown"}
	if cfg, err := config.LoadProjectConfig(root); err == nil && cfg != nil {
		dest.Team = cfg.TeamName
	}
	if ok, _ := auth.IsAuthCredentialValidForEndpoint(ep); !ok {
		return fail(importErrNotLoggedIn, "you are not logged in to SageOx", "Run ox login, then retry.")
	}
	if !config.ResolveSessionRecording(root).ShouldRecord() {
		return fail(importErrRecordingDisabled, "session recording is disabled for this repo",
			"Importing would upload sessions you chose not to record. Enable recording with ox config set session_recording auto.")
	}
	if err := session.ValidateRedactPolicy(root); err != nil {
		return fail(importErrRedactionRules, err.Error(), "Fix the listed REDACT.md problems (see ox session redaction policy), then retry.")
	}
	fetchImportDestination(ep, repoID, &dest)
	if !opts.dryRun {
		if !dest.verified {
			return fail(importErrUnverified, "could not confirm the destination Ledger with SageOx",
				"Check your connection and ox status, then retry. A preview still works with --dry-run.")
		}
		if dest.readOnly {
			return fail(importErrReadOnly, "you have read-only access to this repo's Ledger", "Ask a team admin for member access.")
		}
	}
	env := &importEnv{
		projectRoot: root,
		ledgerPath:  ledgerPath,
		repoID:      repoID,
		endpoint:    ep,
		username:    identity.AttributionDisplayName(ep, config.GetDisplayName()),
		stagingRoot: importStagingRoot(ledgerPath),
		summarizer:  opts.summarizer,
		logger:      slog.Default(),
	}
	return env, dest, nil
}

// importStagingRoot is where sessions are assembled before the commit: the
// Ledger's gitignored cache, never sessions/, so a failed session leaves
// nothing a later git add could pick up.
func importStagingRoot(ledgerPath string) string {
	return filepath.Join(ledgerPath, ".sageox", "cache", "session-import", "staging")
}

// importLedgerPath is this project's configured Ledger, and only if it is a
// git repository: never a path derived from the working directory.
func importLedgerPath(root string) string {
	path := configuredLedgerPath(root)
	if path == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return ""
	}
	return path
}

func fetchImportDestination(ep, repoID string, dest *importDestination) {
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil {
		return
	}
	detail, err := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken).GetRepoDetail(repoID)
	if err != nil || detail == nil {
		return
	}
	dest.verified = true
	dest.readOnly = detail.IsReadOnly()
	if detail.Visibility != "" {
		dest.Visibility = detail.Visibility
	}
}

func productionImportDeps(env *importEnv) importDeps {
	runners := map[nativeimport.Agent]agentwork.Runner{}
	projectCfg, _ := config.LoadProjectConfig(env.projectRoot)
	return importDeps{
		readNative: readNativeWithAdapter,
		runner: func(agent nativeimport.Agent) agentwork.Runner {
			if runners[agent] == nil {
				runners[agent] = agentwork.NewRunner(string(agent), env.logger)
			}
			return runners[agent]
		},
		lfsClient: func() (*lfs.Client, error) {
			return lfs.NewClientFromLedger(env.ledgerPath, env.endpoint)
		},
		push: pushLedger,
		notify: func(meta *lfs.SessionMeta, name string) {
			_, _ = notifySessionUploaded(env.projectRoot, meta, name)
		},
		now: time.Now,
		urlFor: func(sessionID string) string {
			if projectCfg == nil {
				return ""
			}
			return buildConversationURL(projectCfg, sessionID)
		},
		usable: func(agent nativeimport.Agent) bool {
			u := agentwork.CheckAgentUsability(string(agent))
			return u.Installed && u.Authenticated
		},
		syncLedger:  requestLedgerSync,
		interactive: cli.IsInteractive,
		confirm: func(prompt string) (bool, error) {
			return cli.ConfirmYesNoRequired(prompt, false, false)
		},
	}
}

// runImport is everything after preflight: discovery, classification, the
// preview and, once confirmed, the uploads.
func runSessionImportFlow(ctx context.Context, out io.Writer, opts importOptions, env *importEnv, dest importDestination) error {
	if !importMayUpload(opts, env.deps.interactive()) {
		cands, ignored, failure := planImport(ctx, opts, env)
		if failure != nil {
			return renderImportFailure(out, opts.jsonOut, *failure)
		}
		return renderImportPreview(out, opts, dest, cands, ignored, true)
	}
	var runErr error
	ran := false
	lockTarget := filepath.Join(env.ledgerPath, ".sageox", "cache", "session-import")
	if err := os.MkdirAll(lockTarget, 0o700); err != nil {
		return err
	}
	lockErr := fileutil.WithFileLockTimeout(ctx, lockTarget, time.Second, func() error {
		ran = true
		runErr = runLockedImport(ctx, out, opts, env, dest)
		return nil
	})
	if !ran {
		msg := "another import is running against this Ledger"
		if lockErr != nil && !errors.Is(lockErr, context.DeadlineExceeded) && !strings.Contains(lockErr.Error(), "timeout") {
			msg = "could not take the import lock: " + lockErr.Error()
		}
		return renderImportFailure(out, opts.jsonOut, importFailure{
			Code: importErrInProgress, Message: msg, Guidance: "Wait for it to finish, then rerun.",
		})
	}
	return runErr
}

// importMayUpload decides whether a run goes past the preview. --yes is the
// confirmation; without it only a coworker at a terminal, reading text, is
// asked. An AI coworker or a JSON reader gets the preview and must rerun.
func importMayUpload(opts importOptions, interactive bool) bool {
	if opts.dryRun {
		return false
	}
	return opts.yes || (interactive && !opts.agentCtx && !opts.jsonOut)
}

func runLockedImport(ctx context.Context, out io.Writer, opts importOptions, env *importEnv, dest importDestination) error {
	if failure := checkImportLedgerHealth(ctx, env); failure != nil {
		return renderImportFailure(out, opts.jsonOut, *failure)
	}
	if err := os.RemoveAll(env.stagingRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(env.stagingRoot, 0o700); err != nil {
		return err
	}
	cands, ignored, failure := planImport(ctx, opts, env)
	if failure != nil {
		return renderImportFailure(out, opts.jsonOut, *failure)
	}
	selected := selectedCandidates(cands)
	if len(selected) == 0 {
		if opts.jsonOut {
			return renderImportResult(out, opts, dest, cands, ignored)
		}
		return renderImportPreview(out, opts, dest, cands, ignored, true)
	}
	if !opts.yes {
		if err := renderImportPreview(out, opts, dest, cands, ignored, false); err != nil {
			return err
		}
		prompt := fmt.Sprintf("Upload %d session%s to %s (%s)?", len(selected), plural(len(selected)), ledgerLabel(dest), dest.Visibility)
		confirmed, err := env.deps.confirm(prompt)
		if err != nil || !confirmed {
			fmt.Fprintln(out, "Nothing was uploaded.")
			return nil
		}
	}
	client, err := env.deps.lfsClient()
	if err != nil {
		return renderImportFailure(out, opts.jsonOut, importFailure{
			Code: importErrNotLoggedIn, Message: "no Ledger credentials on this machine: " + err.Error(), Guidance: "Run ox login, then retry.",
		})
	}
	env.lfs = client

	batch := env.pushBatch
	if batch <= 0 {
		batch = importPushBatch
	}
	var pending []*importCandidate // committed, not yet pushed
	var pushErr error
	sinceFlush := 0
	failed := false
	flush := func() {
		sinceFlush = 0
		if len(pending) == 0 {
			return
		}
		if pushErr = env.deps.push(ctx, env.ledgerPath); pushErr != nil {
			return // they stay pending: the next push carries their commits too
		}
		for _, c := range pending {
			c.Outcome, c.URL = "uploaded", env.deps.urlFor(c.SessionID)
			env.deps.notify(c.meta, c.Name)
		}
		pending = nil
	}
	for i, c := range selected {
		if !opts.jsonOut {
			fmt.Fprintf(out, "[%d/%d] %s %-6s %s summarizing… ", i+1, len(selected), c.Session.StartedAt.Local().Format("2006-01-02"), c.Session.Agent, nativeShortID(c.Session.NativeID))
		}
		skip, err := publishImport(ctx, env, c)
		switch {
		case err != nil:
			failed = true
			c.Outcome, c.Detail = "failed", strings.TrimPrefix(err.Error(), errImportHeld.Error()+": ")
			c.Retry = importRetryCommand(opts, c)
		case skip != "":
			c.Outcome, c.Detail = "skipped", skip
		default:
			c.Outcome = "committed"
			pending = append(pending, c)
			sinceFlush++
		}
		if !opts.jsonOut {
			printImportLine(out, c)
		}
		if sinceFlush >= batch {
			flush()
		}
	}
	if sinceFlush > 0 {
		flush()
	}
	for _, c := range pending { // the last push failed
		failed = true
		c.Outcome, c.Detail = "committed", "committed locally but not pushed ("+pushErr.Error()+"); the next import pushes it first"
	}
	if err := renderImportResult(out, opts, dest, cands, ignored); err != nil {
		return err
	}
	if failed {
		return cli.ErrSilent
	}
	return nil
}

// checkImportLedgerHealth refuses a clone in any state #1105 describes, and
// pushes commits an earlier run left behind before adding new ones.
func checkImportLedgerHealth(ctx context.Context, env *importEnv) *importFailure {
	wedged := func(msg string) *importFailure {
		return &importFailure{Code: importErrLedgerWedged, Message: msg, Guidance: "Run ox doctor to repair the Ledger, then retry."}
	}
	if gitutil.IsRebaseInProgress(env.ledgerPath) {
		return wedged("the Ledger has a rebase in progress")
	}
	if err := gitutil.IsSafeForGitOps(env.ledgerPath); err != nil {
		return wedged("the Ledger is not safe to write: " + err.Error())
	}
	if unmerged, err := gitutil.HasUnmergedEntries(ctx, env.ledgerPath); err != nil || unmerged {
		return wedged("the Ledger has unmerged paths")
	}
	if out, err := gitutil.RunGit(ctx, env.ledgerPath, "rev-list", "--count", "@{upstream}..HEAD"); err == nil {
		if n, _ := strconv.Atoi(strings.TrimSpace(out)); n > 0 {
			if err := env.deps.push(ctx, env.ledgerPath); err != nil {
				return wedged(fmt.Sprintf("%d earlier Ledger commit%s could not be pushed: %v", n, plural(n), err))
			}
		}
	}
	return nil
}

// planImport discovers native sessions, keeps this repo's, and classifies
// each one against the Ledger index and this machine's verdicts.
func planImport(ctx context.Context, opts importOptions, env *importEnv) ([]*importCandidate, importIgnored, *importFailure) {
	var ignored importIgnored
	scope, err := nativeimport.NewScope(env.projectRoot)
	if err != nil {
		return nil, ignored, &importFailure{Code: importErrNotInitialized, Message: "cannot resolve this repository: " + err.Error()}
	}
	env.deps.syncLedger()
	idx, err := buildImportIndex(ctx, env.projectRoot, env.ledgerPath, env.repoID)
	if err != nil {
		return nil, ignored, &importFailure{Code: importErrLedgerUnreadable, Message: err.Error(), Guidance: "Run ox doctor, then retry."}
	}
	sessions, err := discoverImportSessions(opts, scope, &ignored)
	if err != nil {
		return nil, ignored, &importFailure{Code: importErrNativeUnreadable, Message: err.Error()}
	}
	env.verdicts = loadImportVerdicts(env.ledgerPath)
	now := env.deps.now()
	usable := map[nativeimport.Agent]bool{}
	summarizerFor := func(s nativeimport.Session) nativeimport.Agent {
		if env.summarizer != "" {
			return env.summarizer
		}
		return s.Agent
	}
	var cands []*importCandidate
	for _, group := range sessions {
		s := group.session
		c := &importCandidate{Session: s, Summarizer: summarizerFor(s)}
		c.Name = nativeimport.Name(s.Agent, s.NativeID, s.StartedAt)
		c.SessionID = nativeimport.SessionID(env.repoID, c.Name)
		switch {
		case group.problem != "":
			c.State, c.Reason = stateIneligible, group.problem
		default:
			c.State, c.Covered = idx.classify(s, now)
			switch c.State {
			case stateInProgress:
				c.Reason, c.Covered = c.Covered, ""
			case stateAlreadyImported:
				if idx.continuedSinceImport(s) {
					c.Reason = "continued after it was imported; the later part is not uploaded"
				}
			case stateReady:
				if v, ok := env.verdicts.lookup(s); ok {
					c.State, c.Reason = stateNotShared, v.Verdict
					if v.Reason != "" {
						c.Reason += ": " + v.Reason
					}
				}
			}
		}
		if c.State == stateReady {
			if _, seen := usable[c.Summarizer]; !seen {
				usable[c.Summarizer] = env.deps.usable(c.Summarizer)
			}
			if !usable[c.Summarizer] {
				c.State = stateNeedsSummarizer
				c.Reason = fmt.Sprintf("the %s CLI is not installed or not logged in; use --summarizer", c.Summarizer)
			}
		}
		cands = append(cands, c)
	}
	if err := applyImportSelection(cands, opts); err != nil {
		return nil, ignored, &importFailure{Code: importErrBadFlag, Message: err.Error()}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Session.StartedAt.Before(cands[j].Session.StartedAt) })
	return cands, ignored, nil
}

// requestLedgerSync asks the daemon for a fresh pull, bounded and best effort:
// a stale view can only cause a duplicate attempt, which the push converges.
func requestLedgerSync() {
	if !daemon.IsRunning() {
		return
	}
	_ = daemon.NewClientForCurrentRepoWithTimeout(10 * time.Second).RequestSync()
}

type discoveredSession struct {
	session nativeimport.Session
	problem string
}

// discoverImportSessions reads every native store and keeps this repo's
// sessions, one per native session: the longest of any duplicate copies.
func discoverImportSessions(opts importOptions, scope *nativeimport.Scope, ignored *importIgnored) ([]discoveredSession, error) {
	type nativeFile struct {
		agent nativeimport.Agent
		path  string
	}
	var files []nativeFile
	if opts.agent == "" || opts.agent == nativeimport.AgentClaude {
		dir, err := nativeimport.ClaudeProjectsDir()
		if err != nil {
			return nil, err
		}
		found, err := nativeimport.DiscoverClaude(dir)
		if err != nil {
			return nil, err
		}
		ignored.SubagentThreads += found.Subagents
		for _, p := range found.Paths {
			files = append(files, nativeFile{nativeimport.AgentClaude, p})
		}
	}
	if opts.agent == "" || opts.agent == nativeimport.AgentCodex {
		home, err := nativeimport.CodexHome()
		if err != nil {
			return nil, err
		}
		found, err := nativeimport.DiscoverCodex(home)
		if err != nil {
			return nil, err
		}
		for _, p := range found {
			files = append(files, nativeFile{nativeimport.AgentCodex, p})
		}
	}
	oxData := paths.DataDir()
	byKey := map[nativeKey]*discoveredSession{}
	var order []nativeKey
	for _, p := range files {
		var s nativeimport.Session
		var err error
		if p.agent == nativeimport.AgentClaude {
			s, err = nativeimport.InspectClaude(p.path)
		} else {
			s, err = nativeimport.InspectCodex(p.path)
		}
		if err != nil {
			ignored.Unreadable++
			continue
		}
		if s.Internal {
			ignored.InternalThreads++
			continue
		}
		problem := ""
		switch scope.Classify(s.CWDs) {
		case nativeimport.OtherRepo:
			if underDir(s.CWDs, oxData) {
				ignored.OxRuns++
			} else {
				ignored.OtherFolders++
			}
			continue
		case nativeimport.MixedRepos:
			problem = "it also worked in other repositories"
		}
		if problem == "" && !s.HasConversation() {
			problem = "no conversation"
		}
		if problem == "" && !opts.since.IsZero() && s.LastActivity.Before(opts.since) {
			continue // outside --since; not counted as skipped
		}
		key := keyFor(s.Agent, s.NativeID)
		if existing := byKey[key]; existing != nil {
			if s.Size > existing.session.Size {
				existing.session, existing.problem = s, problem
			}
			continue
		}
		byKey[key] = &discoveredSession{session: s, problem: problem}
		order = append(order, key)
	}
	out := make([]discoveredSession, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out, nil
}

func underDir(dirs []string, root string) bool {
	if root == "" {
		return false
	}
	for _, d := range dirs {
		if d == root || strings.HasPrefix(d, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// applyImportSelection selects every ready session, or exactly the ones named
// with --session. A named session that is not ready keeps its reason and is
// never uploaded.
func applyImportSelection(cands []*importCandidate, opts importOptions) error {
	if len(opts.sessions) == 0 {
		for _, c := range cands {
			c.Selected = c.State == stateReady
		}
		return nil
	}
	for _, prefix := range opts.sessions {
		prefix = strings.ToLower(prefix)
		var matches []*importCandidate
		for _, c := range cands {
			if strings.HasPrefix(c.Session.NativeID, prefix) {
				matches = append(matches, c)
			}
		}
		switch len(matches) {
		case 0:
			return fmt.Errorf("--session %s matches no session of this repo", prefix)
		case 1:
			matches[0].Selected = matches[0].State == stateReady
		default:
			return fmt.Errorf("--session %s matches %d sessions; use more characters", prefix, len(matches))
		}
	}
	return nil
}

func selectedCandidates(cands []*importCandidate) []*importCandidate {
	var out []*importCandidate
	for _, c := range cands {
		if c.Selected {
			out = append(out, c)
		}
	}
	return out
}

// importUploadCommand uploads exactly the sessions a preview showed as ready:
// their full IDs are pinned, so neither a filter left off the command nor a
// session that became ready since can widen what is uploaded.
func importUploadCommand(opts importOptions, selected []*importCandidate) string {
	ids := make([]string, 0, len(selected))
	for _, c := range selected {
		ids = append(ids, c.Session.NativeID)
	}
	return "ox session import --yes" + summarizerFlag(opts) + " --session " + strings.Join(ids, ",")
}

// importRetryCommand retries one session by its full ID: two Codex sessions
// started within a minute share their first eight characters.
func importRetryCommand(opts importOptions, c *importCandidate) string {
	return "ox session import --session " + c.Session.NativeID + summarizerFlag(opts)
}

func summarizerFlag(opts importOptions) string {
	if opts.summarizer == "" {
		return ""
	}
	return " --summarizer " + string(opts.summarizer)
}

func nativeShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func ledgerLabel(dest importDestination) string {
	if dest.Team != "" {
		return dest.Team + "'s Ledger"
	}
	return "the team's Ledger"
}

func renderImportFailure(w io.Writer, jsonOut bool, f importFailure) error {
	if jsonOut {
		_ = cli.PrintJSONTo(w, map[string]any{"status": "refused", "error": f.Code, "message": f.Message, "guidance": f.Guidance})
		return cli.ErrSilent
	}
	cli.PrintErrorTo(os.Stderr, f.Message)
	if f.Guidance != "" {
		cli.PrintHintTo(os.Stderr, f.Guidance)
	}
	return cli.ErrSilent
}

// Session model shown in the preview for each summarizer.
func summarizerModelLabel(agent nativeimport.Agent) string {
	if agent == nativeimport.AgentClaude {
		if m := sessionsummary.DefaultSummaryModel(); m != "" {
			return m
		}
	}
	return "default model"
}
