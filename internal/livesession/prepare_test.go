package livesession_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
)

func prepared(t *testing.T) (*livesessiontest.Server, livesession.Config, string, livesession.OpenParams) {
	t.Helper()
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	dir := t.TempDir()
	p := livesession.OpenParams{SessionID: sessionid.GenerateSessionID(), SessionName: "2026-03-04T13-41-person-a-Oxabc1", RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now()}
	if _, err := livesession.Prepare(t.Context(), cfg, dir, p); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return srv, cfg, dir, p
}

func TestPrepareAcceptsSpoolWithoutTouchingTheNetwork(t *testing.T) {
	srv, _, dir, _ := prepared(t)
	if _, err := livesession.Spool(t.Context(), dir, lines(0, 3), livesession.SpoolOptions{}); err != nil {
		t.Fatalf("spool on a pending session: %v", err)
	}
	st, _ := livesession.ReadState(dir)
	if st.Status != livesession.StatusPending {
		t.Fatalf("status = %q, want pending", st.Status)
	}
	if n := len(srv.Calls()); n != 0 {
		t.Fatalf("prepare+spool made %d server calls, want 0", n)
	}
}

func TestOpenPromotesPendingKeepingSpoolAndLayer(t *testing.T) {
	srv, cfg, dir, p := prepared(t)
	want := withPrompt(lines(0, 4))
	if _, err := livesession.Spool(t.Context(), dir, want, livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	before, _ := livesession.ReadState(dir)
	st, err := livesession.Open(t.Context(), cfg, dir, p)
	if err != nil || st.Status != livesession.StatusStreaming || st.LayerID != before.LayerID {
		t.Fatalf("open pending: %+v %v (want streaming on layer %s)", st, err, before.LayerID)
	}
	if _, err := livesession.Drain(t.Context(), cfg, dir, testBudget); err != nil {
		t.Fatal(err)
	}
	if got := srv.Stored(t, st.ConversationID, st.LayerID); !bytes.Equal(got, want) {
		t.Fatalf("server holds %d bytes, want the %d spooled before open", len(got), len(want))
	}
}

func TestPrepareIsIdempotent(t *testing.T) {
	_, cfg, dir, p := prepared(t)
	first, _ := livesession.ReadState(dir)
	if _, err := livesession.Prepare(t.Context(), cfg, dir, p); err != nil {
		t.Fatal(err)
	}
	second, _ := livesession.ReadState(dir)
	if first.LayerID != second.LayerID {
		t.Fatalf("second prepare minted a new layer: %s -> %s", first.LayerID, second.LayerID)
	}
}

func TestClosePendingOpensThenClosesAndOfflineKeepsTheCloseRequest(t *testing.T) {
	srv, cfg, dir, _ := prepared(t)
	if _, err := livesession.Spool(t.Context(), dir, withPrompt(lines(0, 2)), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	srv.SetDown(true)
	if _, err := livesession.Close(t.Context(), cfg, dir, "", testBudget); err == nil {
		t.Fatal("close succeeded against a down server")
	}
	st, _ := livesession.ReadState(dir)
	if st.Status != livesession.StatusPending || !st.CloseRequested {
		t.Fatalf("after offline stop: %+v, want pending with the close request remembered", st)
	}
	srv.SetDown(false)
	res, err := livesession.Close(t.Context(), cfg, dir, "", testBudget)
	if err != nil || !res.Done || res.Status != livesession.StatusClosed {
		t.Fatalf("close after recovery: %+v %v", res, err)
	}
	if _, ok := srv.ClosedWith(st.ConversationID); !ok {
		t.Fatal("server never saw the close")
	}
}

func TestAbortPendingNeverCallsTheServer(t *testing.T) {
	srv, cfg, dir, _ := prepared(t)
	res, err := livesession.Abort(t.Context(), cfg, dir, testBudget)
	if err != nil || res != (livesession.AbortResult{}) {
		t.Fatalf("abort pending: %+v %v", res, err)
	}
	if n := len(srv.Calls()); n != 0 {
		t.Fatalf("abort of a never-opened session made %d calls", n)
	}
	if _, err := livesession.Spool(t.Context(), dir, lines(0, 1), livesession.SpoolOptions{}); !errors.Is(err, livesession.ErrNotStreaming) {
		t.Fatalf("spool after abort: %v", err)
	}
}
