package daemon

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// An edge-blocked stream is retried on the usual backoff, but the daemon adds
// no Warn line per retry: livesession says it once per streak at Error and
// again when it clears. In the 2026-11-19 outage the per-retry line carried
// CloudFront's HTML page about 690 times. A pending stream's open (key
// registration first) is blocked the same way before any drain runs.
func TestLiveEdgeBlockedRetriesWithoutALinePerAttempt(t *testing.T) {
	tests := []struct {
		name    string
		pending bool // blocked at startup: the stream never opened
	}{
		{name: "streaming drain"},
		{name: "pending open", pending: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLiveEnv(t)
			logs := &lockedBuffer{}
			logger := slog.New(slog.NewTextHandler(logs, nil))
			e.live.logger = logger
			e.cfg.Logger = logger
			want := ""
			if !tt.pending {
				e.spool(lineN(1))
				e.live.Tick(t.Context())
				e.live.Wait()
				if got := e.stored(); got != lineN(1) {
					t.Fatalf("setup: stored %d bytes before the edge block", len(got))
				}
				want = lineN(1)
			}

			const page = `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01 Transitional//EN"><HTML><BODY>403 ERROR</BODY></HTML>`
			e.spool(lineN(2))
			want += lineN(2)
			e.srv.BlockAtEdge("", 3, "text/html", page)
			for range 3 {
				e.clock = e.clock.Add(10 * time.Minute)
				e.live.Tick(t.Context())
				e.live.Wait()
			}
			e.live.mu.Lock()
			_, backingOff := e.live.backoff[canonicalDir(e.dir)]
			e.live.mu.Unlock()
			if !backingOff {
				t.Fatal("an edge-blocked stream is not backing off")
			}
			if tt.pending && e.state().EdgeBlockedSince.IsZero() {
				t.Fatal("a pending stream blocked at the edge did not record it")
			}
			out := logs.String()
			if strings.Contains(out, "DOCTYPE") {
				t.Fatal("the edge's page reached the daemon log")
			}
			if n := strings.Count(out, "level=WARN msg=live_session_step_failed"); n != 0 {
				t.Fatalf("%d Warn step_failed lines for an edge-blocked stream, want 0", n)
			}
			if n := strings.Count(out, "level=ERROR msg=live_session_edge_blocked"); n != 1 {
				t.Fatalf("%d edge_blocked Error lines over one streak, want 1", n)
			}

			e.clock = e.clock.Add(10 * time.Minute)
			e.live.Tick(t.Context())
			e.live.Wait()
			if got := e.stored(); got != want {
				t.Fatalf("stored %d bytes after the edge cleared, want %d", len(got), len(want))
			}
			if n := strings.Count(logs.String(), "msg=live_session_edge_unblocked"); n != 1 {
				t.Fatalf("%d edge_unblocked lines, want 1", n)
			}
		})
	}
}
