package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/flags"
	gh "github.com/sageox/ox/internal/github"
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/sageox/ox/internal/ledger"
)

// Tunables for the mirror relay. Spec: docs/specs/github-bulletin-mirror.md
// ("Daemon relay").
const (
	// defaultGitHubMirrorDetailBudget caps the items that get per-item GitHub
	// calls (up to four each) in one cycle, so a cold start over a busy repo
	// spreads across cycles instead of burning the token's hourly quota.
	defaultGitHubMirrorDetailBudget = 100

	// githubMirrorCursorOverlap re-lists a little before the cursor so an item
	// GitHub indexed late is not missed. Re-listed items cost nothing: their
	// remembered updated_at matches and they are skipped.
	githubMirrorCursorOverlap = 5 * time.Minute

	// Backoffs by cause. A refusal that only a human or a deploy can fix waits
	// a day; things that heal on their own wait much less.
	githubMirrorBackoffRefused      = 24 * time.Hour
	githubMirrorBackoffUnauthorized = time.Hour
	githubMirrorBackoffRateLimited  = 30 * time.Minute
	githubMirrorBackoffBusy         = 15 * time.Minute

	// githubMirrorTextMax bounds error and reason text kept in the state file
	// and logs.
	githubMirrorTextMax = 300

	// githubMirrorReasonExpired marks an item whose newest human activity is
	// already older than the post lifetime.
	githubMirrorReasonExpired = "expired"

	// githubMirrorCycleTimeout bounds one relay cycle. It sits below the
	// 15-minute default GitHub sync interval because the mirror runs inside the
	// sync cycle: a cycle that outlived the interval would keep the sync manager
	// "syncing" and skip the Ledger's next GitHub sync.
	githubMirrorCycleTimeout = 10 * time.Minute

	// githubMirrorRelayGrace is the window the relay of already-built items gets
	// after the cycle budget ran out during collection. Those items cost GitHub
	// calls to build; throwing them away would let a slow repo repeat the same
	// work every cycle and never finish.
	githubMirrorRelayGrace = time.Minute
)

// MirrorRelayClient is the mirror API surface the relay needs.
// *api.RepoClient satisfies it.
type MirrorRelayClient interface {
	RelayGitHubMirrorItems(ctx context.Context, teamRef string, req githubmirror.RelayRequest) (*githubmirror.RelayResponse, error)
}

var _ MirrorRelayClient = (*api.RepoClient)(nil)

// GitHubMirrorDeps are the relay's collaborators. Every field except Logger
// is a func so tests can drive the relay without a network, a clock or a
// daemon.
type GitHubMirrorDeps struct {
	// Enabled reports the server's features.github_mirror verdict. nil means
	// off.
	Enabled func() bool
	// AuthToken returns the SageOx token the mirror API call is made with.
	AuthToken func() string
	// Team returns the project's team id (or slug) and the on-disk path of
	// its Team Context checkout. Either empty means "not set up here".
	Team func() (teamRef, teamContextPath string)
	// NewFetcher builds the GitHub reader from a GitHub token.
	NewFetcher func(githubToken string) githubmirror.Fetcher
	// NewRelay builds the mirror API client from a SageOx token.
	NewRelay func(authToken string) MirrorRelayClient
	// Now defaults to time.Now.
	Now func() time.Time
	// DetailBudget defaults to defaultGitHubMirrorDetailBudget.
	DetailBudget int
	// CycleTimeout bounds one cycle; defaults to githubMirrorCycleTimeout.
	CycleTimeout time.Duration
	Logger       *slog.Logger
}

// MirrorTarget is one relay cycle's input: the repo the Ledger sync already
// resolved, the same GitHub token, and the same per-kind on/off config.
type MirrorTarget struct {
	LedgerPath   string // holds the local-only state file
	Owner, Repo  string
	GitHubToken  string
	PullRequests bool
	Issues       bool
}

