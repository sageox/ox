package livesession_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionstream"
)

func TestDrainSpoolEqualsServerStoredBytes(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 700)) // ~700 KB: three requests
	res := e.mustDrain()

	spool := e.spoolBytes()
	if got := e.stored(e.state()); !bytes.Equal(got, spool) {
		t.Fatalf("server holds %d bytes, spool has %d", len(got), len(spool))
	}
	if !res.Done || res.Acked != int64(len(spool)) || res.Sent != int64(len(spool)) {
		t.Fatalf("result = %+v, want done with %d bytes acked", res, len(spool))
	}
	if n := e.srv.CallsTo("POST", "/lines"); n < 3 {
		t.Fatalf("append requests = %d, want >= 3 for a ~700 KB spool", n)
	}
}

func TestDrainDropAfterWriteStoresEachByteOnce(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 40))
	e.srv.DropAfterWrite(1)

	if _, err := e.drain(); err == nil {
		t.Fatal("drain reported success though the reply was lost")
	}
	if got := e.state().Acked; got != 0 {
		t.Fatalf("acked = %d after a lost reply, want 0", got)
	}
	e.mustDrain() // resend of the same bytes

	if got, want := e.stored(e.state()), e.spoolBytes(); !bytes.Equal(got, want) {
		t.Fatalf("server holds %d bytes after resend, want exactly %d", len(got), len(want))
	}
}

func TestDrainResyncForwardOverManyStoredObjects(t *testing.T) {
	e := newEnv(t)
	all := lines(0, 40)
	e.spool(all)
	// 12 stored objects exceed MaxResendObjects, so the server answers resync
	// instead of re-reading them all; the client must jump forward.
	chunk := len(all) / 12
	var parts [][]byte
	for off := 0; off < len(all); {
		end := min(off+chunk, len(all))
		for end < len(all) && all[end-1] != '\n' {
			end++
		}
		parts = append(parts, all[off:end])
		off = end
	}
	if len(parts) <= sessionstream.MaxResendObjects {
		t.Fatalf("test setup: %d objects do not exceed the resend cap", len(parts))
	}
	e.srv.Preload(t, e.st.ConversationID, e.st.LayerID, parts...)

	res := e.mustDrain()
	if !res.Done || res.Acked != int64(len(all)) {
		t.Fatalf("result = %+v, want fully acked at %d", res, len(all))
	}
	if st := e.state(); st.LayerID != e.st.LayerID || !bytes.Equal(e.stored(st), all) {
		t.Fatalf("resync must continue on the same layer with the server bytes intact (layer changed=%v)", st.LayerID != e.st.LayerID)
	}
	resynced := false
	for _, c := range e.srv.Calls() {
		resynced = resynced || c.Status == 409
	}
	if !resynced {
		t.Fatal("no resync answer was exercised")
	}
}

func TestDrainNewLayerRestartsOnNewLayerFromByteZero(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 20))
	e.mustDrain() // acked > 0 on the first layer
	oldLayer, firstPart := e.st.LayerID, e.spoolBytes()
	e.spool(lines(20, 30))
	e.srv.ForceNewLayer(1)

	e.mustDrain()
	st := e.state()
	if st.LayerID == oldLayer {
		t.Fatal("layer id did not change after new_layer")
	}
	if got := e.stored(st); !bytes.Equal(got, e.spoolBytes()) {
		t.Fatalf("new layer holds %d bytes, want the whole %d-byte spool from byte 0", len(got), len(e.spoolBytes()))
	}
	if got := e.srv.Stored(t, st.ConversationID, oldLayer); !bytes.Equal(got, firstPart) {
		t.Fatalf("old layer holds %d bytes, want its original %d", len(got), len(firstPart))
	}
	// restarting at byte 0 means the new layer never sees an offset gap
	refused := 0
	for _, c := range e.srv.Calls() {
		if c.Status == 409 {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("409 answers = %d, want only the forced new_layer", refused)
	}
}

func TestDrainResyncBeyondSpoolFallsBackToNewLayer(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 5))
	e.srv.ForceResync(1 << 40)
	oldLayer := e.st.LayerID

	e.mustDrain()
	st := e.state()
	if st.LayerID == oldLayer || !bytes.Equal(e.stored(st), e.spoolBytes()) {
		t.Fatalf("stored_through past the spool must restart on a new layer; layer changed=%v", st.LayerID != oldLayer)
	}
}

func TestDrainSessionDeletedWipesSpoolAndLeavesOtherFiles(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 5))
	raw := filepath.Join(e.dir, "raw.jsonl")
	if err := os.WriteFile(raw, []byte("v1 raw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.srv.DeleteSession(e.st.ConversationID)

	res := e.mustDrain()
	if res.Status != livesession.StatusDeleted || !res.Done {
		t.Fatalf("result = %+v, want deleted and done", res)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "spool.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("spool still exists after 410: %v", err)
	}
	if b, err := os.ReadFile(raw); err != nil || string(b) != "v1 raw\n" {
		t.Fatalf("raw.jsonl was touched: %q, %v", b, err)
	}
}

// A server-side delete must reach every v1 publisher, including ones in other
// processes (the daemon's finalize and recovery): the drain that hears the 410
// records it in the stream state and holds the session on this machine.
func TestDrainSessionDeletedHoldsTheSessionOffTheLedger(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 5))
	e.srv.DeleteSession(e.st.ConversationID)

	e.mustDrain()
	if !livesession.WasDeletedOnServer(e.dir) {
		t.Fatalf("stream state = %+v, want deleted on server", e.state())
	}
	hold, err := session.ReadHoldMarker(e.dir)
	if err != nil {
		t.Fatalf("no hold after a server-side delete: %v", err)
	}
	if hold.Reason != session.HoldDeletedOnServer {
		t.Fatalf("hold reason = %q, want %q", hold.Reason, session.HoldDeletedOnServer)
	}
}

