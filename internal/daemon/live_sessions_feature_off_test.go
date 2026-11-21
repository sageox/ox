package daemon

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
)

// Failure prevented: with the rollout flag turned off after ox cached it as on,
// the daemon retried the refused open every 30 s for good, and a stopped
// session re-ran the summary model before every one of those retries.
func TestLiveFeatureOffAtOpenIsNeverRetriedNorResummarized(t *testing.T) {
	cases := []struct {
		name    string
		stopped bool // the SessionEnd hook asked for the close first
	}{
		{"streaming session", false},
		{"stopped session", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLiveEnv(t)
			e.spool(lineN(1))
			if tc.stopped {
				if err := livesession.RequestClose(e.dir); err != nil {
					t.Fatal(err)
				}
			}
			var summaries atomic.Int32
			e.live.summarize = func(context.Context, string, livesession.State) string {
				summaries.Add(1)
				return "a summary"
			}
			cnv := e.state().ConversationID
			e.srv.RefuseNextOpen(http.StatusForbidden, sessionstream.ReasonFeatureOff, sessionstream.ActionStop)

			for range 3 {
				e.live.Tick(t.Context())
				e.live.Wait()
				e.clock = e.clock.Add(liveBackoffCap + liveTickInterval) // past any backoff
			}

			if st := e.state(); st.Status != livesession.StatusDisabled || st.DisabledReason != sessionstream.ReasonFeatureOff {
				t.Fatalf("status %s (%q), want disabled for feature_off", st.Status, st.DisabledReason)
			}
			if n := e.srv.CallsTo(http.MethodPut, cnv); n != 1 {
				t.Fatalf("%d opens sent over 3 ticks, want 1", n)
			}
			want := int32(0)
			if tc.stopped {
				want = 1
			}
			if got := summaries.Load(); got != want {
				t.Fatalf("summary model ran %d times, want %d", got, want)
			}
		})
	}
}