// GitHubMirrorStats is the relay's status for ox status / IPC.
type GitHubMirrorStats struct {
	Enabled bool `json:"enabled"`
	// LastAttempt is the last cycle that got past the gates.
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	// NextAllowed is when a backoff ends; zero or past means not backing off.
	NextAllowed time.Time `json:"next_allowed,omitempty"`
	// RepoStatus is the server's last verdict on the repo (enabled,
	// not_opted_in, not_linked, not_eligible).
	RepoStatus string `json:"repo_status,omitempty"`
	// ItemsRemembered is how many items the state file tracks.
	ItemsRemembered int `json:"items_remembered"`
}

// GitHubMirrorRelayer mirrors a repo's pull requests and issues to the team's
// github bulletin board through the SageOx mirror API. It runs beside the
// Ledger GitHub sync and shares nothing with its failure accounting: it
// returns nothing, so a failure here cannot reach the Ledger sync.
type GitHubMirrorRelayer struct {
	deps GitHubMirrorDeps

	// runMu serializes Run. The sync manager already runs one cycle at a time;
	// this keeps the read-modify-write of the state file safe on its own.
	runMu sync.Mutex

	statsMu  sync.Mutex
	snapshot GitHubMirrorStats
}

// NewGitHubMirrorRelayer applies defaults to deps.
func NewGitHubMirrorRelayer(deps GitHubMirrorDeps) *GitHubMirrorRelayer {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.DetailBudget <= 0 {
		deps.DetailBudget = defaultGitHubMirrorDetailBudget
	}
	if deps.CycleTimeout <= 0 {
		deps.CycleTimeout = githubMirrorCycleTimeout
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &GitHubMirrorRelayer{deps: deps}
}

// newProjectGitHubMirrorRelayer wires the production dependencies for one
// project: the server flag from the cached CLI settings, the heartbeat token
// (then the on-disk token) for the SageOx API, and the project's team.
func newProjectGitHubMirrorRelayer(projectRoot string, settings func() *flags.CLISettingsResponse, heartbeatToken func() string, logger *slog.Logger) *GitHubMirrorRelayer {
	return NewGitHubMirrorRelayer(GitHubMirrorDeps{
		Enabled: func() bool { return githubMirrorFeatureOn(settings) },
		AuthToken: func() string {
			return resolveMirrorAuthToken(heartbeatToken, endpoint.GetForProject(projectRoot))
		},
		Team: func() (string, string) {
			tc := config.FindRepoTeamContext(projectRoot)
			if tc == nil {
				return "", ""
			}
			return tc.TeamID, tc.Path
		},
		NewFetcher: func(githubToken string) githubmirror.Fetcher {
			return gh.NewMirrorFetcher(gh.NewClient(githubToken))
		},
		NewRelay: func(authToken string) MirrorRelayClient {
			return api.NewRepoClientWithEndpoint(endpoint.GetForProject(projectRoot)).WithAuthToken(authToken)
		},
		Logger: logger,
	})
}

// githubMirrorFeatureOn is the server's features.github_mirror verdict. No
// settings yet, a server that does not send the flag, or cached settings too
// old to trust (the refresh has been failing) all mean off: the shared
// resolver treats a stale cache as "no opinion", so a flag the server has since
// revoked cannot stay on indefinitely.
func githubMirrorFeatureOn(settings func() *flags.CLISettingsResponse) bool {
	if settings == nil {
		return false
	}
	// DaemonProvider reads memory only, so there is nothing for a context to cancel
	return flags.Resolve(context.Background(), flags.DaemonProvider{CachedSettings: settings()}).GitHubMirrorEnabled
}

// resolveMirrorAuthToken mirrors SettingsFetcher.resolveAuthToken: the
// heartbeat's token first, then the on-disk token for the endpoint.
func resolveMirrorAuthToken(heartbeatToken func() string, ep string) string {
	if heartbeatToken != nil {
		if t := heartbeatToken(); t != "" {
			return t
		}
	}
	tok, err := auth.GetTokenForEndpoint(ep)
	if err != nil || tok == nil {
		return ""
	}
	return tok.AccessToken
}

// Stats returns the relay's status, or nil when it is off and has never run.
// Safe on a nil receiver.
func (r *GitHubMirrorRelayer) Stats() *GitHubMirrorStats {
	if r == nil {
		return nil
	}
	enabled := r.deps.Enabled != nil && r.deps.Enabled()

	r.statsMu.Lock()
	stats := r.snapshot
	r.statsMu.Unlock()

	if !enabled && stats.LastAttempt.IsZero() {
		return nil
	}
	stats.Enabled = enabled
	return &stats
}

func (r *GitHubMirrorRelayer) publish(st *githubmirror.State) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	r.snapshot = GitHubMirrorStats{
		LastAttempt:     st.LastAttemptAt,
		LastSuccess:     st.LastSuccessAt,
		LastError:       st.LastError,
		NextAllowed:     st.NextAllowedAt,
		RepoStatus:      st.RepoStatus,
		ItemsRemembered: len(st.Items),
	}
}

