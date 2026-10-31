package daemon

import (
	"context"
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
