package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/githubmirror"
)

// ManualMirrorResult describes work actually performed by an explicit index request.
type ManualMirrorResult struct {
	Listed, Accepted, Current, Rejected, Unchanged, Expired, Indexed int
}

// ManualMirrorOptions limits one request without changing the background cursor.
type ManualMirrorOptions struct {
	Days       int
	Full       bool
	NoPush     bool
	IndexLocal func(context.Context, []githubmirror.Item) (int, error)
	KnownLocal func(context.Context) (map[string]githubmirror.ItemState, error)
}

// RunProjectGitHubMirror resolves the same feature gate and dependencies used by the daemon.
func RunProjectGitHubMirror(ctx context.Context, projectRoot string, target MirrorTarget, opts ManualMirrorOptions) (ManualMirrorResult, error) {
	r := newProjectGitHubMirrorRelayer(projectRoot, nil, slog.Default())
	r.deps.AuthToken = func() string {
		token, err := auth.EnsureValidTokenForEndpointContext(ctx, endpoint.GetForProject(projectRoot), 300)
		if err != nil || token == nil {
			return ""
		}
		return token.AccessToken
	}
	return r.RunManual(ctx, target, opts)
}

// RunManual fetches a bounded window and either relays or indexes locally.
// Its in-memory state cannot advance or overwrite the daemon's cursor.
func (r *GitHubMirrorRelayer) RunManual(ctx context.Context, target MirrorTarget, opts ManualMirrorOptions) (ManualMirrorResult, error) {
	var result ManualMirrorResult
	if opts.Days <= 0 {
		return result, errors.New("days must be positive")
	}
	if target.Owner == "" || target.Repo == "" || target.GitHubToken == "" {
		return result, errors.New("GitHub repository or token unavailable")
	}
	var teamRef string
	var relay MirrorRelayClient
	if !opts.NoPush {
		if r.deps.Enabled == nil || !r.deps.Enabled() {
			return result, errors.New("GitHub bulletin mirror is disabled or settings are unavailable")
		}
		if r.deps.Team == nil {
			return result, errors.New("team context unavailable")
		}
		teamRef, _ = r.deps.Team()
		if teamRef == "" {
			return result, errors.New("repository team unavailable")
		}
		if r.deps.AuthToken == nil {
			return result, errors.New("SageOx authentication unavailable")
		}
		token := r.deps.AuthToken()
		if token == "" {
			return result, errors.New("SageOx authentication unavailable")
		}
		relay = r.deps.NewRelay(token)
	} else if opts.IndexLocal == nil {
		return result, errors.New("local GitHub index unavailable")
	}
	st := r.loadState(target.LedgerPath, target.Owner+"/"+target.Repo, teamRef)
	if opts.Full || opts.NoPush {
		st.Items = make(map[string]githubmirror.ItemState)
	}
	if opts.NoPush && !opts.Full && opts.KnownLocal != nil {
		known, err := opts.KnownLocal(ctx)
		if err != nil {
			return result, err
		}
		st.Items = known
	}
	now := r.deps.Now()
	// Explicit indexing is not subject to the background cycle's 100-item budget.
	deps := r.deps
	deps.DetailBudget = int(^uint(0) >> 1)
	manual := NewGitHubMirrorRelayer(deps)
	c := &mirrorCycle{relayer: manual, target: target, teamRef: teamRef, fetcher: r.deps.NewFetcher(target.GitHubToken), relay: relay, st: st, now: now, timeout: r.deps.CycleTimeout, log: r.deps.Logger, manualSince: now.AddDate(0, 0, -opts.Days), localOnly: opts.NoPush}
	if err := c.refreshRepo(ctx); err != nil {
		return result, err
	}
	candidates, err := c.list(ctx)
	result.Listed = len(candidates)
	if err != nil {
		return result, err
	}
	pending, err := c.collect(ctx, candidates)
	if err != nil {
		return result, err
	}
	result.Unchanged, result.Expired = c.skipped+c.unchanged, c.expired
	if opts.NoPush {
		items := make([]githubmirror.Item, 0, len(pending))
		for _, p := range pending {
			items = append(items, p.item)
		}
		result.Indexed, err = opts.IndexLocal(ctx, items)
		return result, err
	}
	if len(pending) == 0 {
		// Even an empty window must detect an older server with no relay route.
		_, probeErr := relay.RelayGitHubMirrorItems(ctx, teamRef, githubmirror.RelayRequest{RepoID: target.RepoID, Repo: *st.RepoMeta, Items: []githubmirror.Item{}})
		if probeErr != nil && !errors.Is(probeErr, api.ErrGitHubMirrorValidation) {
			return result, probeErr
		}
	}
	if err := c.relayPending(ctx, pending); err != nil {
		return result, err
	}
	result.Accepted, result.Current, result.Rejected = c.accepted, c.current, c.rejected
	if c.missing > 0 {
		return result, fmt.Errorf("relay returned no usable result for %d items", c.missing)
	}
	if c.rejected > 0 {
		return result, fmt.Errorf("%d GitHub items rejected by bulletin", c.rejected)
	}
	return result, nil
}