// Run executes one relay cycle. Every gate miss is a quiet return with no
// network calls. All failures are recorded in the state file and logged; none
// is returned. A cycle is bounded by deps.CycleTimeout; running out of time is
// logged and saved like progress, not recorded as a failure.
func (r *GitHubMirrorRelayer) Run(ctx context.Context, target MirrorTarget) {
	if !r.runMu.TryLock() {
		r.deps.Logger.Debug("github mirror skipped: previous cycle still running")
		return
	}
	defer r.runMu.Unlock()

	log := r.deps.Logger
	now := r.deps.Now()

	if r.deps.Enabled == nil || !r.deps.Enabled() {
		log.Debug("github mirror skipped: feature off")
		return
	}
	if r.deps.Team == nil {
		log.Debug("github mirror skipped: no team lookup")
		return
	}
	teamRef, teamPath := r.deps.Team()
	if teamRef == "" || teamPath == "" {
		log.Debug("github mirror skipped: no team context for this project")
		return
	}
	if _, err := os.Stat(teamPath); err != nil {
		log.Debug("github mirror skipped: team context checkout missing", "path", teamPath, "error", err)
		return
	}
	var authToken string
	if r.deps.AuthToken != nil {
		authToken = r.deps.AuthToken()
	}
	if authToken == "" {
		log.Debug("github mirror skipped: no SageOx auth token")
		return
	}
	if target.LedgerPath == "" || target.GitHubToken == "" || target.Owner == "" || target.Repo == "" {
		log.Debug("github mirror skipped: incomplete target")
		return
	}

	fullName := target.Owner + "/" + target.Repo
	st := r.loadState(target.LedgerPath, fullName, teamRef)
	if now.Before(st.NextAllowedAt) {
		log.Debug("github mirror in backoff", "repo", fullName, "until", st.NextAllowedAt.Format(time.RFC3339))
		r.publish(st)
		return
	}

	st.LastAttemptAt = now
	cycle := &mirrorCycle{
		relayer: r,
		target:  target,
		teamRef: teamRef,
		fetcher: r.deps.NewFetcher(target.GitHubToken),
		relay:   r.deps.NewRelay(authToken),
		st:      st,
		now:     now,
		timeout: r.deps.CycleTimeout,
		log:     log,
	}
	err := cycle.run(ctx)

	switch {
	case err == nil:
		st.LastSuccessAt = now
		st.LastError = ""
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		// shutting down is not a failure: keep what was learned, record nothing
		log.Debug("github mirror cycle interrupted", "repo", fullName)
	case cycle.outOfTime:
		// A slow repo is not a broken one: no backoff and no error. What was
		// finished is saved below, the cursors stayed put, and the next cycle
		// picks up the rest. Success is not claimed either, since the cycle did
		// not complete.
		log.Info("github mirror cycle ran out of time; unfinished items continue next cycle", "repo", fullName, "budget", cycle.timeout.String())
	default:
		r.recordFailure(st, now, err)
		log.Warn("github mirror cycle failed", "repo", fullName, "error", st.LastError, "backoff_until", backoffAttr(st, now))
	}

	st.Prune(now)
	if saveErr := githubmirror.SaveState(target.LedgerPath, st); saveErr != nil {
		log.Warn("github mirror state save failed", "repo", fullName, "error", mirrorText(saveErr))
	}
	r.publish(st)

	cycle.logSummary(err)
}

