package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
	"github.com/sageox/ox/internal/tokens"
	"github.com/sageox/ox/internal/weblink"
	"github.com/spf13/cobra"
)

// Live (v3) sessions are rendered by the server (GET
// /api/v1/sessions/{id}/agent-view); the CLI only fetches and prints. v1
// ledger session names keep their existing local path in runSessionView.

// liveSessionIDPattern is the strict grammar for ids that reach the request
// path. Loose on the UUID/typeid body, strict on the prefix and charset.
var liveSessionIDPattern = regexp.MustCompile(`^(ses|cnv)_[A-Za-z0-9][A-Za-z0-9_-]{5,63}$`)

// liveViewRef is a parsed id or /c/ link. Host is empty for a bare id.
type liveViewRef struct {
	ID   string
	Host string
}

// parseLiveViewRef accepts ses_/cnv_ ids and https://sageox.ai/c/ses_… links
// (any sageox.ai subdomain; trailing slash, query and fragment ignored).
// ok is false for anything else, which falls through to the v1 name path.
func parseLiveViewRef(raw string) (liveViewRef, bool) {
	raw = strings.TrimSpace(raw)
	if liveSessionIDPattern.MatchString(raw) {
		return liveViewRef{ID: raw}, true
	}
	if !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "http://") {
		return liveViewRef{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return liveViewRef{}, false
	}
	host := endpoint.NormalizeSlug("https://" + strings.TrimSuffix(strings.ToLower(u.Hostname()), "."))
	if host != "sageox.ai" && !strings.HasSuffix(host, ".sageox.ai") {
		return liveViewRef{}, false
	}
	segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if len(segs) != 2 || segs[0] != "c" || !liveSessionIDPattern.MatchString(segs[1]) {
		return liveViewRef{}, false
	}
	return liveViewRef{ID: segs[1], Host: host}, true
}

// looksLikeLiveViewArg is true for anything that is plainly meant as a live id
// or link, valid or not, so junk gets a clear error instead of "not found".
func looksLikeLiveViewArg(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "ses_") || strings.HasPrefix(raw, "cnv_") || strings.Contains(raw, "/c/ses_") || strings.Contains(raw, "/c/cnv_")
}

const liveViewFollowInterval = 15 * time.Second

// Seams for tests: the authenticated client, the project lookup, the wait
// between polls and the token operations.
var (
	liveViewNewClient = newLiveViewClient
	liveViewWait      = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
	// liveViewBaseURL maps the request endpoint to the URL requests go to;
	// identity in production, an httptest server in tests.
	liveViewBaseURL = func(ep string) string { return ep }
	// liveViewEnsureToken returns a token for ep, refreshing it when it
	// expires within 5 minutes.
	liveViewEnsureToken = func(ctx context.Context, ep string) (string, error) {
		stored, err := auth.EnsureValidTokenForEndpointContext(ctx, ep, 300)
		if err != nil || stored == nil {
			return "", errors.New("no valid login")
		}
		return stored.AccessToken, nil
	}
	// liveViewForceRefresh refreshes ep's token after the server rejected it.
	liveViewForceRefresh = func(ctx context.Context, ep string) (string, error) {
		stored, err := auth.GetTokenForEndpoint(ep)
		if err != nil || stored == nil {
			return "", errors.New("no stored login")
		}
		refreshed, err := auth.Handle401ErrorForEndpoint(stored, ep)
		if err != nil || refreshed == nil {
			return "", errors.New("refresh failed")
		}
		return refreshed.AccessToken, nil
	}
	// liveViewTrace credits the reading AI coworker's session with the read.
	liveViewTrace = traceLiveSessionRead
)

// liveViewProject returns the repo_id and project root of the ox project this
// command runs in; both empty when there is none. A variable so tests can
// script it. The server, not the client, decides whether this repo may see the
// session: .sageox/config.json is committed and can be stale or crafted.
var liveViewProject = func() (repoID, projectRoot string) {
	root := liveProjectRoot()
	if root == "" {
		return "", ""
	}
	cfg, err := config.LoadProjectConfig(root)
	if err != nil || cfg == nil {
		return "", root
	}
	return cfg.RepoID, root
}

