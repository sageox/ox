package daemon

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/ipcfault"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Why this test exists: take-before-show shipped because the daemon's inbox, the
// hook, and the drain's ack path were each tested alone, against doubles that
// never fail. Here one random sequence of events drives all three together,
// through the shared fault doubles (the IPC transport that delays, drops and
// loses responses; the live server that refuses or loses acks), and four
// invariants are checked after EVERY step:
//
//	(a) a note the server holds an ack for was shown exactly once, and none was shown twice;
//	(b) while opted in and the session lives, no note is lost: pending (leased
//	    included) or shown;
//	(c) a note dropped unshown (opted out) is never acked;
//	(d) an ack obligation is settled only by a 2xx or a permanent 4xx.

// safeClock is a fake clock safe to share between the test and the goroutines
// (IPC server, background drains) that read it.
type safeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSafeClock(t time.Time) *safeClock { return &safeClock{now: t} }

func (c *safeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *safeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type noteModel struct {
	t      *testing.T
	e      *liveEnv
	box    *teammateCommentInbox
	store  *whisperstore.Store
	ipc    *ipcfault.Server
	client *Client
	// shortClient is the hook whose read gives up while the daemon is held.
	shortClient *Client
	// clk is the one injected clock for the lease TTL and for the drains' backoff.
	// The IPC server goroutine reads it while the test advances it, so it is
	// synchronized: an unsynchronized time.Time here was a data race (CI, -race).
	clk *safeClock
	// legacy drives the pre-lease behavior (consume and ack inside take) so the
	// test can be shown to catch it.
	legacy bool

	issued int // IPC requests the hook side has made
	seed   int64
	step   int
	last   string
	next   int

	held     map[string]bool // queued on the server and not acked by it
	inScope  map[string]bool // ox received it while opted in and it was not dropped
	shown    map[string]int
	dropped  map[string]bool // dropped unshown by an opt-out
	optedOut bool
	closed   bool
}

func newNoteModel(t *testing.T, seed int64, legacy bool) *noteModel {
	e, box, store := newCommentEnv(t, "agent-a")
	m := &noteModel{
		t: t, e: e, box: box, store: store, seed: seed, legacy: legacy,
		clk:  newSafeClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		held: map[string]bool{}, inScope: map[string]bool{}, shown: map[string]int{},
		dropped: map[string]bool{},
	}
	box.now = m.clk.Now
	e.live.now = m.clk.Now
	m.ipc = ipcfault.New(t, m.handle)
	// healthy calls get a generous read timeout so a loaded machine never turns
	// one into a fault; the hook that times out uses the short client below
	m.client = NewClientWithSocketAndTimeout(m.ipc.Path(), 10*time.Second)
	m.shortClient = NewClientWithSocketAndTimeout(m.ipc.Path(), 50*time.Millisecond)
	return m
}

// handle is the daemon side of the IPC transport: the real inbox.
func (m *noteModel) handle(line []byte) []byte {
	var msg Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return mustJSON(Response{Error: err.Error()})
	}
	switch msg.Type {
	case MsgTypeTeammateCommentsTake:
		var p TeammateCommentsTakePayload
		_ = json.Unmarshal(msg.Payload, &p)
		var out TeammateCommentsTakeResponse
		var err error
		if m.legacy {
			out.Entries, err = m.legacyTake(p)
		} else {
			out, err = m.box.lease(m.t.Context(), p)
		}
		if err != nil {
			return mustJSON(Response{Error: err.Error()})
		}
		if out.Entries == nil {
			out.Entries = []whisperstore.WhisperEntry{}
		}
		return mustJSON(Response{Success: true, Data: mustJSON(out)})
	case MsgTypeTeammateCommentsConfirm:
		var p TeammateCommentsConfirmPayload
		_ = json.Unmarshal(msg.Payload, &p)
		if !m.legacy {
			if err := m.box.confirm(m.t.Context(), p); err != nil {
				return mustJSON(Response{Error: err.Error()})
			}
		}
		return mustJSON(Response{Success: true})
	}
	return mustJSON(Response{Error: "unexpected " + msg.Type})
}