// loadState returns the repo's state, or a fresh one when the file is
// unreadable or belongs to another repo or team. Losing it only costs repeat
// relays the server ignores, so it never blocks the mirror. A state that
// records no team (written before teams were recorded) is adopted, not dropped.
func (r *GitHubMirrorRelayer) loadState(ledgerPath, fullName, teamRef string) *githubmirror.State {
	st, err := githubmirror.LoadState(ledgerPath)
	if err != nil {
		r.deps.Logger.Warn("github mirror state unreadable, starting fresh", "repo", fullName, "error", mirrorText(err))
	}
	if st == nil {
		st = &githubmirror.State{Version: githubmirror.StateVersion}
	}
	if st.Repo != "" && !strings.EqualFold(st.Repo, fullName) {
		r.deps.Logger.Info("github mirror state belongs to another repo, starting fresh", "repo", fullName, "state_repo", st.Repo)
		st = &githubmirror.State{Version: githubmirror.StateVersion}
	}
	if st.Team != "" && st.Team != teamRef {
		// everything remembered was sent to another team's board; this team's
		// board has none of it, so skipping those items would leave it empty
		r.deps.Logger.Info("github mirror state belongs to another team, starting fresh", "repo", fullName, "team", teamRef, "state_team", st.Team)
		st = &githubmirror.State{Version: githubmirror.StateVersion}
	}
	if st.Items == nil {
		st.Items = map[string]githubmirror.ItemState{}
	}
	st.Repo = fullName
	st.Team = teamRef
	return st
}

// recordFailure stores the error and, for causes with a known recovery time,
// the backoff. Unclassified errors are retried on the next cycle.
func (r *GitHubMirrorRelayer) recordFailure(st *githubmirror.State, now time.Time, err error) {
	st.LastError = mirrorText(err)
	st.LastErrorAt = now
	if d := mirrorBackoff(err); d > 0 {
		st.NextAllowedAt = now.Add(d)
	}
}

func backoffAttr(st *githubmirror.State, now time.Time) string {
	if !st.NextAllowedAt.After(now) {
		return ""
	}
	return st.NextAllowedAt.Format(time.RFC3339)
}

// repoRefusedError is a relay that succeeded on the wire but where the server
// declined the repo itself (not opted in, not linked, not eligible).
type repoRefusedError struct{ Status string }

func (e *repoRefusedError) Error() string {
	return "the mirror does not accept this repo: " + e.Status
}

// mirrorBackoff maps a failure to how long the mirror should stay quiet. Zero
// means retry on the next cycle.
func mirrorBackoff(err error) time.Duration {
	var busy *api.GitHubMirrorBusyError
	var refused *repoRefusedError
	switch {
	case errors.Is(err, api.ErrGitHubMirrorNotEnabled),
		errors.Is(err, api.ErrGitHubMirrorNotAMember),
		errors.Is(err, api.ErrGitHubMirrorUnsupported),
		errors.Is(err, api.ErrVersionUnsupported),
		errors.Is(err, api.ErrInvalidTeamRef),
		errors.As(err, &refused):
		return githubMirrorBackoffRefused
	case errors.Is(err, api.ErrUnauthorized), errors.Is(err, gh.ErrGitHubAuth):
		return githubMirrorBackoffUnauthorized
	case errors.As(err, &busy):
		if busy.RetryAfter > 0 {
			// a server-supplied wait is honored, but never longer than a day
			return min(busy.RetryAfter, githubMirrorBackoffRefused)
		}
		return githubMirrorBackoffBusy
	case errors.Is(err, gh.ErrGitHubRateLimited):
		return githubMirrorBackoffRateLimited
	}
	return 0
}

// mirrorText flattens an error or reason to one bounded line for the state
// file and logs. Item bodies never pass through here.
func mirrorText(v any) string {
	var s string
	switch t := v.(type) {
	case error:
		s = t.Error()
	case string:
		s = t
	default:
		s = fmt.Sprint(t)
	}
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > githubMirrorTextMax {
		s = string(runes[:githubMirrorTextMax]) + "..."
	}
	return s
}

