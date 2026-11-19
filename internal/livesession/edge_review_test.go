package livesession_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/secretguard"
	"github.com/sageox/ox/internal/sessionid"
)

// Shrinking a stalled batch must never shrink what is scanned together. Three
// stalls cap the next send at 16 KiB; a Helm extraEnv name ends just inside
// that cap and its value is appended past it. Sent piece by piece with each
// piece scanned alone, both halves pass and the pair reaches the server; the
// window a full-size batch covers is scanned whole, so the pair is withheld.
func TestDrainScansTheFullWindowWhenStallsShrinkTheBatch(t *testing.T) {
	e := newEnv(t)
	name := []byte(`{"type":"msg","content":"app.extraEnv[0].name='SERVICE_KEY'"}` + "\n")
	value := []byte(`{"type":"msg","content":"app.extraEnv[0].value='simplecredential'"}` + "\n")
	if secretguard.ValidateContent("sessions/content", joined(name, value)) == nil ||
		livesession.CheckPublicationForTest(e.cfg, name) != nil ||
		livesession.CheckPublicationForTest(e.cfg, value) != nil {
		t.Fatal("test setup: want the pair refused together and each half accepted alone")
	}
	const capAfterStalls = 16 << 10
	head := joined(lines(0, 15), name)
	tail := lines(15, 17)
	if len(head) > capAfterStalls || len(head)+len(lines(15, 16)) <= capAfterStalls {
		t.Fatalf("test setup: the name must end the last whole line under the cap (head %d bytes)", len(head))
	}
	e.spool(joined(head, tail))
	e.srv.BlockAtEdge("", 3, "text/html", cloudFrontPage)
	for i := range 3 {
		if _, err := e.drain(); !errors.Is(err, livesession.ErrEdgeBlocked) {
			t.Fatalf("stalled drain %d: %v", i, err)
		}
	}
	if got := e.state().BatchLimit; got != capAfterStalls {
		t.Fatalf("test setup: cap after 3 stalls = %d, want %d", got, capAfterStalls)
	}
	e.spool(value)

	if res := e.mustDrain(); !res.Done {
		t.Fatalf("drain after the stalls: %+v", res)
	}
	server := e.stored(e.state())
	assertNotContains(t, "server copy", server, "simplecredential")
	assertNotContains(t, "server copy", server, "SERVICE_KEY")
	assertSameBytes(t, "server copy", server, withheldGapFor(joined(head, tail, value)))
}

// A close the edge blocked after every byte was acknowledged sends no append
// again, so only a close reply can end the streak: any reply from SageOx
// itself, a retryable refusal or success, clears it, persists that, and says so.
func TestCloseClearsAnEdgeStreakOnceSageOxAnswers(t *testing.T) {
	tests := []struct {
		name        string
		thenRefused bool // SageOx answers 503 retry before it accepts the close
	}{
		{name: "retryable refusal then success", thenRefused: true},
		{name: "success"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			logs := e.captureLogs()
			e.spool(lines(0, 3))
			e.mustDrain()
			e.srv.BlockAtEdge("", 1, "text/html", cloudFrontPage)
			closeNow := func() (livesession.DrainResult, error) {
				return livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget)
			}

			if _, err := closeNow(); !errors.Is(err, livesession.ErrEdgeBlocked) || strings.Contains(err.Error(), "DOCTYPE") {
				t.Fatalf("close at a blocking edge: %v", err)
			}
			if st := e.state(); st.Status != livesession.StatusClosing || st.EdgeBlockedSince.IsZero() {
				t.Fatalf("state after the blocked close: %+v", st)
			}
			if tt.thenRefused {
				e.srv.SetDown(true)
				if _, err := closeNow(); err == nil || errors.Is(err, livesession.ErrEdgeBlocked) {
					t.Fatalf("close answered 503 by SageOx: %v", err)
				}
				if st := e.state(); !st.EdgeBlockedSince.IsZero() || livesession.StreamNotice(e.dir) != "" {
					t.Fatalf("SageOx answered, yet the edge still reads as blocking: %+v", st)
				}
				e.srv.SetDown(false)
			}
			if res, err := closeNow(); err != nil || !res.Done || res.Status != livesession.StatusClosed {
				t.Fatalf("close: res=%+v err=%v", res, err)
			}
			if st := e.state(); !st.EdgeBlockedSince.IsZero() {
				t.Fatalf("closed with the edge streak still recorded: %+v", st)
			}
			if n := countLines(logs.String(), "live_session_edge_unblocked"); n != 1 {
				t.Fatalf("%d edge_unblocked lines, want 1", n)
			}
		})
	}
}

// The daemon opens a pending stream before it drains it, so an edge 403 on
// key registration or the open itself is handled like one on an append: the
// error carries no body, the streak is logged once and saved in the pending
// state for status output, and it clears when the open succeeds.
func TestOpenAtABlockingEdgeIsRecordedForThePendingStream(t *testing.T) {
	tests := []struct{ name, method string }{
		{name: "key registration", method: "POST"},
		{name: "open", method: "PUT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := livesessiontest.New(t)
			cfg := srv.Config(t)
			logs := &lockedBuffer{}
			cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
			dir := t.TempDir()
			p := livesession.OpenParams{
				SessionID: sessionid.GenerateSessionID(), SessionName: "2026-03-04T13-41-person-a-Oxabc1",
				RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now(),
			}
			if _, err := livesession.Prepare(t.Context(), cfg, dir, p); err != nil {
				t.Fatal(err)
			}
			srv.BlockAtEdge(tt.method, 2, "text/html", cloudFrontPage)
			for i := range 2 {
				_, err := livesession.Open(t.Context(), cfg, dir, p)
				if !errors.Is(err, livesession.ErrEdgeBlocked) || strings.Contains(err.Error(), "DOCTYPE") {
					t.Fatalf("open %d at a blocking edge: %v", i, err)
				}
			}
			st, err := livesession.ReadState(dir)
			if err != nil || st.Status != livesession.StatusPending || st.EdgeBlockedSince.IsZero() {
				t.Fatalf("pending state after blocked opens: %+v err=%v", st, err)
			}
			if !strings.Contains(livesession.StreamNotice(dir), "blocked by the network edge") {
				t.Fatal("a pending stream blocked at the edge shows no status notice")
			}
			out := logs.String()
			if strings.Contains(out, "DOCTYPE") || countLines(out, "live_session_edge_blocked") != 1 {
				t.Fatalf("want one edge_blocked line and no edge page; got %d lines", countLines(out, "live_session_edge_blocked"))
			}

			if st, err := livesession.Open(t.Context(), cfg, dir, p); err != nil || st.Status != livesession.StatusStreaming || !st.EdgeBlockedSince.IsZero() {
				t.Fatalf("open once the edge cleared: %+v err=%v", st, err)
			}
			if n := countLines(logs.String(), "live_session_edge_unblocked"); n != 1 {
				t.Fatalf("%d edge_unblocked lines, want 1", n)
			}
		})
	}
}
