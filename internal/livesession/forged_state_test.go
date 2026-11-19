package livesession_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
)

// forgery is what a Ledger contributor plants: the session cache is inside the
// Ledger checkout, so a force-added stream-state.json (and .held) arrives with
// an ordinary pull.
type forgery struct {
	endpoint string
	cnv      string
	status   livesession.Status
}

// plant writes a forged session folder: a stream state naming f's conversation
// and endpoint, a spool of attacker bytes, and a hold, so every path that acts
// on a stream (drain, close, the held stop's delete) is invited to sign for it.
func plant(t *testing.T, f forgery) (string, livesession.State) {
	t.Helper()
	dir := t.TempDir()
	st := livesession.State{
		ConversationID: f.cnv, LayerID: "clyr_" + uuid.Must(uuid.NewV7()).String(),
		Endpoint: f.endpoint, Status: f.status, SessionID: sessionid.GenerateSessionID(),
		SessionName: filepath.Base(dir), RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now(),
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spool.jsonl"), []byte(`{"type":"msg","n":"attacker"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteHoldMarker(dir, session.HoldManualPublishing, "session_stop"); err != nil {
		t.Fatal(err)
	}
	return dir, st
}

// errorEvents counts ERROR records named event in a text log.
func errorEvents(log *bytes.Buffer, event string) int {
	n := 0
	for line := range strings.Lines(log.String()) {
		if strings.Contains(line, "level=ERROR") && strings.Contains(line, "msg="+event) {
			n++
		}
	}
	return n
}

// Failure prevented (ox #1921 review): a Ledger contributor force-added a
// stream-state.json naming a conversation and an endpoint of their choosing,
// plus .held. After an ordinary pull the next tick signed requests for it with
// this machine's key and login, a DELETE of someone else's conversation among
// them. Only a conversation ox minted here, on the configured endpoint, from
// the folder it was minted for, may be sent anything: a forgery that copies
// one of this machine's own ids into a folder of its own deleted the real
// session's copy. A forgery is refused, logged once at Error, and nothing at
// all reaches any server or the pending-deletes queue.
func TestForgedStreamStateSendsNothing(t *testing.T) {
	e := newEnv(t) // a real session ox opened: this machine's key is enrolled
	// another endpoint this machine also streams to (a second project): the
	// conversation minted there is in this machine's registry, so only the
	// endpoint check stands between a forgery naming it and a signed request
	other := livesessiontest.New(t)
	onOther := e.cfg
	onOther.Endpoint = other.URL
	theirs, err := livesession.Open(t.Context(), onOther, t.TempDir(), livesession.OpenParams{SessionID: sessionid.GenerateSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	// someone else's conversation on the configured server: minted on another
	// machine (its own registry), so this one never minted it
	elsewhere := e.cfg
	elsewhere.DataDir = t.TempDir()
	victim, err := livesession.Open(t.Context(), elsewhere, t.TempDir(), livesession.OpenParams{SessionID: sessionid.GenerateSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	attacker := livesessiontest.New(t) // no key here: a pure attacker endpoint

	for _, tc := range []struct {
		name  string
		f     forgery
		event string
	}{
		{"configured endpoint, a conversation never minted here",
			forgery{e.srv.URL, victim.ConversationID, livesession.StatusStreaming}, "live_session_unknown_conversation"},
		{"pending state for a conversation never minted here",
			forgery{e.srv.URL, victim.ConversationID, livesession.StatusPending}, "live_session_unknown_conversation"},
		{"this machine's own conversation, copied into a folder of the forger's",
			forgery{e.srv.URL, e.st.ConversationID, livesession.StatusStreaming}, "live_session_unknown_conversation"},
		{"a conversation minted here for another endpoint",
			forgery{other.URL, theirs.ConversationID, livesession.StatusStreaming}, "live_session_endpoint_mismatch"},
		{"this machine's own conversation, redirected to another endpoint it holds a key for",
			forgery{other.URL, e.st.ConversationID, livesession.StatusStreaming}, "live_session_endpoint_mismatch"},
		{"attacker endpoint and a conversation never minted here",
			forgery{attacker.URL, victim.ConversationID, livesession.StatusStreaming}, "live_session_endpoint_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, st := plant(t, tc.f)
			var log bytes.Buffer
			cfg := e.cfg
			cfg.Logger = slog.New(slog.NewTextHandler(&log, nil))
			before := map[*livesessiontest.Server]int{e.srv: len(e.srv.Calls()), other: len(other.Calls()), attacker: len(attacker.Calls())}

			// every entry point the daemon and the CLI reach a session folder through
			_, _ = livesession.Open(t.Context(), cfg, dir, livesession.OpenParams{
				SessionID: st.SessionID, SessionName: st.SessionName, RepoID: st.RepoID, Agent: st.Agent, StartedAt: st.StartedAt,
			})
			_, _ = livesession.Drain(t.Context(), cfg, dir, testBudget)
			_, _ = livesession.Drain(t.Context(), cfg, dir, testBudget) // a second tick
			_, _ = livesession.Close(t.Context(), cfg, dir, "", testBudget)
			_, _ = livesession.Abort(t.Context(), cfg, dir, testBudget)
			_, _ = livesession.RetryPendingDeletes(t.Context(), cfg)

			for srv, n := range before {
				if calls := srv.Calls()[n:]; len(calls) != 0 {
					t.Errorf("a forged stream state sent %d signed request(s) to %s: %+v", len(calls), srv.URL, calls)
				}
			}
			if e.srv.IsDeleted(victim.ConversationID) || other.IsDeleted(theirs.ConversationID) || e.srv.IsDeleted(e.st.ConversationID) {
				t.Errorf("a conversation was deleted with this machine's key on a forgery's say-so")
			}
			if refused, err := livesession.RefusedDeletes(t.Context(), cfg); err != nil || len(refused) != 0 {
				t.Errorf("pending-deletes queue = %+v %v; a forged id must never enter the trusted queue", refused, err)
			}
			if n := errorEvents(&log, tc.event); n != 1 {
				t.Errorf("%s logged at Error %d times, want exactly once\n%s", tc.event, n, log.String())
			}
		})
	}

	// the real session still streams, closes and aborts normally
	e.spool(lines(0, 3))
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("the real session did not close: %+v %v", res, err)
	}
	if !bytes.Equal(e.stored(e.state()), e.spoolBytes()) {
		t.Fatal("the real session's bytes did not all reach the server")
	}
	if res, err := livesession.Abort(t.Context(), e.cfg, e.dir, testBudget); err != nil || !res.Deleted || !e.srv.IsDeleted(e.st.ConversationID) {
		t.Fatalf("the real session's abort: %+v %v, want its conversation deleted", res, err)
	}
	if e.srv.IsDeleted(victim.ConversationID) {
		t.Fatal("someone else's conversation was deleted")
	}
}