// mirrorCandidate is one listed PR or issue, before any per-item call.
type mirrorCandidate struct {
	kind      string
	number    int
	updatedAt time.Time
	pr        *githubmirror.SourcePR
	issue     *githubmirror.SourceIssue
}

// mirrorPending is a built item waiting to be relayed.
type mirrorPending struct {
	key       string
	updatedAt time.Time
	item      githubmirror.Item
}

// mirrorCycle is the state of one Run past the gates.
type mirrorCycle struct {
	relayer *GitHubMirrorRelayer
	target  MirrorTarget
	teamRef string
	fetcher githubmirror.Fetcher
	relay   MirrorRelayClient
	st      *githubmirror.State
	now     time.Time
	timeout time.Duration // the cycle's time budget
	log     *slog.Logger

	// newestPR and newestIssue are the newest updated_at listed for each kind
	// this cycle; zero when the kind was not listed. They become the cursors
	// once the cycle leaves nothing behind.
	newestPR, newestIssue time.Time

	// outOfTime is set when the time budget, not a failure or a shutdown, ended
	// the cycle early.
	outOfTime bool

	// counters, for the summary log
	listed, skipped, detailed, notFound, unchanged, expired int
	relayed, accepted, current, rejected, deferred, missing int
	detailCalls                                             int // GitHub calls made for per-item detail
	unknownStatus, unexpectedKeys                           int // relay response anomalies
	warned                                                  bool
	cursorAdvanced                                          bool
}

// run bounds the cycle by its time budget. ctx is the daemon's: its
// cancellation is a shutdown. The budget running out is a different thing, and
// outOfTime tells the two apart.
func (c *mirrorCycle) run(ctx context.Context) error {
	work, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	err := c.runWithin(ctx, work)
	if err != nil && ctx.Err() == nil &&
		errors.Is(work.Err(), context.DeadlineExceeded) && errors.Is(err, context.DeadlineExceeded) {
		c.outOfTime = true
	}
	return err
}

// runWithin does the cycle's work. GitHub and relay calls use work, which
// carries the budget; ctx is the daemon's.
func (c *mirrorCycle) runWithin(ctx, work context.Context) error {
	if err := c.refreshRepo(work); err != nil {
		return err
	}

	candidates, err := c.list(work)
	if err != nil {
		return err
	}
	c.listed = len(candidates)

	pending, buildErr := c.collect(work, candidates)

	// Relay what was built even when collection stopped early: the per-item
	// calls were already paid for. If the budget is what stopped it, the relay
	// gets a short window of its own; on the spent context it would fail at
	// once, and a repo slow enough to hit the budget would redo the same work
	// every cycle and never finish.
	relayCtx := work
	if work.Err() != nil && ctx.Err() == nil {
		var stop context.CancelFunc
		relayCtx, stop = context.WithTimeout(ctx, githubMirrorRelayGrace)
		defer stop()
	}
	if relayErr := c.relayPending(relayCtx, pending); relayErr != nil {
		return relayErr
	}
	if buildErr != nil {
		return buildErr
	}

	// Each cursor moves only when nothing was left behind, so the next cycle
	// re-lists exactly the unfinished items; the ones done are skipped cheaply.
	// A kind that was not listed has no newest item and keeps its cursor.
	if c.deferred == 0 && c.missing == 0 {
		if c.newestPR.After(c.st.PullRequestCursor) {
			c.st.PullRequestCursor = c.newestPR
			c.cursorAdvanced = true
		}
		if c.newestIssue.After(c.st.IssueCursor) {
			c.st.IssueCursor = c.newestIssue
			c.cursorAdvanced = true
		}
		c.st.ColdStartDone = true
	}
	return nil
}

