package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
)

// plantForged writes what a Ledger contributor can force-add under the
// session cache, which lives inside the Ledger checkout: a streaming stream
// state naming a conversation and endpoint of their choosing, a spool and a
// hold. It arrives with an ordinary pull.
func plantForged(t *testing.T, sessionsDir, name, endpoint, cnv string) string {
	t.Helper()
	dir := filepath.Join(sessionsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(livesession.State{
		ConversationID: cnv, LayerID: "clyr_" + uuid.Must(uuid.NewV7()).String(), Endpoint: endpoint,
		Status: livesession.StatusStreaming, SessionID: sessionid.GenerateSessionID(), SessionName: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string][]byte{"stream-state.json": raw, "spool.jsonl": []byte(lineN(1))} {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.WriteHoldMarker(dir, session.HoldManualPublishing, "session_stop"); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Failure prevented (ox #1921 review): the daemon built each request's config
// from the endpoint a stream-state.json named, and acted on any conversation id
// one named. A forged state plus .held, pulled from the Ledger, made the next
// tick sign a DELETE with this machine's key: of someone else's conversation on
// the real server, or of this machine's own one sent to another endpoint. The
// daemon sends only to its own configured endpoint and only for conversations
// minted here. The real session beside them still streams.
func TestLiveTickSendsNothingForAForgedStreamState(t *testing.T) {
	e := newLiveEnv(t)
	e.spool(lineN(1))
	e.live.Tick(t.Context())
	e.live.Wait()
	mine := e.state()
	if mine.Status != livesession.StatusStreaming {
		t.Fatalf("the real session did not start streaming: %+v", mine)
	}

	// another endpoint this machine also streams to (a second project): its
	// conversation is in this machine's registry, so only the daemon's own
	// endpoint keeps a forgery naming it from being signed and sent there
	other := livesessiontest.New(t)
	onOther := e.cfg
	onOther.Endpoint = other.URL
	theirs, err := livesession.Open(t.Context(), onOther, t.TempDir(), livesession.OpenParams{SessionID: sessionid.GenerateSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	// someone else's conversation on the configured server, minted elsewhere
	elsewhere := e.cfg
	elsewhere.DataDir = t.TempDir()
	victim, err := livesession.Open(t.Context(), elsewhere, t.TempDir(), livesession.OpenParams{SessionID: sessionid.GenerateSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	plantForged(t, e.sessionsDir, "2026-03-04T13-42-attacker-OxAAAA", e.srv.URL, victim.ConversationID)
	plantForged(t, e.sessionsDir, "2026-03-04T13-43-attacker-OxBBBB", other.URL, theirs.ConversationID)
	plantForged(t, e.sessionsDir, "2026-03-04T13-44-attacker-OxCCCC", other.URL, mine.ConversationID)
	// this machine's own id on its own endpoint, copied into the forger's folder
	plantForged(t, e.sessionsDir, "2026-03-04T13-45-attacker-OxDDDD", e.srv.URL, mine.ConversationID)
	victimCalls := e.srv.CallsTo("DELETE", victim.ConversationID) + e.srv.CallsTo("POST", victim.ConversationID)
	otherCalls := len(other.Calls())

	e.spool(lineN(2))
	for range 3 {
		e.clock = e.clock.Add(liveTickInterval)
		e.live.Tick(t.Context())
		e.live.Wait()
	}

	if n := e.srv.CallsTo("DELETE", victim.ConversationID) + e.srv.CallsTo("POST", victim.ConversationID) - victimCalls; n != 0 {
		t.Errorf("signed %d request(s) for someone else's conversation", n)
	}
	if e.srv.IsDeleted(victim.ConversationID) {
		t.Error("someone else's conversation was deleted with this machine's key")
	}
	if calls := other.Calls()[otherCalls:]; len(calls) != 0 {
		t.Errorf("sent %d signed request(s) to an endpoint a stream-state.json named: %+v", len(calls), calls)
	}
	if e.srv.IsDeleted(mine.ConversationID) || other.IsDeleted(theirs.ConversationID) {
		t.Error("this machine's own conversation was deleted on the forged state's say-so")
	}
	if got, want := e.stored(), e.spooled(); got != want {
		t.Errorf("the real session stopped streaming: server holds %q, spool %q", got, want)
	}
}

// plantForgedClosing writes a forged session folder that asks for a close: a
// stream state with status closing, naming a supported agent, endpoint and
// conversation of the forger's choosing, beside a spool worth summarizing. No
// hold, so nothing but provenance stands between it and the summarizer.
func plantForgedClosing(t *testing.T, sessionsDir, name, endpoint, cnv string) string {
	t.Helper()
	dir := filepath.Join(sessionsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(livesession.State{
		ConversationID: cnv, LayerID: "clyr_" + uuid.Must(uuid.NewV7()).String(), Endpoint: endpoint,
		Status: livesession.StatusClosing, SessionID: sessionid.GenerateSessionID(), SessionName: name,
		Agent: "claude-code", RepoID: "repo_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	spool := lineN(1) + lineN(2) + lineN(3) + `{"type":"assistant","text":"a long answer worth a summary"}` + "\n"
	for file, data := range map[string][]byte{"stream-state.json": raw, "spool.jsonl": []byte(spool)} {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Failure prevented (ox #2002 review): the daemon wrote a close's summary with
// the person's own model CLI before Close authorized the stream state. A
// Ledger contributor force-adding a closing state and a nontrivial spool made
// every sweep run the authenticated model over attacker bytes, and the later
// ErrUntrustedState cleared the backoff, so the next sweep ran it again.
// Provenance is checked first, and a refused state backs off.
func TestLiveForgedClosingStateNeverReachesTheSummarizer(t *testing.T) {
	e := newLiveEnv(t)
	logs := e.captureLogs()
	e.spool(lineN(1))
	e.live.Tick(t.Context())
	e.live.Wait()
	mine := e.state()
	if mine.Status != livesession.StatusStreaming {
		t.Fatalf("the real session did not start streaming: %+v", mine)
	}
	elsewhere := e.cfg
	elsewhere.DataDir = t.TempDir()
	victim, err := livesession.Open(t.Context(), elsewhere, t.TempDir(), livesession.OpenParams{SessionID: sessionid.GenerateSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	forged := []string{
		// someone else's conversation on the configured server, minted elsewhere
		plantForgedClosing(t, e.sessionsDir, "2026-03-04T13-46-attacker-OxEEEE", e.srv.URL, victim.ConversationID),
		// this machine's own id, copied into the forger's folder
		plantForgedClosing(t, e.sessionsDir, "2026-03-04T13-47-attacker-OxFFFF", e.srv.URL, mine.ConversationID),
	}
	var mu sync.Mutex
	summarized := map[string]int{}
	e.live.summarize = func(_ context.Context, dir string, _ livesession.State) string {
		mu.Lock()
		summarized[filepath.Base(dir)]++
		mu.Unlock()
		return "SUMMARY-OF-ATTACKER-BYTES"
	}

	// seven sweeps over 30 s: two backoff windows (15 s, then the 30 s cap)
	const ticks = 7
	for i := range ticks {
		if i > 0 {
			e.clock = e.clock.Add(5 * time.Second)
		}
		e.live.Tick(t.Context())
		e.live.Wait()
	}

	mu.Lock()
	defer mu.Unlock()
	for _, dir := range forged {
		name := filepath.Base(dir)
		if n := summarized[name]; n != 0 {
			t.Errorf("%s: the model ran %d time(s) over a forged closing state", name, n)
		}
		if _, backingOff := e.live.backoff[canonicalDir(dir)]; !backingOff {
			t.Errorf("%s: a refused state left no backoff, so the next sweep refuses it again", name)
		}
		attempts := 0
		for _, rec := range logs.records(t, "live_session_step_skipped") {
			if rec["reason"] == "untrusted_state" && rec["conversation_id"] == mustState(t, dir).ConversationID && rec["session_id"] == mustState(t, dir).SessionID {
				attempts++
			}
		}
		if attempts == 0 || attempts > 2 {
			t.Errorf("%s: refused %d time(s) in %d sweeps over 30 s, want 1 or 2 (once per backoff window)", name, attempts, ticks)
		}
	}
	if _, ok := e.srv.ClosedWith(victim.ConversationID); ok {
		t.Error("someone else's conversation was closed from a forged state")
	}
	if _, ok := e.srv.ClosedWith(mine.ConversationID); ok {
		t.Error("this machine's own conversation was closed from a forged folder")
	}
}

func mustState(t *testing.T, dir string) livesession.State {
	t.Helper()
	st, err := livesession.ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