// liveProjectRoot is the ox project root, or "" outside a project.
// findProjectRoot falls back to the cwd (no error) when no .sageox exists, so
// the directory is checked here.
func liveProjectRoot() string {
	root, err := findProjectRoot()
	if err != nil || root == "" {
		return ""
	}
	// an empty .sageox directory is not a project: require its config file
	if !config.IsInitialized(root) {
		return ""
	}
	return root
}

// linkOnlyView is what is shown when the server withheld the content: status,
// ids, link and the server's notice. Never a title or any turn content.
type linkOnlyView struct {
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id,omitempty"`
	Status         string `json:"status"`
	Live           bool   `json:"live"`
	Link           string `json:"link,omitempty"`
	Notice         string `json:"notice"`
}

const defaultOtherTeamNotice = "This session belongs to another team. Its content is not shown here. Open it in the browser."

// linkOnly builds the withheld view. ep is the endpoint the request went to, so
// the browser link stays on the environment that holds the session.
func linkOnly(view *api.AgentView, id, ep string) linkOnlyView {
	if view.SessionID != "" {
		id = view.SessionID
	}
	link := ""
	if p := weblink.IDTypePrefix(id); p != "" {
		link = weblink.ArtifactURL(ep, id, "/c/", p)
	}
	notice := view.Notice
	if notice == "" {
		notice = defaultOtherTeamNotice
	}
	return linkOnlyView{SessionID: id, ConversationID: view.ConversationID, Status: view.Status, Live: view.Live, Link: link, Notice: notice}
}

// withheld fails closed: content is shown only when the server says
// team_match is exactly true. Absent, null, false, or no markdown all withhold.
func withheld(v *api.AgentView) bool {
	return v.TeamMatch == nil || !*v.TeamMatch || strings.TrimSpace(v.Markdown) == ""
}

// liveStoredEndpoint finds the stored login for a pasted link's host, including
// one whose access token expired (it can still refresh).
func liveStoredEndpoint(host string) string {
	var all []string
	if eps, err := auth.ListEndpoints(); err == nil {
		all = append(all, eps...)
	}
	all = append(all, auth.GetLoggedInEndpoints()...)
	for _, ep := range all {
		if endpoint.NormalizeSlug(ep) == host {
			return ep
		}
	}
	return ""
}

// newLiveViewClient resolves the request endpoint and returns a client that
// refreshes its token as it goes. A pasted link's host picks the matching
// stored login; a bare id follows the project endpoint (SAGEOX_ENDPOINT wins).
func newLiveViewClient(ctx context.Context, ref liveViewRef) (liveViewFetcher, string, error) {
	var ep string
	if ref.Host != "" {
		ep = liveStoredEndpoint(ref.Host)
		if ep == "" {
			return nil, "", fmt.Errorf("you are not logged in to %s; run `ox login` and retry", cli.SanitizeTerminalText(ref.Host))
		}
	} else if root := liveProjectRoot(); root != "" {
		ep = endpoint.GetForProject(root)
	} else {
		ep = endpoint.Get()
	}
	if _, err := liveViewEnsureToken(ctx, ep); err != nil {
		return nil, "", fmt.Errorf("not logged in to %s (or the login expired); run `ox login` and retry", cli.SanitizeTerminalText(endpoint.NormalizeSlug(ep)))
	}
	return &refreshingFetcher{ep: ep}, ep, nil
}

// refreshingFetcher re-checks the token before every request (so a long
// --follow outlives one access token) and, after a 401, refreshes once and
// retries.
type refreshingFetcher struct{ ep string }

func (f *refreshingFetcher) GetSessionAgentView(ctx context.Context, id string, o api.SessionAgentViewOptions) (*api.AgentView, error) {
	token, err := liveViewEnsureToken(ctx, f.ep)
	if err != nil {
		return nil, api.ErrUnauthorized
	}
	get := func(tok string) (*api.AgentView, error) {
		c := api.NewRepoClientWithEndpoint(liveViewBaseURL(f.ep)).WithAuthToken(tok).WithTimeout(30 * time.Second)
		return c.GetSessionAgentView(ctx, id, o)
	}
	view, err := get(token)
	if errors.Is(err, api.ErrUnauthorized) {
		if fresh, rerr := liveViewForceRefresh(ctx, f.ep); rerr == nil && fresh != "" {
			return get(fresh)
		}
	}
	return view, err
}