func TestDrainAuthRefusalKeepsSpoolAndAck(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 5))
	e.srv.RejectAuth(true)

	if _, err := e.drain(); err == nil {
		t.Fatal("401 must surface as a retryable error")
	}
	if st := e.state(); st.Acked != 0 || st.Status != livesession.StatusStreaming {
		t.Fatalf("state after 401 = %+v, want untouched", st)
	}
	e.srv.RejectAuth(false)
	e.mustDrain()
}

func TestDrainRateLimitReportsRetryAfter(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 5))
	e.srv.RateLimit(1, 1500)

	res, err := e.drain()
	if err == nil || res.RetryAfter != 1500*time.Millisecond {
		t.Fatalf("res=%+v err=%v, want an error and RetryAfter 1.5s", res, err)
	}
	e.mustDrain()
}

func TestDrainSessionClosedByServerMarksClosed(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	if res, err := livesession.Close(t.Context(), e.cfg, e.dir, "", testBudget); err != nil || res.Status != livesession.StatusClosed {
		t.Fatalf("close: %+v %v", res, err)
	}
	// the server now refuses writes; a stale local state that still says
	// streaming must settle on closed, not retry forever.
	st := e.state()
	st.Status = livesession.StatusStreaming
	raw, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(e.dir, "stream-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	e.spool(lines(3, 4))

	if res := e.mustDrain(); res.Status != livesession.StatusClosed {
		t.Fatalf("status = %s, want closed", res.Status)
	}
}

func TestDrainConcurrentCallsAreSerializedByLock(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 600))
	e.srv.Latency(40 * time.Millisecond)

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, _ = e.drain() })
	}
	wg.Wait()

	if n := e.srv.MaxConcurrent(); n != 1 {
		t.Fatalf("max concurrent appends = %d, want 1 (live.lock must serialize drains)", n)
	}
	if !bytes.Equal(e.stored(e.state()), e.spoolBytes()) {
		t.Fatal("server bytes differ from the spool after concurrent drains")
	}
}

// Only answers known to settle a stream may stop it (C7). An answer this
// client cannot classify (a new action, or a body without one) keeps the bytes
// and retries like a 5xx; it must never switch off capture.
func TestDrainRefusalsOnlyStopTheStreamWhenKnownToBeFinal(t *testing.T) {
	tests := []struct {
		name   string
		status int
		reason string
		action sessionstream.Action
		retry  bool
	}{
		{name: "unknown action retries", status: 409, reason: "new_reason", action: "rewind", retry: true},
		{name: "body without action retries", status: 400, reason: "", action: "", retry: true},
		{name: "explicit stop disables", status: 403, reason: sessionstream.ReasonForbidden, action: sessionstream.ActionStop},
		{name: "fix disables", status: 400, reason: sessionstream.ReasonInvalidRequest, action: sessionstream.ActionFix},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.spool(lines(0, 3))
			e.srv.RefuseNextAppend(tt.status, tt.reason, tt.action)

			res, err := e.drain()
			st := e.state()
			if !tt.retry {
				if err != nil || st.Status != livesession.StatusDisabled || st.DisabledReason != tt.reason {
					t.Fatalf("res=%+v err=%v state=%+v, want disabled for %q", res, err, st, tt.reason)
				}
				return
			}
			if err == nil || res.Done || st.Status != livesession.StatusStreaming || st.Acked != 0 {
				t.Fatalf("res=%+v err=%v state=%+v, want a retryable error with the stream intact", res, err, st)
			}
			if res := e.mustDrain(); !res.Done || !bytes.Equal(e.stored(e.state()), e.spoolBytes()) {
				t.Fatalf("retry after an unclassified refusal: %+v", res)
			}
		})
	}
}

// A server-side delete must never be forgotten (C3). When neither the hold
// marker nor the stream state can be written, nothing durable knows of the
// delete: the drain must fail, leaving the session to hear the 410 again, not
// report a final status that only ever existed in memory.
func TestDrainServerDeleteThatCannotBeRecordedIsHeardAgain(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only session folder")
	}
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.srv.DeleteSession(e.st.ConversationID)
	if err := os.Chmod(e.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.dir, 0o700) })

	if res, err := e.drain(); err == nil || res.Done {
		t.Fatalf("res=%+v err=%v: an unrecorded delete was reported settled", res, err)
	}
	if livesession.WasDeletedOnServer(e.dir) {
		t.Fatal("test setup: the delete was recorded despite the read-only folder")
	}

	if err := os.Chmod(e.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if res := e.mustDrain(); res.Status != livesession.StatusDeleted {
		t.Fatalf("the next drain: %+v, want the delete heard and recorded", res)
	}
	if !livesession.WasDeletedOnServer(e.dir) {
		t.Fatal("the stream state does not record the server-side delete")
	}
	if _, err := session.ReadHoldMarker(e.dir); err != nil {
		t.Fatalf("no hold after the delete was heard again: %v", err)
	}
}
