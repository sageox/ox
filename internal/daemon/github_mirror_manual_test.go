package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/stretchr/testify/require"
)

func TestManualMirrorLocalDoesNotRelayOrWriteLedger(t *testing.T) {
	h := newMirrorHarness(t)
	seedColdStart(h)
	var got []githubmirror.Item
	result, err := h.relayer.RunManual(context.Background(), h.target(), ManualMirrorOptions{Days: 180, NoPush: true, IndexLocal: func(_ context.Context, items []githubmirror.Item) (int, error) { got = items; return len(items), nil }})
	require.NoError(t, err)
	require.Len(t, got, 4)
	require.Equal(t, 4, result.Indexed)
	require.Zero(t, h.relay.requestCount())
	_, err = os.Stat(filepath.Join(h.ledgerPath, "data", "github"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(githubmirror.StatePath(h.ledgerPath))
	require.True(t, os.IsNotExist(err))
	require.Equal(t, h.clock().AddDate(0, 0, -180), h.fetcher.since("ListPullRequests"))
}

func TestManualMirrorDoesNotMoveBackgroundCursor(t *testing.T) {
	h := newMirrorHarness(t)
	seedColdStart(h)
	h.run()
	before := h.state()
	h.advance(time.Hour)
	_, err := h.relayer.RunManual(context.Background(), h.target(), ManualMirrorOptions{Days: 30, Full: true})
	require.NoError(t, err)
	after := h.state()
	require.Equal(t, before.PullRequestCursor, after.PullRequestCursor)
	require.Equal(t, before.IssueCursor, after.IssueCursor)
	require.Equal(t, before.LastAttemptAt, after.LastAttemptAt)
	require.Equal(t, h.clock().AddDate(0, 0, -30), h.fetcher.since("ListPullRequests"))
}

func TestManualMirrorEmptyWindowStillDetectsMissingRoute(t *testing.T) {
	h := newMirrorHarness(t)
	h.relay.fail = func(githubmirror.RelayRequest) error { return api.ErrGitHubMirrorUnsupported }
	_, err := h.relayer.RunManual(context.Background(), h.target(), ManualMirrorOptions{Days: 30})
	require.ErrorIs(t, err, api.ErrGitHubMirrorUnsupported)
}

func TestManualNoPushReusesLocalRowsUntilFull(t *testing.T) {
	h := newMirrorHarness(t)
	seedColdStart(h)
	known := make(map[string]githubmirror.ItemState)
	opts := ManualMirrorOptions{Days: 180, NoPush: true, KnownLocal: func(context.Context) (map[string]githubmirror.ItemState, error) { return known, nil }, IndexLocal: func(_ context.Context, items []githubmirror.Item) (int, error) {
		for _, it := range items {
			key := githubmirror.SourceKey("acme", "api", it.Kind, it.Number)
			known[key] = githubmirror.ItemState{UpdatedAt: it.UpdatedAt, CommitMetadata: it.Kind == githubmirror.KindPullRequest}
		}
		return len(items), nil
	}}
	first, err := h.relayer.RunManual(context.Background(), h.target(), opts)
	require.NoError(t, err)
	require.Equal(t, 4, first.Indexed)
	details := h.fetcher.detailCalls()
	second, err := h.relayer.RunManual(context.Background(), h.target(), opts)
	require.NoError(t, err)
	require.Equal(t, 0, second.Indexed)
	require.Equal(t, 4, second.Unchanged)
	require.Equal(t, details, h.fetcher.detailCalls())
	opts.Full = true
	third, err := h.relayer.RunManual(context.Background(), h.target(), opts)
	require.NoError(t, err)
	require.Equal(t, 4, third.Indexed)
	require.Greater(t, h.fetcher.detailCalls(), details)
	require.Zero(t, h.relay.requestCount())
}

func TestManualMirrorRejectsUnavailableInputsBeforeFetching(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*mirrorHarness, *MirrorTarget, *ManualMirrorOptions)
		want  string
	}{
		{"invalid days", func(_ *mirrorHarness, _ *MirrorTarget, o *ManualMirrorOptions) { o.Days = -1 }, "days must be positive"},
		{"missing repository", func(_ *mirrorHarness, target *MirrorTarget, _ *ManualMirrorOptions) { target.Owner = "" }, "repository or token unavailable"},
		{"disabled", func(h *mirrorHarness, _ *MirrorTarget, _ *ManualMirrorOptions) {
			h.relayer.deps.Enabled = func() bool { return false }
		}, "disabled"},
		{"team unavailable", func(h *mirrorHarness, _ *MirrorTarget, _ *ManualMirrorOptions) { h.relayer.deps.Team = nil }, "team context unavailable"},
		{"team missing", func(h *mirrorHarness, _ *MirrorTarget, _ *ManualMirrorOptions) {
			h.relayer.deps.Team = func() (string, string) { return "", "" }
		}, "repository team unavailable"},
		{"auth unavailable", func(h *mirrorHarness, _ *MirrorTarget, _ *ManualMirrorOptions) { h.relayer.deps.AuthToken = nil }, "authentication unavailable"},
		{"auth missing", func(h *mirrorHarness, _ *MirrorTarget, _ *ManualMirrorOptions) {
			h.relayer.deps.AuthToken = func() string { return "" }
		}, "authentication unavailable"},
		{"local sink missing", func(_ *mirrorHarness, _ *MirrorTarget, o *ManualMirrorOptions) { o.NoPush = true }, "local GitHub index unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newMirrorHarness(t)
			target := h.target()
			opts := ManualMirrorOptions{Days: 30}
			tc.setup(h, &target, &opts)
			result, err := h.relayer.RunManual(context.Background(), target, opts)
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, ManualMirrorResult{}, result)
			require.Zero(t, h.totalCalls())
		})
	}
}