// liveViewMessage maps API errors to what an agent or human should do next.
func liveViewMessage(err error) error {
	switch {
	case errors.Is(err, api.ErrSessionNotFound):
		return errors.New("session not found, or you do not have access to it")
	case errors.As(err, new(*api.RateLimitedError)):
		return errors.New("rate limited by the server; try again shortly")
	case errors.Is(err, api.ErrUnauthorized):
		return errors.New("authentication failed: run `ox login` and retry")
	case errors.Is(err, api.ErrInvalidSessionID):
		return errors.New("invalid session id")
	}
	return err
}

type liveViewOptions struct {
	Ref    liveViewRef
	Since  string
	Budget int
	Layer  string
	JSON   bool
	Follow bool
	// Endpoint is the endpoint the request goes to; browser links use it.
	Endpoint string
}

// liveOnlyFlagsRejected are session-view flags that only apply to a local (v1)
// session; for a live ref they would be silently ignored, so they are refused.
var liveOnlyFlagsRejected = []string{"metadata", "context", "input", "limit", "latest", "text"}

func rejectLocalOnlyFlags(cmd *cobra.Command) error {
	for _, name := range liveOnlyFlagsRejected {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			return fmt.Errorf("--%s applies to local sessions only and is not supported for a live session id or link; live sessions take --json, --since, --budget, --layer and --follow", name)
		}
	}
	return nil
}

func runLiveSessionView(cmd *cobra.Command, ref liveViewRef) error {
	if err := rejectLocalOnlyFlags(cmd); err != nil {
		return err
	}
	opts := liveViewOptions{Ref: ref}
	opts.Since, _ = cmd.Flags().GetString("since")
	opts.Budget, _ = cmd.Flags().GetInt("budget")
	opts.Layer, _ = cmd.Flags().GetString("layer")
	opts.JSON, _ = cmd.Flags().GetBool("json")
	opts.Follow, _ = cmd.Flags().GetBool("follow")

	if opts.Budget < 0 {
		return errors.New("--budget must be positive")
	}
	if opts.Layer != "" && !strings.HasPrefix(opts.Layer, "clyr_") {
		return errors.New("--layer takes a clyr_ layer id (listed at the end of the session view)")
	}
	if opts.Follow && opts.Layer != "" {
		return errors.New("--follow cannot be combined with --layer")
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	client, ep, err := liveViewNewClient(ctx, ref)
	if err != nil {
		return err
	}
	opts.Endpoint = ep
	return streamLiveView(ctx, cmd.OutOrStdout(), client, opts)
}

// liveViewFetcher is the one client method the loop needs.
type liveViewFetcher interface {
	GetSessionAgentView(ctx context.Context, id string, opts api.SessionAgentViewOptions) (*api.AgentView, error)
}

// maxFollowPages bounds paging through a truncated view so a server that keeps
// returning a cursor cannot hold the command forever.
const maxFollowPages = 200

// streamLiveView prints one view, then with --follow keeps polling while the
// session is live, printing only new text, and stops when it leaves live (after
// paging out any truncated remainder) or the context is canceled (Ctrl-C is a
// clean exit).
func streamLiveView(ctx context.Context, w io.Writer, client liveViewFetcher, opts liveViewOptions) error {
	since := opts.Since
	first := true
	traced := false
	contextRepo, projectRoot := liveViewProject()
	lastStatus := ""
	drained := 0 // consecutive pages fetched back to back; reset on every poll wait
	for {
		wasFirst := first
		view, err := fetchWithRetry(ctx, client, opts.Ref.ID, api.SessionAgentViewOptions{Since: since, Budget: opts.Budget, Layer: opts.Layer, ContextRepo: contextRepo})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if opts.Follow && errors.As(err, new(*api.RateLimitedError)) {
				// still throttled after the retries: keep following
				if werr := liveViewWait(ctx, liveViewFollowInterval); werr != nil {
					return nil
				}
				continue
			}
			return liveViewMessage(err)
		}
		isLive := view.Live || view.Status == "live"

		if withheld(view) {
			// link-only: never print markdown, title, turns, or the raw JSON
			lo := linkOnly(view, opts.Ref.ID, opts.Endpoint)
			if first || view.Status != lastStatus {
				if opts.JSON {
					b, _ := json.Marshal(lo)
					fmt.Fprintln(w, string(b))
				} else {
					fmt.Fprintln(w, safeLiveText(linkOnlyText(lo)))
				}
			}
			lastStatus = view.Status
			first = false
			if !opts.Follow || !isLive {
				return nil
			}
			if err := liveViewWait(ctx, liveViewFollowInterval); err != nil {
				return nil
			}
			continue
		}

		if opts.JSON {
			fmt.Fprintln(w, strings.TrimSpace(string(view.Raw)))
		} else if first {
			fmt.Fprintln(w, safeLiveText(strings.TrimRight(view.Markdown, "\n")))
		} else if body := stripStatusLine(view.Markdown); strings.TrimSpace(body) != "" {
			fmt.Fprintln(w, safeLiveText(strings.TrimRight(body, "\n")))
		}
		first = false
		if !traced {
			// content was shown: credit the reading session (never for link-only)
			traced = true
			liveViewTrace(projectRoot, view, opts.Ref.ID, opts.Endpoint)
		}

		if !opts.Follow {
			return nil
		}
		// a truncated page has more turns waiting at next_since: page on
		// immediately, whether or not the session is still live
		more := view.Truncated && view.NextSince != "" && view.NextSince != since && drained < maxFollowPages
		if more {
			drained++
			since = view.NextSince
			continue
		}
		drained = 0
		if !isLive {
			if !opts.JSON && !wasFirst {
				// the closing status line tells the follower why we stopped
				if line := leadingLine(view.Markdown); strings.HasPrefix(line, "Status:") {
					fmt.Fprintln(w, safeLiveText(line))
				}
			}
			return nil
		}
		if view.NextSince != "" {
			since = view.NextSince
		}
		if err := liveViewWait(ctx, liveViewFollowInterval); err != nil {
			return nil
		}
	}
}

