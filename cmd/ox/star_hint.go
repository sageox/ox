package main

// star_hint.go prints the rare one-line "star us on GitHub" ask.
//
// The ask comes from the server's Nudges engine (placement "cli.hint"): the
// server decides WHO is a good fit (someone already getting value from ox) and
// WHICH card; the CLI only decides WHETHER a person is reading right now and
// keeps the local promise that it will not nag. Life of one command:
//
//	PersistentPreRunE -> beginStarHint   gates, caps, and a refresh of the cached
//	                                     answer started in the background
//	command runs (the refresh overlaps it)
//	main              -> showStarHint    join the refresh (bounded), print ONE
//	                                     stderr line, stamp the cap, tell the server
//
// Rules this file keeps:
//   - Humans only. Never with --json/--text, a non-TTY stderr, `ox agent ...`,
//     hooks, the daemon, OX_STARTED_BY_OX, CI or an agent context, a team
//     (oxt_) token or an env-supplied credential, a signed-out machine, when
//     tips are off, or after a command that failed.
//   - At most starHintMaxShows ever per endpoint+person, starHintMinGap apart.
//     The count is stamped only AFTER the line was written, so a line nobody
//     saw does not spend the budget.
//   - Never slows a command noticeably. The GET answer is cached for 24h, the
//     refresh overlaps the command, and every request has a hard 300ms budget.
//     A failure is silent (slog.Debug) and backs off for an hour so a bad
//     network does not cost 300ms on every command.
//   - Only the "star-ox-github" card prints. Any other card on this placement is
//     ignored in this version, and a 404 or unknown_placement (an older server)
//     is a quiet "nothing to show" for a day.
//
// The state lives in its own cache file, never in updatenotice's version cache:
// that file's writers round-trip a fixed set of fields, and an extra tenant is
// exactly how a writer drops the ledger and restores a nag.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/updatenotice"
)

const (
	// starHintNudgeID is the only card this version prints. The server pins the
	// id forever, so a different card on the placement is a newer server's
	// content that this release does not know how to word.
	starHintNudgeID = "star-ox-github"

	// starHintMaxShows is the lifetime cap per endpoint+person.
	starHintMaxShows = 2

	// starHintMinGap keeps the two shows a week apart, so the second one is not
	// the same afternoon's repeat.
	starHintMinGap = 7 * 24 * time.Hour

	// starHintAnswerTTL is how long the server's answer, including "nothing to
	// show", is reused without asking again.
	starHintAnswerTTL = 24 * time.Hour

	// starHintRetryAfterFailure is the backoff after a failed or timed-out ask.
	starHintRetryAfterFailure = time.Hour

	// starHintBudget is the hard time limit on each request.
	starHintBudget = 300 * time.Millisecond

	// starHintStateVersion is the schema of the state file. A newer file is
	// left alone and the hint stays quiet, rather than being rewritten by an
	// older ox that cannot read its meaning.
	starHintStateVersion = 1
)

// starHintStatePath overrides the state file; empty means star-hint.json under
// paths.CacheDir(). A var so tests redirect it, resolved on use for the same
// reason updatenotice.Path is: paths caches the home directory on first use.
var starHintStatePath string

// starHintAutomated reports whether this process looks like CI or a coding
// agent rather than a person. A var so tests can stand in for a person without
// depending on the environment `go test` happens to run in.
var starHintAutomated = detectAutomatedRun

// starHintAutomaticRoots are top-level commands that run because something else
// asked, not because a person typed them. skipPostHogCommand covers the ones
// telemetry already knows; the rest are the long-lived or machine-driven roots.
var starHintAutomaticRoots = map[string]bool{
	"agent":                 true, // `ox agent ...` is the AI coworker's interface
	"hooks":                 true, // git hooks, run by git
	"daemon":                true,
	"mcp":                   true,
	"statusline":            true,
	"git-credential-helper": true,
	"completion":            true,
	"__complete":            true, // the shell asking for completions
	"__completeNoDesc":      true,
}

// starHintIdentity is the signed-in person the hint speaks to.
type starHintIdentity struct {
	endpoint string
	userID   string
	token    string
}

func (i starHintIdentity) key() string { return i.endpoint + "|" + i.userID }