// legacyTake is the committed take, kept faithfully: it logs each ack and
// deletes each note BEFORE the hook has shown anything.
func (m *noteModel) legacyTake(p TeammateCommentsTakePayload) ([]whisperstore.WhisperEntry, error) {
	waiting, err := m.store.PeekTeammateComments(p.AgentID, teammateCommentPeekLimit)
	if err != nil {
		return nil, err
	}
	var shown []whisperstore.WhisperEntry
	var settled []string
	for _, e := range waiting {
		if len(shown) == sessionstream.MaxTeammateCommentsPerAppend {
			break
		}
		if e.Metadata[tcMetaConversationID] != p.ConversationID {
			settled = append(settled, e.ID)
			continue
		}
		_, ok := m.e.live.trusted(e.Metadata[tcMetaSessionDir])
		if !ok || livesession.RecordTeammateCommentAck(m.e.rdir(), e.ID, p.DeliveredAtSeq) != nil {
			continue
		}
		settled = append(settled, e.ID)
		shown = append(shown, e)
	}
	if err := m.store.DeleteTeammateComments(settled...); err != nil {
		return nil, err
	}
	return shown, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func (m *noteModel) drain() {
	m.e.spool(lineN(100 + m.step))
	m.clk.Advance(time.Minute)
	m.e.drainOnce()
	m.e.srv.Calls()
}

// hook is one PostToolUse hook call as the real hook makes it: take, write the
// notes, then confirm. mode is how it dies.
func (m *noteModel) hook(mode string) {
	seq := int64(10 + m.step)
	client := m.client
	var release chan struct{}
	if mode == "timeout" {
		// the daemon holds the take until the hook's read has given up
		release = make(chan struct{})
		m.ipc.Inject(ipcfault.Fault{Hold: release})
		client = m.shortClient
	}
	resp, err := client.TeammateCommentsTake("agent-a", m.e.conv(), seq)
	if release != nil {
		close(release)
	}
	m.issued++
	m.ipc.WaitServed(m.issued) // a take the hook abandoned still finishes in the daemon
	if err != nil || resp == nil || len(resp.Entries) == 0 {
		return
	}
	if mode == "crash" {
		return // died holding the lease, before writing anything
	}
	var ids []string
	for _, e := range resp.Entries {
		m.shown[e.ID]++ // the envelope was written
		ids = append(ids, e.ID)
	}
	if mode == "confirmLost" {
		m.ipc.Inject(ipcfault.Fault{CloseMidResponse: true})
	}
	_ = m.client.TeammateCommentsConfirm("agent-a", resp.LeaseID, ids)
	m.issued++
	m.ipc.WaitServed(m.issued)
	m.e.live.Wait()
}

func (m *noteModel) logIDs(name string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(m.e.rdir(), name))
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		var a sessionstream.TeammateCommentAck
		if json.Unmarshal([]byte(l), &a) == nil && a.ID != "" {
			out[a.ID] = true
		} else {
			out[l] = true
		}
	}
	return out
}

func (m *noteModel) ackDrain() {
	m.drain()
}

// settledByClient: the client saw a 2xx or a permanent 4xx for an ack request
// naming id. A request whose answer was lost (a dead connection) settles nothing,
// even though the server may have recorded it.
func (m *noteModel) settledByClient(id string) bool {
	for _, at := range m.e.srv.AckAttempts() {
		if (at.Status == http.StatusOK || at.Status == http.StatusBadRequest) && slices.Contains(at.IDs, id) {
			return true
		}
	}
	return false
}

func (m *noteModel) markScope() {
	if m.optedOut {
		return
	}
	for id := range m.held {
		if m.shown[id] == 0 {
			m.inScope[id] = true
		}
	}
}

func (m *noteModel) do(r *rand.Rand) {
	m.step++
	switch n := r.Intn(100); {
	case n < 18 && !m.optedOut:
		m.next++
		id := fmt.Sprintf("tcm_%d", m.next)
		m.e.srv.QueueTeammateComment(srvComment(id, "note "+id))
		m.held[id] = true
		m.last = "deliver " + id
		m.ackDrain()
		m.markScope()
	case n < 40:
		m.last = "hook writes"
		m.hook("write")
	case n < 48:
		m.last = "hook times out"
		m.hook("timeout")
	case n < 54:
		m.last = "hook crashes before confirm"
		m.hook("crash")
	case n < 58:
		m.last = "hook writes, confirm response lost"
		m.hook("confirmLost")
	case n < 72:
		kinds := []string{"ack ok", "ack 503", "ack 4xx", "ack dropped before", "ack dropped after"}
		kind := kinds[r.Intn(len(kinds))]
		m.last = kind
		switch kind {
		case "ack 503":
			m.e.srv.RefuseAcks(1, http.StatusServiceUnavailable, sessionstream.ReasonUnavailable, sessionstream.ActionRetry)
		case "ack 4xx":
			m.e.srv.RefuseAcks(1, http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix)
		case "ack dropped before":
			m.e.srv.DropAcksBeforeRecording(1)
		case "ack dropped after":
			m.e.srv.DropAcksAfterRecording(1)
		}
		m.ackDrain()
		m.markScope()
	case n < 80 && !m.optedOut:
		m.last = "owner opts out"
		for id := range m.held {
			m.e.srv.RemoveTeammateComment(id)
			if m.shown[id] == 0 {
				m.dropped[id] = true
				delete(m.inScope, id)
			}
		}
		m.optedOut = true
		m.ackDrain()
	case n < 88 && m.optedOut:
		m.last = "owner opts back in"
		for id := range m.held {
			m.e.srv.QueueTeammateComment(srvComment(id, "note "+id))
			delete(m.dropped, id)
		}
		m.optedOut = false
		m.ackDrain()
		m.markScope()
	default:
		m.last = "30s pass"
		m.clk.Advance(teammateLeaseTTL + time.Second)
	}
	// the server's own ack record settles a held note
	for id := range m.held {
		if _, ok := m.e.srv.DeliveredAtSeq(id); ok {
			delete(m.held, id)
		}
	}
	m.check()
}

