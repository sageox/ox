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
	if _, err := os.Stat(filepath.Join(e.dir, "live.ndjson")); !os.IsNotExist(err) {
		t.Fatalf("spool still exists after 410: %v", err)
	}
	if b, err := os.ReadFile(raw); err != nil || string(b) != "v1 raw\n" {
		t.Fatalf("raw.jsonl was touched: %q, %v", b, err)
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
	if err := os.WriteFile(filepath.Join(e.dir, "live.json"), raw, 0o600); err != nil {
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