// refreshRepo re-reads repo metadata every cycle — one cheap call — because
// the private flag gates what the server may publish team-wide: a repo that
// turned private must stop being relayed as public on the very next cycle, not
// a day later. A failed read fails the cycle; nothing is relayed on stale
// visibility.
func (c *mirrorCycle) refreshRepo(ctx context.Context) error {
	repo, err := c.fetcher.Repo(ctx, c.target.Owner, c.target.Repo)
	if err != nil {
		return fmt.Errorf("get repo: %w", err)
	}
	// GitHub's spelling is canonical; the git remote may be cased differently
	// or predate a rename. A fetcher that leaves a name blank must not blank
	// every source key, so fall back to the remote's spelling for that part.
	if repo.Owner == "" {
		repo.Owner = c.target.Owner
	}
	if repo.Name == "" {
		repo.Name = c.target.Repo
	}
	if repo.FullName == "" {
		repo.FullName = repo.Owner + "/" + repo.Name
	}
	c.st.RepoMeta = &repo
	c.st.RepoMetaAt = c.now
	return nil
}

// since is the listing lower bound for one kind: its cursor less the overlap,
// never older than the post lifetime (anything older would be expired anyway).
func (c *mirrorCycle) since(cursor time.Time) time.Time {
	floor := c.now.Add(-githubmirror.Window)
	if cursor.IsZero() {
		return floor
	}
	since := cursor.Add(-githubMirrorCursorOverlap)
	if since.Before(floor) {
		return floor
	}
	return since
}

// list returns the PRs and issues updated since each kind's own bound, newest
// first, and notes the newest updated_at of each kind for its cursor.
func (c *mirrorCycle) list(ctx context.Context) ([]mirrorCandidate, error) {
	var out []mirrorCandidate

	if c.target.PullRequests {
		prs, err := c.fetcher.ListPullRequests(ctx, c.target.Owner, c.target.Repo, c.since(c.st.PullRequestCursor))
		if err != nil {
			return nil, fmt.Errorf("list pull requests: %w", err)
		}
		for i := range prs {
			out = append(out, mirrorCandidate{kind: githubmirror.KindPullRequest, number: prs[i].Number, updatedAt: prs[i].UpdatedAt, pr: &prs[i]})
			if prs[i].UpdatedAt.After(c.newestPR) {
				c.newestPR = prs[i].UpdatedAt
			}
		}
	}
	if c.target.Issues {
		issues, err := c.fetcher.ListIssues(ctx, c.target.Owner, c.target.Repo, c.since(c.st.IssueCursor))
		if err != nil {
			return nil, fmt.Errorf("list issues: %w", err)
		}
		for i := range issues {
			out = append(out, mirrorCandidate{kind: githubmirror.KindIssue, number: issues[i].Number, updatedAt: issues[i].UpdatedAt, issue: &issues[i]})
			if issues[i].UpdatedAt.After(c.newestIssue) {
				c.newestIssue = issues[i].UpdatedAt
			}
		}
	}

	slices.SortStableFunc(out, func(a, b mirrorCandidate) int { return b.updatedAt.Compare(a.updatedAt) })
	return out, nil
}