// starHintRun is the work begun in pre-run and finished in main.
type starHintRun struct {
	ident starHintIdentity
	// refreshed closes when the cached answer is as fresh as it is going to get
	// for this command, immediately when it was already fresh.
	refreshed chan struct{}
}

// starHintPending is set by beginStarHint and consumed by showStarHint. Both
// run on the main goroutine; the refresh goroutine touches only the state file.
var starHintPending *starHintRun

// beginStarHint decides, before the command runs, whether this invocation can
// show the hint at all, and starts refreshing the cached answer if it is stale.
// Cheap rejections come first: most commands stop at the TTY check without
// reading a file.
func beginStarHint() {
	if starHintPending != nil { // friction recovery runs pre-run twice
		return
	}
	ident, ok := starHintAudience(cliCtx)
	if !ok {
		return
	}
	now := time.Now()
	state, err := readStarHintState()
	if err != nil {
		slog.Debug("star hint state unreadable", "error", err)
		return
	}
	entry := state.entry(ident.key())
	if !entry.mayShow(now) {
		return // capped or too soon: no network, nothing to do at exit
	}

	run := &starHintRun{ident: ident, refreshed: make(chan struct{})}
	starHintPending = run
	if entry.answerFresh(now) {
		close(run.refreshed)
		return
	}
	go func() {
		defer close(run.refreshed)
		refreshStarHintAnswer(ident, now)
	}()
}

// showStarHint prints the hint if the command succeeded and everything still
// holds. Like capturePostHogCommand it must never change what the command did,
// so a panic is recovered and dropped.
func showStarHint(exitCode int, w io.Writer) {
	run := starHintPending
	starHintPending = nil
	if run == nil || exitCode != 0 {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("star hint failed", "panic", r)
		}
	}()

	// Bounded by the request's own time budget.
	<-run.refreshed

	now := time.Now()
	state, err := readStarHintState()
	if err != nil {
		return
	}
	entry := state.entry(run.ident.key())
	// Re-checked against the file as it is now: another ox may have shown the
	// hint while this command ran.
	if !entry.mayShow(now) || !entry.answerFresh(now) {
		return
	}
	line, ok := starHintLine(entry.Card, run.ident.endpoint)
	if !ok {
		return
	}
	// A command can change who is signed in (logout, login). The ask belongs to
	// the person it was fetched for.
	if auth.LocalUserID(run.ident.endpoint) != run.ident.userID {
		return
	}
	// A command can also turn tips off (`ox config set tips off`), which must
	// silence this very run: read the setting as it is now, not as pre-run saw it.
	if !starHintTipsEnabled() {
		return
	}

	if _, err := fmt.Fprintln(w, line); err != nil {
		return // nothing reached the terminal, so nothing is spent
	}
	if err := updateStarHintEntry(run.ident.key(), func(e *starHintEntry) {
		e.Shown++
		e.LastShown = now
	}); err != nil {
		slog.Debug("star hint stamp failed", "error", err)
	}
	reportStarHintSeen(run.ident, entry.Card.Token)
}

// starHintTipsEnabled reads the tips setting from disk on every call (the user
// config is not cached), so a command that just changed it is honored.
func starHintTipsEnabled() bool {
	userCfg, err := config.LoadUserConfig()
	return err == nil && userCfg != nil && userCfg.AreTipsEnabled()
}

// starHintAudience answers "is a person reading this command's stderr, and are
// they someone we may ask?", cheapest checks first.
func starHintAudience(c *cli.Context) (starHintIdentity, bool) {
	if startedByOx || c == nil || c.Cmd == nil || c.LongRunning {
		return starHintIdentity{}, false
	}
	if c.Config != nil && c.Config.Quiet {
		return starHintIdentity{}, false
	}
	if starHintSkipsCommand(c.CommandPath(c.Cmd)) {
		return starHintIdentity{}, false
	}
	// --json and --text (cli.NewContext sets this from the flags and OX_JSON /
	// OX_TEXT), and a stderr that is not a terminal: the notice-wide rule for
	// prose aimed at a person.
	if updatenotice.Suppressed() {
		return starHintIdentity{}, false
	}
	if starHintAutomated() {
		return starHintIdentity{}, false
	}
	if !starHintTipsEnabled() {
		return starHintIdentity{}, false
	}

	ep := postHogCommandEndpoint(c)
	if auth.EnvTokenIsTeamFamily(ep) {
		return starHintIdentity{}, false
	}
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" ||
		strings.HasPrefix(token.AccessToken, auth.TeamTokenPrefix) || token.IsExpired(0) {
		return starHintIdentity{}, false
	}
	// LocalUserID is empty for a signed-out machine, a team credential, and any
	// credential supplied through SAGEOX_TOKEN: automation has no person to ask.
	userID := auth.LocalUserID(ep)
	if userID == "" {
		return starHintIdentity{}, false
	}
	return starHintIdentity{endpoint: ep, userID: userID, token: token.AccessToken}, true
}