func TestManualMirrorReportsFetchAndLocalIndexFailures(t *testing.T) {
	sentinel := errors.New("source unavailable")
	for _, method := range []string{"Repo", "ListPullRequests", "ListIssueComments"} {
		t.Run(method, func(t *testing.T) {
			h := newMirrorHarness(t)
			seedColdStart(h)
			h.run()
			statePath := githubmirror.StatePath(h.ledgerPath)
			before, err := os.ReadFile(statePath)
			require.NoError(t, err)
			h.fetcher.fail = func(got string, _ int) error {
				if got == method {
					return sentinel
				}
				return nil
			}
			result, err := h.relayer.RunManual(context.Background(), h.target(), ManualMirrorOptions{Days: 30, Full: true})
			require.ErrorIs(t, err, sentinel)
			require.Zero(t, result.Accepted)
			after, err := os.ReadFile(statePath)
			require.NoError(t, err)
			require.Equal(t, before, after, "manual failure must not advance the background state")
		})
	}
	t.Run("local cache failure", func(t *testing.T) {
		h := newMirrorHarness(t)
		called := false
		result, err := h.relayer.RunManual(context.Background(), h.target(), ManualMirrorOptions{Days: 30, NoPush: true, KnownLocal: func(context.Context) (map[string]githubmirror.ItemState, error) { return nil, sentinel }, IndexLocal: func(context.Context, []githubmirror.Item) (int, error) { called = true; return 0, nil }})
		require.ErrorIs(t, err, sentinel)
		require.Equal(t, ManualMirrorResult{}, result)
		require.False(t, called)
		require.Zero(t, h.totalCalls())
	})
	t.Run("local sink failure preserves count", func(t *testing.T) {
		h := newMirrorHarness(t)
		seedPRs(h, 2)
		result, err := h.relayer.RunManual(context.Background(), h.target(), ManualMirrorOptions{Days: 30, NoPush: true, IndexLocal: func(_ context.Context, items []githubmirror.Item) (int, error) {
			require.Len(t, items, 2)
			return 1, sentinel
		}})
		require.ErrorIs(t, err, sentinel)
		require.Equal(t, 1, result.Indexed)
		require.Equal(t, 2, result.Listed)
		require.Zero(t, h.relay.requestCount())
	})
}

func TestManualMirrorReportsRelayOutcomesWithoutAdvancingCursor(t *testing.T) {
	for _, mode := range []string{"accepted and current", "rejected", "missing receipt", "relay failure"} {
		t.Run(mode, func(t *testing.T) {
			h := newMirrorHarness(t)
			seedPRs(h, 2)
			failure := errors.New("relay unavailable")
			switch mode {
			case "accepted and current":
				h.relay.verdict = func(it githubmirror.Item) (string, string) {
					if it.Number == 1 {
						return githubmirror.ResultAccepted, ""
					}
					return githubmirror.ResultCurrent, ""
				}
			case "rejected":
				h.relay.verdict = func(it githubmirror.Item) (string, string) {
					if it.Number == 1 {
						return githubmirror.ResultRejected, "flagged"
					}
					return githubmirror.ResultAccepted, ""
				}
			case "missing receipt":
				h.relay.respond = func(githubmirror.RelayRequest) *githubmirror.RelayResponse {
					return &githubmirror.RelayResponse{RepoStatus: githubmirror.RepoEnabled}
				}
			case "relay failure":
				h.relay.fail = func(githubmirror.RelayRequest) error { return failure }
			}
			result, err := h.relayer.RunManual(context.Background(), h.target(), ManualMirrorOptions{Days: 30, Full: true})
			require.Equal(t, 2, result.Listed)
			switch mode {
			case "accepted and current":
				require.NoError(t, err)
				require.Equal(t, 1, result.Accepted)
				require.Equal(t, 1, result.Current)
			case "rejected":
				require.ErrorContains(t, err, "1 GitHub items rejected")
				require.Equal(t, 1, result.Rejected)
				require.Equal(t, 1, result.Accepted)
			case "missing receipt":
				require.ErrorContains(t, err, "no usable result for 2 items")
				require.Zero(t, result.Accepted)
			case "relay failure":
				require.ErrorIs(t, err, failure)
				require.Zero(t, result.Accepted)
			}
			_, statErr := os.Stat(githubmirror.StatePath(h.ledgerPath))
			require.True(t, os.IsNotExist(statErr), "manual relay must not create background cursor state")
		})
	}
}