// collect walks the listing newest first and builds the items that need
// relaying, spending at most the detail budget on per-item GitHub calls. A
// fatal per-item error stops the walk; what was built so far is returned
// alongside it.
func (c *mirrorCycle) collect(ctx context.Context, candidates []mirrorCandidate) ([]mirrorPending, error) {
	var pending []mirrorPending
	windowStart := c.now.Add(-githubmirror.Window)

	for _, cand := range candidates {
		// the canonical repo, not the remote's spelling, so a differently cased
		// or renamed remote cannot fork keys the server has already seen
		key := githubmirror.SourceKey(c.st.RepoMeta.Owner, c.st.RepoMeta.Name, cand.kind, cand.number)

		prev, remembered := c.st.Items[key]
		if remembered && prev.UpdatedAt.Equal(cand.updatedAt) {
			c.skipped++
			continue
		}
		if cand.updatedAt.Before(windowStart) {
			c.skipped++
			continue
		}
		if c.detailed >= c.relayer.deps.DetailBudget {
			c.deferred++
			continue
		}

		c.detailed++
		item, err := c.build(ctx, cand)
		if errors.Is(err, ledger.ErrGitHubNotFound) {
			// deleted or transferred between the listing and now
			c.notFound++
			c.log.Debug("github mirror item not found on GitHub", "kind", cand.kind, "number", cand.number)
			continue
		}
		if err != nil {
			c.deferred++
			return pending, err
		}

		// A later last_material_change_at with the same hash is not "unchanged": a
		// repeated approval, or a close and reopen between two cycles, moves the
		// expiry clock without moving the hash. Such an item falls through to be
		// relayed again so the server can extend the post's life. This also
		// revives an item remembered as expired (those markers keep a zero
		// last_material_change_at) once fresh human activity puts it back in the
		// window.
		if remembered && prev.ChangeHash == item.ChangeHash && !item.LastMaterialChangeAt.After(prev.LastMaterialChangeAt) {
			// a bot comment, a reaction or a label bumped updated_at; nothing a
			// reader sees changed
			prev.UpdatedAt = cand.updatedAt
			c.st.Items[key] = prev
			c.unchanged++
			continue
		}
		if githubmirror.Expired(item, c.now) {
			// Remembered so the next cycle skips it without per-item calls;
			// otherwise enough expired-but-recently-touched items would eat the
			// whole budget every cycle and a cold start would never finish.
			// LastMaterialChangeAt is left zero on purpose: State.Prune then
			// falls back to RelayedAt and keeps the marker for a full window.
			c.st.Items[key] = githubmirror.ItemState{
				UpdatedAt:  cand.updatedAt,
				ChangeHash: item.ChangeHash,
				Status:     githubmirror.ResultRejected,
				Reason:     githubMirrorReasonExpired,
				RelayedAt:  c.now,
			}
			c.expired++
			continue
		}
		pending = append(pending, mirrorPending{key: key, updatedAt: cand.updatedAt, item: item})
	}
	return pending, nil
}

// build makes the per-item GitHub calls and assembles the relay item.
func (c *mirrorCycle) build(ctx context.Context, cand mirrorCandidate) (githubmirror.Item, error) {
	owner, repo, number := c.target.Owner, c.target.Repo, cand.number
	meta := *c.st.RepoMeta

	c.detailCalls++
	conversation, err := c.fetcher.ListIssueComments(ctx, owner, repo, number)
	if err != nil {
		return githubmirror.Item{}, fmt.Errorf("list comments for %s %d: %w", cand.kind, number, err)
	}
	if cand.issue != nil {
		return githubmirror.BuildIssue(meta, *cand.issue, conversation), nil
	}

	c.detailCalls++
	inline, err := c.fetcher.ListReviewComments(ctx, owner, repo, number)
	if err != nil {
		return githubmirror.Item{}, fmt.Errorf("list review comments for %s %d: %w", cand.kind, number, err)
	}
	c.detailCalls++
	reviews, err := c.fetcher.ListReviews(ctx, owner, repo, number)
	if err != nil {
		return githubmirror.Item{}, fmt.Errorf("list reviews for %s %d: %w", cand.kind, number, err)
	}
	c.detailCalls++
	files, truncated, err := c.fetcher.ListPRFiles(ctx, owner, repo, number, githubmirror.MaxFilesPerPR)
	if err != nil {
		return githubmirror.Item{}, fmt.Errorf("list files for %s %d: %w", cand.kind, number, err)
	}
	return githubmirror.BuildPR(meta, *cand.pr, conversation, inline, reviews, files, truncated), nil
}