func (m *noteModel) fail(format string, args ...any) {
	m.t.Helper()
	m.t.Fatalf("seed %d, step %d (%s): %s", m.seed, m.step, m.last, fmt.Sprintf(format, args...))
}

func (m *noteModel) check() {
	m.t.Helper()
	m.e.live.Wait() // a confirm kicks a background drain; settle it before judging
	pending := map[string]bool{}
	for _, id := range stillPending(m.t, m.store, "agent-a") {
		pending[id] = true
	}
	for id, n := range m.shown {
		if n > 1 {
			m.fail("(a) note %s was shown %d times", id, n)
		}
	}
	for i := 1; i <= m.next; i++ {
		id := fmt.Sprintf("tcm_%d", i)
		if seq, ok := m.e.srv.DeliveredAtSeq(id); ok && m.shown[id] != 1 {
			m.fail("(a) the server holds an ack (seq %d) for note %s, which was shown %d times", seq, id, m.shown[id])
		}
		if m.dropped[id] {
			if _, ok := m.e.srv.DeliveredAtSeq(id); ok {
				m.fail("(c) note %s was dropped unshown by an opt-out but acked", id)
			}
		}
		if !m.optedOut && !m.closed && m.inScope[id] && m.shown[id] == 0 && !pending[id] {
			m.fail("(b) note %s is lost: not pending, not leased, never shown", id)
		}
	}
	for id := range m.logIDs("teammate-comment-acks-sent") {
		if !m.settledByClient(id) {
			m.fail("(d) the ack for %s was settled without the client seeing a 2xx or a permanent refusal", id)
		}
	}
}

func runNoteModel(t *testing.T, seed int64, legacy bool, steps int) {
	m := newNoteModel(t, seed, legacy)
	r := rand.New(rand.NewSource(seed))
	for range steps {
		m.do(r)
	}
	// the session ends: Close and the daemon's flush must leave no owed receipt
	m.step++
	m.last = "session close"
	m.closed = true
	if _, err := livesession.Close(t.Context(), m.e.cfg, m.e.dir, "", 5*time.Second); err != nil {
		m.fail("close: %v", err)
	}
	for range 3 {
		if _, err := livesession.FlushTeammateAcks(t.Context(), m.e.cfg, m.e.dir, 5*time.Second, time.Now()); err != nil {
			m.fail("flush: %v", err)
		}
	}
	m.check()
	for id, n := range m.shown {
		if !m.settledByClient(id) && n == 1 {
			m.fail("(d) note %s was shown but its receipt never reached the server after close", id)
		}
	}
}

// Fixed seeds: the sequences are reproducible, so a failure names its seed and step.
var noteModelSeeds = []int64{1, 2, 3, 4, 5, 6, 7, 8}

func TestTeammateNotesLifecycleModel(t *testing.T) {
	// OX_NOTE_MODEL_LEGACY=1 runs the model against the committed take-before-show
	// behavior, to prove the model catches it: it must fail.
	legacy := os.Getenv("OX_NOTE_MODEL_LEGACY") == "1"
	for _, seed := range noteModelSeeds {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { runNoteModel(t, seed, legacy, 30) })
	}
}

// Named regression (N1): the hook's read timed out, the daemon finished the
// take anyway, and the note was gone and acked although nothing was shown. Now
// the abandoned lease expires and the next tool call shows the note, once.
func TestTeammateNoteShownOnTheNextToolCallAfterAHookReadTimeout(t *testing.T) {
	m := newNoteModel(t, 0, false)
	m.next++
	m.e.srv.QueueTeammateComment(srvComment("tcm_1", "look at the cache"))
	m.held["tcm_1"] = true
	m.drain()
	m.step = 1
	m.hook("timeout") // delayed past the hook's read timeout: nothing shown
	if m.shown["tcm_1"] != 0 || ackLogExists(m.e) {
		t.Fatalf("shown=%d, acked=%v after a timed-out take; neither may happen", m.shown["tcm_1"], ackLogExists(m.e))
	}
	m.clk.Advance(teammateLeaseTTL + time.Second)
	m.step = 2
	m.hook("write")
	m.e.live.Wait()
	if m.shown["tcm_1"] != 1 {
		t.Fatalf("shown = %d on the next tool call, want 1", m.shown["tcm_1"])
	}
	if seq, ok := m.e.srv.DeliveredAtSeq("tcm_1"); !ok || seq != 12 {
		t.Fatalf("delivered_at_seq = %d, %v; want the confirmed call's seq 12", seq, ok)
	}
}
