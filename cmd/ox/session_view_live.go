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
	"github.com/sageox/ox/internal/endpoint"
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

// Seams for tests: the authenticated client, and the wait between polls.
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
)

// liveViewProject returns the repo_id and endpoint of the ox project this
// command runs in; repoID is empty when there is none. A variable so tests can
// script it. The server, not the client, decides whether this repo may see the
// session: .sageox/config.json is committed and can be stale or crafted.
var liveViewProject = func() (repoID, ep string) {
	root, err := findProjectRoot()
	if err != nil {
		return "", prResolveEndpoint(nil)
	}
	cfg, err := config.LoadProjectConfig(root)
	if err != nil || cfg == nil {
		return "", prResolveEndpoint(nil)
	}
	return cfg.RepoID, prResolveEndpoint(cfg)
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

// newLiveViewClient resolves the endpoint (the link's host, else the project's
// or default endpoint) and returns an authenticated client for it.
func newLiveViewClient(ctx context.Context, ref liveViewRef) (*api.RepoClient, error) {
	var ep string
	if ref.Host != "" {
		ep = loggedInEndpointForHost(ref.Host)
		if ep == "" {
			return nil, fmt.Errorf("you are not logged in to %s; run `ox login` and retry", ref.Host)
		}
	} else {
		var cfg *config.ProjectConfig
		if root, err := findProjectRoot(); err == nil {
			cfg, _ = config.LoadProjectConfig(root)
		}
		ep = prResolveEndpoint(cfg)
	}
	stored, err := auth.EnsureValidTokenForEndpointContext(ctx, ep, 300)
	if err != nil || stored == nil || stored.AccessToken == "" {
		return nil, fmt.Errorf("not logged in to %s (or the login expired); run `ox login` and retry", endpoint.NormalizeSlug(ep))
	}
	return api.NewRepoClientWithEndpoint(ep).WithAuthToken(stored.AccessToken).WithTimeout(30 * time.Second), nil
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
}

func runLiveSessionView(cmd *cobra.Command, ref liveViewRef) error {
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

	client, err := liveViewNewClient(ctx, ref)
	if err != nil {
		return err
	}
	return streamLiveView(ctx, cmd.OutOrStdout(), client, opts)
}

// liveViewFetcher is the one client method the loop needs.
type liveViewFetcher interface {
	GetSessionAgentView(ctx context.Context, id string, opts api.SessionAgentViewOptions) (*api.AgentView, error)
}

// streamLiveView prints one view, then with --follow keeps polling while the
// session is live, printing only new text, and stops when it leaves live or
// the context is cancelled (Ctrl-C is a clean exit).
func streamLiveView(ctx context.Context, w io.Writer, client liveViewFetcher, opts liveViewOptions) error {
	since := opts.Since
	first := true
	contextRepo, ep := liveViewProject()
	lastStatus := ""
	for {
		wasFirst := first
		view, err := fetchWithRetry(ctx, client, opts.Ref.ID, api.SessionAgentViewOptions{Since: since, Budget: opts.Budget, Layer: opts.Layer, ContextRepo: contextRepo})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if opts.Follow && errors.As(err, new(*api.RateLimitedError)) {
				// still throttled after the retry: keep following
				if werr := liveViewWait(ctx, liveViewFollowInterval); werr != nil {
					return nil
				}
				continue
			}
			return liveViewMessage(err)
		}

		if withheld(view) {
			// link-only: never print markdown, title, turns, or the raw JSON
			lo := linkOnly(view, opts.Ref.ID, ep)
			if first || view.Status != lastStatus {
				if opts.JSON {
					b, _ := json.Marshal(lo)
					fmt.Fprintln(w, string(b))
				} else {
					fmt.Fprintln(w, safeLiveText(linkOnlyText(lo, view.Markdown)))
				}
			}
			lastStatus = view.Status
			first = false
			if !opts.Follow || !(view.Live || view.Status == "live") {
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

		if !opts.Follow {
			return nil
		}
		if !(view.Live || view.Status == "live") {
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

// linkOnlyText renders the link-only view: status line, id, link, notice.
func linkOnlyText(lo linkOnlyView, markdown string) string {
	status := leadingLine(markdown)
	if !strings.HasPrefix(status, "Status:") {
		status = "Status: " + strings.ToUpper(lo.Status)
	}
	lines := []string{status, "Session: " + lo.SessionID}
	if lo.Link != "" {
		lines = append(lines, "Link: "+lo.Link)
	}
	lines = append(lines, "", lo.Notice)
	return strings.Join(lines, "\n")
}

const maxRetryAfter = 60 * time.Second

// fetchWithRetry honors a 429's Retry-After once (default 5s, capped), then
// retries; a second 429 is reported.
func fetchWithRetry(ctx context.Context, client liveViewFetcher, id string, o api.SessionAgentViewOptions) (*api.AgentView, error) {
	view, err := client.GetSessionAgentView(ctx, id, o)
	var rl *api.RateLimitedError
	if !errors.As(err, &rl) {
		return view, err
	}
	wait := rl.RetryAfter
	if wait <= 0 {
		wait = 5 * time.Second
	}
	if wait > maxRetryAfter {
		wait = maxRetryAfter
	}
	if werr := liveViewWait(ctx, wait); werr != nil {
		return nil, werr
	}
	return client.GetSessionAgentView(ctx, id, o)
}