// relayPending sends the items in batches and records each result. A batch the
// server finds too large, or invalid, is split until the offending item stands
// alone; that item is recorded as rejected and the rest carry on.
func (c *mirrorCycle) relayPending(ctx context.Context, pending []mirrorPending) error {
	limit := githubmirror.MaxBatchItems

	for len(pending) > 0 {
		n := min(limit, len(pending))
		batch := pending[:n]

		items := make([]githubmirror.Item, n)
		for i, p := range batch {
			items[i] = p.item
		}
		resp, err := c.relay.RelayGitHubMirrorItems(ctx, c.teamRef, githubmirror.RelayRequest{Repo: *c.st.RepoMeta, Items: items})

		if errors.Is(err, api.ErrGitHubMirrorTooLarge) || errors.Is(err, api.ErrGitHubMirrorValidation) {
			if n > 1 {
				limit = max(1, n/2)
				continue
			}
			// retrying the same bytes cannot succeed; wait for the item to change
			c.remember(batch[0], githubmirror.ResultRejected, mirrorText(err))
			pending = pending[1:]
			limit = githubmirror.MaxBatchItems
			continue
		}
		if err != nil {
			return fmt.Errorf("relay items: %w", err)
		}
		if resp == nil {
			return errors.New("relay items: empty response")
		}

		c.st.RepoStatus = resp.RepoStatus
		if resp.RepoStatus != githubmirror.RepoEnabled {
			return &repoRefusedError{Status: resp.RepoStatus}
		}

		sent := make(map[string]struct{}, n)
		for _, p := range batch {
			sent[p.key] = struct{}{}
		}
		results := make(map[string]githubmirror.ItemResult, len(resp.Results))
		for _, res := range resp.Results {
			key := strings.ToLower(res.SourceKey)
			if _, ok := sent[key]; !ok {
				c.unexpectedKeys++
				continue
			}
			results[key] = res
		}
		for _, p := range batch {
			res, ok := results[p.key]
			if !ok {
				c.missing++
				continue
			}
			switch res.Status {
			case githubmirror.ResultAccepted, githubmirror.ResultCurrent, githubmirror.ResultRejected:
				c.remember(p, res.Status, mirrorText(res.Reason))
			default:
				// a status this client does not know: not done. Leaving the item
				// unrecorded retries it next cycle, by which time a newer client
				// or server may agree on what the status means.
				c.unknownStatus++
				c.missing++
			}
		}
		c.warnAnomalies()
		pending = pending[n:]
	}
	return nil
}

// warnAnomalies reports, once per cycle, relay results this client could not
// act on. The affected items stay unrecorded and are retried.
func (c *mirrorCycle) warnAnomalies() {
	if c.warned || (c.unknownStatus == 0 && c.unexpectedKeys == 0 && c.missing == 0) {
		return
	}
	c.warned = true
	c.log.Warn("github mirror relay response not fully understood; affected items will be retried",
		"repo", c.st.Repo, "unknown_status", c.unknownStatus, "unexpected_source_keys", c.unexpectedKeys, "missing_results", c.missing)
}

// remember records a relay outcome. The change hash is kept for rejected items
// too, so a bot-only bump of updated_at does not send the same bytes again.
func (c *mirrorCycle) remember(p mirrorPending, status, reason string) {
	c.relayed++
	switch status {
	case githubmirror.ResultAccepted:
		c.accepted++
	case githubmirror.ResultCurrent:
		c.current++
	case githubmirror.ResultRejected:
		c.rejected++
	}
	if status != githubmirror.ResultRejected {
		reason = ""
	}
	c.st.Items[p.key] = githubmirror.ItemState{
		UpdatedAt:            p.updatedAt,
		ChangeHash:           p.item.ChangeHash,
		LastMaterialChangeAt: p.item.LastMaterialChangeAt,
		Status:               status,
		Reason:               reason,
		RelayedAt:            c.now,
	}
}

// logSummary writes one line per cycle: Info when something was relayed or
// went wrong, Debug when the cycle found nothing to do. Item text is never
// logged.
func (c *mirrorCycle) logSummary(err error) {
	level := slog.LevelDebug
	if c.relayed > 0 || c.deferred > 0 || err != nil {
		level = slog.LevelInfo
	}
	c.log.Log(context.Background(), level, "github mirror cycle",
		"repo", c.st.Repo,
		"listed", c.listed,
		"skipped", c.skipped,
		"detail_items", c.detailed,
		"detail_calls", c.detailCalls,
		"unchanged", c.unchanged,
		"expired", c.expired,
		"not_found", c.notFound,
		"relayed", c.relayed,
		"accepted", c.accepted,
		"current", c.current,
		"rejected", c.rejected,
		"deferred", c.deferred,
		"missing_results", c.missing,
		"cursor_advanced", c.cursorAdvanced,
		"ok", err == nil,
	)
}