// starHintSkipsCommand reports commands nobody typed for the hint's benefit.
func starHintSkipsCommand(path string) bool {
	if skipPostHogCommand(path) {
		return true
	}
	root, _, _ := strings.Cut(path, " ")
	return starHintAutomaticRoots[root]
}

// detectAutomatedRun is the production starHintAutomated: CI, or a coding agent
// by environment signals or by any other detector agentx has. A person in a
// repository an agent also uses may read as an agent here, which only costs
// them the hint.
func detectAutomatedRun() bool {
	if config.IsCI() || agentx.IsAgentContext() {
		return true
	}
	actor, _ := cli.DetectActor(nil)
	return actor != "human"
}

// starHintLine renders the card as the one stderr line:
//
//	★ {title} {body} {endpoint}{cta.href}
//
// Everything in it comes from the server, so it is stripped of terminal
// escapes (OSC 8 and OSC 52 smuggle links and clipboard writes), and the link
// is plain text, never an OSC 8 hyperlink. Only the star is styled, through
// the theme's adaptive color, so NO_COLOR leaves plain text.
func starHintLine(card *api.NudgeCard, endpoint string) (string, bool) {
	if card == nil || card.NudgeID != starHintNudgeID || card.Token == "" {
		return "", false
	}
	if !isSameOriginPath(card.CTA.Href) {
		return "", false
	}
	title := cli.SanitizeTerminalText(strings.TrimSpace(card.Title))
	body := cli.SanitizeTerminalText(strings.TrimSpace(card.Body))
	if title == "" {
		return "", false
	}
	parts := []string{title}
	if body != "" {
		parts = append(parts, body)
	}
	parts = append(parts, strings.TrimSuffix(endpoint, "/")+card.CTA.Href)
	return cli.StyleSecondary.Render("★") + " " + strings.Join(parts, " "), true
}

// isSameOriginPath accepts "/cli" and refuses everything that could leave the
// endpoint or carry control characters: "//host", "/\host", whitespace, escapes.
func isSameOriginPath(href string) bool {
	if len(href) < 2 || len(href) > 200 || href[0] != '/' || href[1] == '/' {
		return false
	}
	if strings.ContainsAny(href, "\\ \t\r\n") {
		return false
	}
	return cli.SanitizeTerminalText(href) == href
}

// refreshStarHintAnswer asks the server for the placement's card and caches the
// answer. Every outcome is recorded so the next command does not ask again.
func refreshStarHintAnswer(ident starHintIdentity, now time.Time) {
	// Not the command's context: it is canceled the moment the command returns,
	// which is exactly when a slow answer is about to be used.
	ctx, cancel := context.WithTimeout(context.Background(), starHintBudget)
	defer cancel()

	client := api.NewRepoClientWithEndpoint(ident.endpoint).
		WithAuthToken(ident.token).
		WithTimeout(starHintBudget)

	var card *api.NudgeCard
	checkAgainAt := now.Add(starHintAnswerTTL)
	resp, err := client.GetNudge(ctx, api.NudgePlacementCLIHint)
	switch {
	case err == nil:
		card = resp.Card
	case errors.Is(err, api.ErrNudgesUnsupported):
		// an older server: "nothing to show", asked again tomorrow
	default:
		slog.Debug("star hint fetch failed", "error", err)
		checkAgainAt = now.Add(starHintRetryAfterFailure)
	}

	if err := updateStarHintEntry(ident.key(), func(e *starHintEntry) {
		e.Card = card
		e.CheckAgainAt = checkAgainAt
	}); err != nil {
		// Without the file there is no cached answer for showStarHint to read,
		// so a machine that cannot remember never prints.
		slog.Debug("star hint cache write failed", "error", err)
	}
}