// traceLiveSessionRead records the read as a surfaced item of the AI coworker's
// own session (ref and title only, never content), so `context used ses_…` can
// attach to it. Fail-open and a no-op without an agent id or live recording.
func traceLiveSessionRead(projectRoot string, view *api.AgentView, id, ep string) {
	agentID := traceAgentIDFromEnv()
	if agentID == "" || projectRoot == "" {
		return
	}
	traceProvidedHits(projectRoot, agentID, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval,
		[]providedHit{liveSessionHit(view, id, ep)})
}

// liveSessionHit is the trace entry for a live read: a reference only. The
// session title is deliberately left out because the trace writer does not
// redact, so a title could carry a credential into context-trace.jsonl. The
// cnv_ id (and whatever id the caller typed) is kept as an alias so `context
// used` can match it.
func liveSessionHit(view *api.AgentView, id, ep string) providedHit {
	ref := view.SessionID
	if ref == "" {
		ref = id
	}
	url := ""
	if p := weblink.IDTypePrefix(ref); p != "" {
		url = weblink.ArtifactURL(ep, ref, "/c/", p)
	}
	return providedHit{SurfacedRef: contextuse.SurfacedRef{Ref: ref, RefType: "session", URL: url, Aliases: hitAliases(ref, id, view.ConversationID)}}
}

func leadingLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// stripStatusLine drops the leading `Status:` line; follow prints status only
// when it changes (on exit), not on every poll.
func stripStatusLine(md string) string {
	if strings.HasPrefix(md, "Status:") {
		_, rest, _ := strings.Cut(md, "\n")
		return rest
	}
	return md
}

