package daemon

// sync_bubbles_compat_test.go — ox-gzp.30 daemon-side. Pins the
// OX_KB_DISABLE escape-hatch contract on the daemon's kb sync loop, so
// an operator flipping the switch during a rollout incident can trust
// the daemon stops calling the kb API entirely.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
)

// TestSyncBubbles_OXKBDisableNoOps verifies setting OX_KB_DISABLE=1 makes
// the daemon's syncBubbles a complete no-op: ListBubbles is never called,
// no clones happen, no symlinks reconcile.
//
// Failure prevented: the merger respects the env var but the daemon's
// sync loop independently constructs an api.KBClient and would otherwise
// keep hitting /api/v1/kb every cadence — defeating the operator-facing
// emergency switch documented in the kb plan.
func TestSyncBubbles_OXKBDisableNoOps(t *testing.T) {
	t.Setenv("OX_KB_DISABLE", "1")
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	var calls atomic.Int32
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &countingLister{calls: &calls}
	})

	s.syncBubbles(context.Background())

	assert.Equal(t, int32(0), calls.Load(), "OX_KB_DISABLE must short-circuit before ListBubbles is called")
}

// TestSyncBubbles_OXKBDisableEmptyRunsNormally pins the inverse: when
// OX_KB_DISABLE is unset, the daemon DOES call ListBubbles. Otherwise a
// future buggy default that always-disables would silently skip every
// caller's kb sync without producing a test failure.
//
// Failure prevented: a default-on for the env hatch (or a typo in the
// recognizer) silently disabling kb sync for every user.
func TestSyncBubbles_OXKBDisableEmptyRunsNormally(t *testing.T) {
	t.Setenv("OX_KB_DISABLE", "")
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	var calls atomic.Int32
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &countingLister{calls: &calls, err: errors.New("intentional: prevent further reconcile work")}
	})

	s.syncBubbles(context.Background())

	assert.Equal(t, int32(1), calls.Load(), "with OX_KB_DISABLE unset the daemon must call ListBubbles")
}

// countingLister is a stub KBBubbleLister that counts invocations and
// optionally returns a canned error. Used to verify the env-var short-
// circuit decisions independent of any real HTTP plumbing.
type countingLister struct {
	calls *atomic.Int32
	err   error
}

func (c *countingLister) ListBubbles(_ context.Context) ([]api.KB, error) {
	c.calls.Add(1)
	return nil, c.err
}