// reportStarHintSeen tells the server the card was shown. Best effort.
func reportStarHintSeen(ident starHintIdentity, cardToken string) {
	ctx, cancel := context.WithTimeout(context.Background(), starHintBudget)
	defer cancel()

	client := api.NewRepoClientWithEndpoint(ident.endpoint).
		WithAuthToken(ident.token).
		WithTimeout(starHintBudget)
	if err := client.ReportNudgeSeen(ctx, cardToken, api.NudgePlacementCLIHint); err != nil {
		slog.Debug("star hint seen report failed", "error", err)
	}
}

// --- state ---

// starHintFile is the on-disk state, one entry per endpoint+person.
type starHintFile struct {
	Version int                       `json:"version"`
	People  map[string]*starHintEntry `json:"people"`
}

// starHintEntry is what the CLI remembers about one person on one endpoint.
type starHintEntry struct {
	// Shown counts the lines actually printed.
	Shown     int       `json:"shown"`
	LastShown time.Time `json:"last_shown,omitzero"`
	// CheckAgainAt is when the cached answer expires. A failed ask sets it an
	// hour out with Card empty, so it also records "do not retry yet".
	CheckAgainAt time.Time `json:"check_again_at,omitzero"`
	// Card is the server's last answer; nil means nothing to show.
	Card *api.NudgeCard `json:"card,omitempty"`
}

// mayShow applies the local caps. A LastShown in the future means the clock
// moved backwards; it is treated as unstamped rather than muting the hint until
// real time catches up (the hard cap still bounds the damage).
func (e starHintEntry) mayShow(now time.Time) bool {
	if e.Shown >= starHintMaxShows {
		return false
	}
	if e.LastShown.IsZero() || e.LastShown.After(now) {
		return true
	}
	return now.Sub(e.LastShown) >= starHintMinGap
}

// answerFresh reports whether the cached answer may be used without asking. An
// expiry further out than the TTL can only come from a clock that moved back.
func (e starHintEntry) answerFresh(now time.Time) bool {
	if e.CheckAgainAt.IsZero() || e.CheckAgainAt.After(now.Add(starHintAnswerTTL)) {
		return false
	}
	return now.Before(e.CheckAgainAt)
}

func (f *starHintFile) entry(key string) starHintEntry {
	if e := f.People[key]; e != nil {
		return *e
	}
	return starHintEntry{}
}

func starHintStateFile() string {
	if starHintStatePath != "" {
		return starHintStatePath
	}
	return filepath.Join(paths.CacheDir(), "star-hint.json")
}

// readStarHintState returns the state, empty when the file is missing or
// corrupt (at worst one extra show), and an error for a file written by a newer
// schema, which this release must not overwrite.
func readStarHintState() (*starHintFile, error) {
	empty := &starHintFile{Version: starHintStateVersion, People: map[string]*starHintEntry{}}
	raw, err := os.ReadFile(starHintStateFile())
	if err != nil {
		return empty, nil
	}
	var f starHintFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return empty, nil
	}
	if f.Version > starHintStateVersion {
		return nil, fmt.Errorf("star hint state is version %d, this ox reads %d", f.Version, starHintStateVersion)
	}
	if f.People == nil {
		f.People = map[string]*starHintEntry{}
	}
	f.Version = starHintStateVersion
	return &f, nil
}

// updateStarHintEntry re-reads the file, applies mutate to one entry and saves
// atomically. Re-reading right before the write keeps a refresh that started
// earlier from overwriting a count another ox stamped meanwhile; two writers in
// the same instant can still lose one update, which costs at most an extra show
// bounded by the lifetime cap.
func updateStarHintEntry(key string, mutate func(*starHintEntry)) error {
	state, err := readStarHintState()
	if err != nil {
		return err
	}
	entry := state.entry(key)
	mutate(&entry)
	state.People[key] = &entry

	path := starHintStateFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	return fileutil.AtomicWriteJSON(path, state, 0o600)
}