// safeLiveText makes server text inert before it reaches a terminal: ESC
// sequences (CSI, OSC incl. hyperlinks and title changes), C0 and C1 controls
// are dropped, but newlines and tabs survive so markdown keeps its shape.
// Transcripts are untrusted user content. --json output is not passed through
// here and stays byte-exact.
func safeLiveText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = cli.SanitizeTerminalText(l)
	}
	return strings.Join(lines, "\n")
}

// statusLineFor builds the link-only status line from the structured status
// fields alone, never from server markdown (which may carry a title or turn
// text on its first line).
func statusLineFor(lo linkOnlyView) string {
	switch lo.Status {
	case "live":
		return "Status: LIVE (still updating)."
	case "idle":
		return "Status: IDLE."
	case "finished":
		return "Status: FINISHED."
	case "auto_finished":
		return "Status: AUTO-FINISHED."
	}
	if lo.Live {
		return "Status: LIVE (still updating)."
	}
	return "Status: UNKNOWN."
}

// linkOnlyText renders the link-only view: status line, id, link, notice.
func linkOnlyText(lo linkOnlyView) string {
	lines := []string{statusLineFor(lo), "Session: " + lo.SessionID}
	if lo.Link != "" {
		lines = append(lines, "Link: "+lo.Link)
	}
	lines = append(lines, "", lo.Notice)
	return strings.Join(lines, "\n")
}

const maxFetchAttempts = 3

// settledLiveError reports failures that will not change on retry: auth
// rejection, not found / no access (403, 404), other settled 4xx (400, 410),
// version rejection and a malformed id. Command cancellation is checked on the
// context itself in fetchWithRetry: a single request's timeout also matches
// context.DeadlineExceeded and must stay retryable. Everything else
// (transport errors, 5xx, rate limits, anything unclassified) is retried.
func settledLiveError(err error) bool {
	var rejected *api.RejectedError
	return errors.Is(err, api.ErrUnauthorized) ||
		errors.Is(err, api.ErrSessionNotFound) ||
		errors.Is(err, api.ErrVersionUnsupported) ||
		errors.Is(err, api.ErrInvalidSessionID) ||
		errors.As(err, &rejected)
}

const (
	maxRetryAfter    = 60 * time.Second
	retryBackoffBase = time.Second
)

// fetchWithRetry makes up to maxFetchAttempts requests. Settled failures return
// at once; others wait (Retry-After for a 429, else exponential backoff) and
// retry.
func fetchWithRetry(ctx context.Context, client liveViewFetcher, id string, o api.SessionAgentViewOptions) (*api.AgentView, error) {
	var lastErr error
	for attempt := 0; attempt < maxFetchAttempts; attempt++ {
		view, err := client.GetSessionAgentView(ctx, id, o)
		if err == nil {
			return view, nil
		}
		lastErr = err
		if ctx.Err() != nil || settledLiveError(err) || attempt == maxFetchAttempts-1 {
			break
		}
		wait := retryBackoffBase << attempt
		var rl *api.RateLimitedError
		if errors.As(err, &rl) && rl.RetryAfter > 0 {
			wait = rl.RetryAfter
		}
		if wait > maxRetryAfter {
			wait = maxRetryAfter
		}
		if werr := liveViewWait(ctx, wait); werr != nil {
			return nil, werr
		}
	}
	return nil, lastErr
}

// liveShowData is the success payload of `ox conversation show ses_…` when the
// server returned content.
type liveShowData struct {
	SessionID      string             `json:"session_id"`
	ConversationID string             `json:"conversation_id,omitempty"`
	Title          string             `json:"title,omitempty"`
	Status         string             `json:"status"`
	Live           bool               `json:"live"`
	TurnCount      int                `json:"turn_count"`
	NextSince      string             `json:"next_since,omitempty"`
	Truncated      bool               `json:"truncated"`
	Layers         []api.SessionLayer `json:"layers,omitempty"`
	Markdown       string             `json:"markdown"`
}

// liveShowErrorCode maps a live-view failure to a conversation envelope code.
func liveShowErrorCode(err error) string {
	switch {
	case errors.Is(err, api.ErrSessionNotFound):
		return read.ErrCodeNotIndexed
	case errors.Is(err, api.ErrUnauthorized):
		return read.ErrCodeNotAuthenticated
	}
	return read.ErrCodeReadError
}

// runLiveConversationShow serves `ox conversation show ses_…`: one live read
// wrapped in the shared conversation envelope, in the resolved --format, with
// failures as envelope errors too. Link-only (withheld) results are a success
// envelope carrying no title or content.
func runLiveConversationShow(cmd *cobra.Command, ref liveViewRef, format string) error {
	w := cmd.OutOrStdout()
	fail := func(err error) error {
		return finishConversationEnvelope(w, format, read.ErrorEnvelope(read.NewError(liveShowErrorCode(err), liveViewMessage(err).Error())), nil)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	client, ep, err := liveViewNewClient(ctx, ref)
	if err != nil {
		return finishConversationEnvelope(w, format, read.ErrorEnvelope(read.NewError(read.ErrCodeNotAuthenticated, err.Error())), nil)
	}
	contextRepo, projectRoot := liveViewProject()
	view, err := fetchWithRetry(ctx, client, ref.ID, api.SessionAgentViewOptions{ContextRepo: contextRepo})
	if err != nil {
		return fail(err)
	}

	if withheld(view) {
		lo := linkOnly(view, ref.ID, ep)
		env := liveSuccessEnvelope(&lo, safeLiveText(lo.Notice))
		return finishConversationEnvelope(w, format, env, func(out io.Writer, e *read.Envelope) {
			fmt.Fprintln(out, safeLiveText(linkOnlyText(lo)))
		})
	}
	liveViewTrace(projectRoot, view, ref.ID, ep)
	data := &liveShowData{SessionID: view.SessionID, ConversationID: view.ConversationID, Title: view.Title, Status: view.Status, Live: view.Live,
		TurnCount: view.TurnCount, NextSince: view.NextSince, Truncated: view.Truncated, Layers: view.Layers, Markdown: view.Markdown}
	env := liveSuccessEnvelope(data, liveShowGuidance(view, ref.ID, ep))
	return finishConversationEnvelope(w, format, env, func(out io.Writer, e *read.Envelope) {
		fmt.Fprintln(out, safeLiveText(strings.TrimRight(view.Markdown, "\n")))
	})
}

// liveSuccessEnvelope fills what read.Reader's success builder fills: the
// token estimate measured on the marshaled payload (what an agent spends) and
// the guidance. Guidance is sanitized because it is printed as-is.
func liveSuccessEnvelope(data any, guidance string) *read.Envelope {
	env := &read.Envelope{Success: true, Data: data, Guidance: safeLiveText(guidance)}
	if payload, err := json.Marshal(data); err == nil {
		env.TokenEstimate = tokens.EstimateTokens(string(payload))
	}
	return env
}

// liveShowGuidance names the next step for a content read.
//
// The suggested target is the endpoint-qualified /c/ link, not the bare id, so
// continuing a read from another environment (say test.sageox.ai) stays there
// instead of falling back to the project's endpoint.
func liveShowGuidance(view *api.AgentView, id, ep string) string {
	sid := view.SessionID
	if sid == "" {
		sid = id
	}
	if p := weblink.IDTypePrefix(sid); p != "" {
		// only a link that parseLiveViewRef accepts for this same session and
		// host is suggested: localhost and self-hosted endpoints do not
		// round-trip, so those keep the bare id
		if link := weblink.ArtifactURL(ep, sid, "/c/", p); link != "" {
			if back, ok := parseLiveViewRef(link); ok && back.ID == sid && back.Host == endpoint.NormalizeSlug(ep) {
				sid = link
			}
		}
	}
	switch {
	case view.Truncated && view.NextSince != "":
		return fmt.Sprintf("Output was truncated to the budget; read the rest with `ox session view %s --since %s`, or raise --budget.", sid, view.NextSince)
	case view.Live && view.NextSince != "":
		return fmt.Sprintf("Session is still live; read new turns with `ox session view %s --since %s`, or follow with --follow.", sid, view.NextSince)
	}
	return "Read one layer in full with `ox session view " + sid + " --layer <clyr_id>`."
}
