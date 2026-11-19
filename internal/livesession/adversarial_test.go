package livesession_test

// Adversarial suite for the ox live-session client (ADR-247): a reference
// model of the spool drives the real Spool / Drain / Close against the
// in-process fake API through seeded random sequences of appends, network and
// storage faults, and simulated crashes between persisted steps, checking
// after EVERY op that the server holds a byte-identical prefix of the spool,
// the client never believes more is stored than is, and nothing is lost.
//
// Where the fake cannot answer the way api-go does (close's resync after a
// lost progress row, a reply lost after an open), advAPIGo, a proxy in front
// of the fake, supplies that answer; the fake itself is not modified.
//
// Black-box on purpose: only exported APIs, so concurrent work on the
// package's internals does not break it.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/sageox/ox/internal/sessionstream"
)

// advSession is one streamed session against a fresh fake server.
type advSession struct {
	t   *testing.T
	srv *livesessiontest.Server
	cfg livesession.Config
	dir string
	p   livesession.OpenParams
}

func advOpen(t *testing.T, endpoint func(*livesessiontest.Server) string) *advSession {
	t.Helper()
	srv := livesessiontest.New(t)
	s := &advSession{t: t, srv: srv, cfg: srv.Config(t), dir: filepath.Join(t.TempDir(), "2026-11-18T10-00-person-a-OxAdv1")}
	if endpoint != nil {
		s.cfg.Endpoint = endpoint(srv)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.p = livesession.OpenParams{SessionID: sessionid.GenerateSessionID(), SessionName: filepath.Base(s.dir), RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now()}
	return s
}

func (s *advSession) mustOpen() livesession.State {
	s.t.Helper()
	st, err := livesession.Open(s.t.Context(), s.cfg, s.dir, s.p)
	if err != nil {
		s.t.Fatalf("open: %v", err)
	}
	return st
}

func (s *advSession) state() livesession.State {
	s.t.Helper()
	st, err := livesession.ReadState(s.dir)
	if err != nil {
		s.t.Fatalf("read state: %v", err)
	}
	return st
}

// spool returns the spool's whole lines (a torn tail is not part of the stream).
func (s *advSession) spool() []byte {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, "spool.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return raw[:bytes.LastIndexByte(raw, '\n')+1]
}

func (s *advSession) stored() []byte {
	s.t.Helper()
	st := s.state()
	return s.srv.Stored(s.t, st.ConversationID, st.LayerID)
}

func (s *advSession) drain(budget time.Duration) (livesession.DrainResult, error) {
	return livesession.Drain(s.t.Context(), s.cfg, s.dir, budget)
}

// drainUntilDone retries like the daemon would once faults are gone.
func (s *advSession) drainUntilDone() {
	s.t.Helper()
	for i := 0; ; i++ {
		res, err := s.drain(5 * time.Second)
		if err == nil && res.Done {
			return
		}
		if i == 10 {
			s.t.Fatalf("never drained: res=%+v err=%v", res, err)
		}
	}
}

// advLines renders n NDJSON lines with multi-byte content.
func advLines(r *rand.Rand, from, n int) []byte {
	var b bytes.Buffer
	words := []string{"ok", "héllo", "日本語", "🙂", `q\"x`, "tab\\t"}
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","seq":%d,"text":"`, i)
		for range 1 + r.IntN(8) {
			b.WriteString(words[r.IntN(len(words))])
		}
		b.WriteString("\"}\n")
	}
	return b.Bytes()
}

// advExpect is the oracle for the spool: each entry is either an exact line
// the spool must hold or a declared gap (any gap line).
type advExpect struct {
	line []byte // nil = a gap
}

func advExpectBatch(batch []byte) []advExpect {
	var out []advExpect
	for len(batch) > 0 {
		var line []byte
		if i := bytes.IndexByte(batch, '\n'); i >= 0 {
			line, batch = batch[:i], batch[i+1:]
		} else {
			line, batch = batch, nil
		}
		trimmed := bytes.TrimSpace(line)
		switch {
		case len(trimmed) == 0:
		// json.Valid accepts invalid UTF-8 inside strings; the server does not
		case len(line)+1 > sessionstream.MaxBatchBytes || trimmed[0] != '{' || !utf8.Valid(trimmed) || !json.Valid(trimmed):
			out = append(out, advExpect{})
		default:
			out = append(out, advExpect{line: append(append([]byte{}, line...), '\n')})
		}
	}
	return out
}

func advCheckSpool(t *testing.T, seed uint64, log []string, spool []byte, want []advExpect) {
	t.Helper()
	got := bytes.SplitAfter(spool, []byte{'\n'})
	if len(got) > 0 && len(got[len(got)-1]) == 0 {
		got = got[:len(got)-1]
	}
	if len(got) != len(want) {
		t.Fatalf("seed %d after %v: spool holds %d lines, the model %d", seed, log, len(got), len(want))
	}
	for i, w := range want {
		if w.line != nil {
			if !bytes.Equal(got[i], w.line) {
				t.Fatalf("seed %d after %v: spool line %d is %q, want %q", seed, log, i, got[i], w.line)
			}
			continue
		}
		var gap struct{ Type, Reason string }
		if json.Unmarshal(got[i], &gap) != nil || gap.Type != sessionstream.LineTypeGap || gap.Reason == "" {
			t.Fatalf("seed %d after %v: spool line %d should declare a gap, is %q", seed, log, i, got[i])
		}
	}
}

// TestAdversarialClientModel: random appends, faults and crashes; the server
// always holds a byte-identical prefix of the spool, the client never claims
// more than the server stored, the spool never loses or reorders an accepted
// line, no fault makes the client abandon its layer, and once faults stop the
// whole spool drains and closes with the matching stream hash.
func TestAdversarialClientModel(t *testing.T) {
	seeds := uint64(12)
	if testing.Short() {
		seeds = 3
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			r := rand.New(rand.NewPCG(seed, 0x0c))
			s := advOpen(t, nil)
			st := s.mustOpen()
			var want []advExpect
			torn, burst := false, false
			var log []string
			next := 0
			spoolBatch := func(batch []byte) {
				t.Helper()
				if _, err := livesession.Spool(t.Context(), s.dir, batch, livesession.SpoolOptions{}); err != nil {
					t.Fatalf("seed %d after %v: spool: %v", seed, log, err)
				}
				if torn {
					want, torn = append(want, advExpect{}), false // the healed tail is declared
				}
				want = append(want, advExpectBatch(batch)...)
			}
			for step := 0; step < 40; step++ {
				switch op := r.IntN(14); op {
				case 0, 1, 2, 3:
					n := 1 + r.IntN(12)
					log = append(log, fmt.Sprintf("spool %d", n))
					spoolBatch(advLines(r, next, n))
					next += n
				case 4: // what an adapter can hand over: junk, a giant line, no final newline
					junk := [][]byte{
						[]byte("not json\n"),
						[]byte(`{"big":"` + strings.Repeat("x", sessionstream.MaxBatchBytes) + "\"}\n"),
						[]byte(`{"no_newline":true}`),
						[]byte("{\"bad_utf8\":\"\xff\"}\n"),
						[]byte("\n\n   \n"),
					}
					b := junk[r.IntN(len(junk))]
					log = append(log, fmt.Sprintf("spool junk(%d bytes)", len(b)))
					spoolBatch(append(advLines(r, next, 1), b...))
					next++
				case 5: // killed mid-write: half a line lands at the spool's end
					if torn {
						continue
					}
					log = append(log, "crash mid spool write")
					f, err := os.OpenFile(filepath.Join(s.dir, "spool.jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = f.Write([]byte(`{"type":"assistant","text":"half a li`))
					_ = f.Close()
					torn = true
				case 6:
					log = append(log, "drain")
					_, _ = s.drain(5 * time.Second)
				case 7:
					log = append(log, "drain, reply lost after write")
					s.srv.DropAfterWrite(1 + r.IntN(2))
					_, _ = s.drain(5 * time.Second)
					s.srv.DropAfterWrite(0)
				case 8:
					log = append(log, "drain, 429")
					s.srv.RateLimit(1, 250)
					_, _ = s.drain(5 * time.Second)
					s.srv.RateLimit(0, 0)
				case 9:
					log = append(log, "drain, server down")
					s.srv.SetDown(true)
					if res, err := s.drain(5 * time.Second); err == nil && res.Acked < int64(len(s.spool())) {
						t.Fatalf("seed %d after %v: a drain against a down server with bytes pending reported no error", seed, log)
					}
					s.srv.SetDown(false)
				case 10: // the process dies after the server's 200, before the ack is persisted
					log = append(log, "crash before ack persisted")
					statePath := filepath.Join(s.dir, "stream-state.json")
					before, err := os.ReadFile(statePath)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = s.drain(5 * time.Second)
					if err := os.WriteFile(statePath, before, 0o600); err != nil {
						t.Fatal(err)
					}
				case 11: // the server says resync at its true end (one-shot: armed
					// only when an append will consume it)
					if s.state().Acked == int64(len(s.spool())) {
						continue
					}
					log = append(log, "drain, resync")
					s.srv.ForceResync(int64(len(s.stored())))
					_, _ = s.drain(5 * time.Second)
				case 13: // a burst just past one batch (256 KiB): resends after a
					// crash now meet a server that holds more than the client is
					// sending. Once per seed: big spools are slow under -race.
					if burst {
						continue
					}
					burst = true
					log = append(log, "spool burst")
					spoolBatch([]byte(`{"pad":"` + strings.Repeat("p", sessionstream.MaxBatchBytes-64) + "\"}\n"))
					spoolBatch(advLines(r, next, 40))
					next += 40
				case 12: // a hung server: the drain gives up loudly within its budget
					log = append(log, "drain, hung server")
					s.srv.Hang(true)
					res, err := s.drain(150 * time.Millisecond)
					s.srv.Hang(false)
					if err == nil && res.Acked < int64(len(s.spool())) {
						t.Fatalf("seed %d after %v: a drain against a hung server reported success with bytes pending", seed, log)
					}
				}
				advInvariants(t, seed, log, s, st, want)
			}
			if torn {
				log = append(log, "heal")
				spoolBatch(advLines(r, next, 1))
			}
			s.drainUntilDone()
			advInvariants(t, seed, log, s, st, want)
			spool := s.spool()
			if got := s.stored(); !bytes.Equal(got, spool) {
				t.Fatalf("seed %d after %v: drained server holds %d bytes, spool %d", seed, log, len(got), len(spool))
			}
			res, err := livesession.Close(t.Context(), s.cfg, s.dir, "", 5*time.Second)
			if err != nil || !res.Done {
				t.Fatalf("seed %d after %v: close: %+v %v", seed, log, res, err)
			}
			closed, ok := s.srv.ClosedWith(st.ConversationID)
			if !ok || closed.StreamSHA256 != sessionstream.BytesSHA256(spool) || closed.StoredAtClose != int64(len(spool)) {
				t.Fatalf("seed %d after %v: closed with %+v, spool %d bytes", seed, log, closed, len(spool))
			}
		})
	}
}

func advInvariants(t *testing.T, seed uint64, log []string, s *advSession, opened livesession.State, want []advExpect) {
	t.Helper()
	spool := s.spool()
	advCheckSpool(t, seed, log, spool, want)
	st := s.state()
	stored := s.stored()
	if len(stored) > len(spool) || !bytes.Equal(stored, spool[:len(stored)]) {
		t.Fatalf("seed %d after %v: the server holds bytes that are not a prefix of the spool", seed, log)
	}
	if st.Acked > int64(len(stored)) {
		t.Fatalf("seed %d after %v: the client believes %d bytes are stored, the server holds %d", seed, log, st.Acked, len(stored))
	}
	if st.Acked > 0 && spool[st.Acked-1] != '\n' {
		t.Fatalf("seed %d after %v: acked %d is not a line boundary", seed, log, st.Acked)
	}
	if layers := s.srv.Layers(opened.ConversationID); len(layers) != 1 || st.LayerID != opened.LayerID {
		t.Fatalf("seed %d after %v: a recoverable fault made the client abandon its layer (%d layers)", seed, log, len(layers))
	}
}

// advAPIGo is a proxy in front of the fake that answers the way api-go does
// where the fake differs, and drops replies on routes the fake cannot.
type advAPIGo struct {
	srv      *httptest.Server
	upstream string

	mu sync.Mutex
	// rowLost models api-go after its progress row was lost (a restore, a
	// reset): close compares against the row and answers 409 stream_mismatch,
	// action resync, stored_through 0 (write.go closeLocked) until an append
	// lands and recovers the cursor from the stored objects.
	rowLost bool
	// dropReply forwards the request, then resets the connection: the server
	// did the work, the client never hears it. Keyed by "PUT", "POST close", ...
	dropReply map[string]int
	appends   int
}

func advProxy(t *testing.T, fake *livesessiontest.Server) *advAPIGo {
	t.Helper()
	p := &advAPIGo{upstream: fake.URL, dropReply: map[string]int{}}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func advRoute(r *http.Request) string {
	switch {
	case strings.HasSuffix(r.URL.Path, "/close"):
		return "POST close"
	case strings.HasSuffix(r.URL.Path, "/lines"):
		return "POST append"
	}
	return r.Method
}

func (p *advAPIGo) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	route := advRoute(r)
	p.mu.Lock()
	lost := p.rowLost
	drop := p.dropReply[route] > 0
	if drop {
		p.dropReply[route]--
	}
	if route == "POST append" {
		p.appends++
	}
	p.mu.Unlock()
	if lost && route == "POST close" {
		zero := int64(0)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(sessionstream.ErrorResponse{
			Error: "stored stream does not match the close request", Reason: sessionstream.ReasonStreamMismatch,
			Action: sessionstream.ActionResync, StoredThrough: &zero,
		})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, p.upstream+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if lost && route == "POST append" && resp.StatusCode == http.StatusOK {
		p.mu.Lock()
		p.rowLost = false // api-go rebuilt the cursor from the stored objects
		p.mu.Unlock()
	}
	if drop {
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}

// The client drained everything, then api-go lost the layer's progress row
// (ADR-247: rebuildable state). Close answers resync with stored_through 0.
// The client still holds its whole spool, so resending from 0 lets api-go
// recover and the close completes; ignoring stored_through leaves the session
// open on the server for good (each daemon tick re-closes and is refused).
func TestAdversarialCloseAfterTheServerLostItsProgressRowStillCloses(t *testing.T) {
	var proxy *advAPIGo
	s := advOpen(t, func(fake *livesessiontest.Server) string { proxy = advProxy(t, fake); return proxy.srv.URL })
	st := s.mustOpen()
	r := rand.New(rand.NewPCG(1, 1))
	for i := range 5 {
		if _, err := livesession.Spool(t.Context(), s.dir, advLines(r, 10*i, 10), livesession.SpoolOptions{}); err != nil {
			t.Fatal(err)
		}
		s.drainUntilDone()
	}
	proxy.mu.Lock()
	proxy.rowLost = true
	proxy.mu.Unlock()

	for tick := 0; tick < 4; tick++ { // stop, then the daemon's retries
		if res, err := livesession.Close(t.Context(), s.cfg, s.dir, "", 5*time.Second); err == nil && res.Done {
			break
		}
	}

	if _, ok := s.srv.ClosedWith(st.ConversationID); !ok {
		proxy.mu.Lock()
		appends := proxy.appends
		proxy.mu.Unlock()
		t.Fatalf("the session never closed: close was answered resync with stored_through 0 and the client "+
			"(status %s, acked %d) never resent from it (%d appends in total)", s.state().Status, s.state().Acked, appends)
	}
}

// ADR-024 / ADR-247: loss is declared, never silent. A spool write that fails
// (EACCES, ENOSPC, a read-only volume) must leave a trace in the stream once
// writing works again: the batch itself, or a gap line naming the loss.
func TestAdversarialSpoolWriteFailureIsDeclaredInTheStream(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("reason: root ignores file permissions, so EACCES cannot be simulated")
	}
	s := advOpen(t, nil)
	s.mustOpen()
	r := rand.New(rand.NewPCG(2, 2))
	first, lost, after := advLines(r, 0, 3), advLines(r, 3, 2), advLines(r, 5, 2)
	if _, err := livesession.Spool(t.Context(), s.dir, first, livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	spoolPath := filepath.Join(s.dir, "spool.jsonl")
	if err := os.Chmod(spoolPath, 0o400); err != nil {
		t.Fatal(err)
	}
	_, failErr := livesession.Spool(t.Context(), s.dir, lost, livesession.SpoolOptions{})
	if err := os.Chmod(spoolPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if failErr == nil {
		t.Fatal("a spool append onto a read-only spool reported success")
	}
	if _, err := livesession.Spool(t.Context(), s.dir, after, livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	s.drainUntilDone()
	stored := s.stored()
	if !bytes.Contains(stored, lost) && !bytes.Contains(stored, []byte(`"type":"gap"`)) {
		t.Fatalf("a batch the spool could not write vanished without a gap line:\nstored %q", stored)
	}
}

// The stream state cannot be written (a read-only folder) right after the
// server's 200: the drain must say so, and once writing works again the next
// drain converges with every byte stored exactly once.
func TestAdversarialStateWriteFailureIsLoudAndLosesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("reason: root ignores directory permissions, so EACCES cannot be simulated")
	}
	s := advOpen(t, nil)
	s.mustOpen()
	r := rand.New(rand.NewPCG(3, 3))
	if _, err := livesession.Spool(t.Context(), s.dir, advLines(r, 0, 20), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	res, err := s.drain(5 * time.Second)
	if cerr := os.Chmod(s.dir, 0o755); cerr != nil {
		t.Fatal(cerr)
	}
	if err == nil && res.Done {
		t.Fatalf("a drain whose acknowledgement could not be persisted reported done: %+v", res)
	}
	s.drainUntilDone()
	if !bytes.Equal(s.stored(), s.spool()) {
		t.Fatal("the stream did not converge after the state became writable again")
	}
}

// TestAdvHelperHoldsLiveLock is not a test: TestAdversarialStaleLiveLock
// re-executes the test binary into it to hold a session's live.lock from a
// separate process that is then SIGKILLed.
func TestAdvHelperHoldsLiveLock(t *testing.T) {
	dir := os.Getenv("ADV_HOLD_LIVE_LOCK")
	if dir == "" {
		t.Skip("reason: helper process for TestAdversarialStaleLiveLock, runs only when re-executed by it")
	}
	_ = fileutil.WithFileLockTimeout(context.Background(), filepath.Join(dir, "live.lock"), 5*time.Second, func() error {
		fmt.Println("locked")
		time.Sleep(time.Hour) // hold until killed
		return nil
	})
}

// live.lock held by a process that then dies: the kernel drops a flock with
// its holder, so the next drain must get the lock and stream, never wedge on
// a lock file left behind.
func TestAdversarialStaleLiveLockOfAKilledProcessIsReclaimed(t *testing.T) {
	if testing.Short() {
		t.Skip("reason: re-executes the test binary")
	}
	s := advOpen(t, nil)
	s.mustOpen()
	r := rand.New(rand.NewPCG(4, 4))
	if _, err := livesession.Spool(t.Context(), s.dir, advLines(r, 0, 5), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdvHelperHoldsLiveLock$")
	cmd.Env = append(os.Environ(), "ADV_HOLD_LIVE_LOCK="+s.dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	scan := bufio.NewScanner(out)
	locked := false
	for !locked && scan.Scan() {
		locked = strings.TrimSpace(scan.Text()) == "locked"
	}
	if !locked {
		t.Fatal("helper exited without taking the lock")
	}
	if res, err := s.drain(200 * time.Millisecond); err == nil {
		t.Fatalf("a drain ran while another process held live.lock: %+v", res)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	s.drainUntilDone()
	if !bytes.Equal(s.stored(), s.spool()) {
		t.Fatal("the stream did not resume after the lock holder died")
	}
}

// advHoldLock holds a session's live.lock from this process (a drain wedged
// on a slow server) until release is called.
func advHoldLock(t *testing.T, dir string) (release func()) {
	t.Helper()
	held, done := make(chan struct{}), make(chan struct{})
	go func() {
		_ = fileutil.WithFileLockTimeout(context.Background(), filepath.Join(dir, "live.lock"), 5*time.Second, func() error {
			close(held)
			<-done
			return nil
		})
	}()
	<-held
	var once sync.Once
	release = func() { once.Do(func() { close(done) }) }
	t.Cleanup(release)
	return release
}

// A peer holds live.lock and never lets go: a drain fails loudly within its
// budget instead of hanging or reporting done.
func TestAdversarialWedgedLockHolderMakesDrainFailLoudly(t *testing.T) {
	s := advOpen(t, nil)
	s.mustOpen()
	r := rand.New(rand.NewPCG(5, 5))
	if _, err := livesession.Spool(t.Context(), s.dir, advLines(r, 0, 5), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	release := advHoldLock(t, s.dir)
	res, err := s.drain(150 * time.Millisecond)
	var timeout *fileutil.ErrLockTimeout
	if !errors.As(err, &timeout) || res.Done {
		t.Fatalf("drain under a wedged lock: res=%+v err=%v, want a lock timeout", res, err)
	}
	release()
	s.drainUntilDone()
}

// Abort while a drain holds live.lock for longer than abort's budget (a slow
// server: a drain may hold it for its whole 30 s budget, abort waits 10 s).
// The caller removes the session folder right after Abort returns, so the
// server-side delete must be sent or queued before then (ADR-247 D9).
func TestAdversarialAbortWhileADrainHoldsTheLockStillDeletes(t *testing.T) {
	s := advOpen(t, nil)
	st := s.mustOpen()
	r := rand.New(rand.NewPCG(6, 6))
	if _, err := livesession.Spool(t.Context(), s.dir, advLines(r, 0, 5), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	s.drainUntilDone()
	advHoldLock(t, s.dir)

	res, err := livesession.Abort(t.Context(), s.cfg, s.dir, 300*time.Millisecond)

	if !res.Deleted && !res.Queued {
		t.Fatalf("abort under a held lock neither deleted nor queued the server copy: %+v err=%v", res, err)
	}
	if !s.srv.IsDeleted(st.ConversationID) {
		if _, rerr := livesession.RetryPendingDeletes(t.Context(), s.cfg); rerr != nil || !s.srv.IsDeleted(st.ConversationID) {
			t.Fatalf("queued delete never reached the server: %v", rerr)
		}
	}
}

// The open reached the server but its reply was lost, so the session still
// reads as pending here. An abort must still delete the conversation the
// server created, or an empty "live" session outlives the abort.
func TestAdversarialAbortAfterALostOpenReplyDeletesTheServerCopy(t *testing.T) {
	var proxy *advAPIGo
	s := advOpen(t, func(fake *livesessiontest.Server) string { proxy = advProxy(t, fake); return proxy.srv.URL })
	st, err := livesession.Prepare(t.Context(), s.cfg, s.dir, s.p)
	if err != nil {
		t.Fatal(err)
	}
	// two: net/http transparently retries an idempotent PUT once on a reused
	// connection, and that retry is lost too
	proxy.mu.Lock()
	proxy.dropReply["PUT"] = 2
	proxy.mu.Unlock()
	if _, err := livesession.Open(t.Context(), s.cfg, s.dir, s.p); err == nil {
		t.Fatal("open with a lost reply reported success")
	}
	if len(s.srv.Layers(st.ConversationID)) == 0 {
		t.Fatal("precondition: the server should hold the conversation the lost open created")
	}

	_, _ = livesession.Abort(t.Context(), s.cfg, s.dir, 2*time.Second)
	_, _ = livesession.RetryPendingDeletes(t.Context(), s.cfg)

	if !s.srv.IsDeleted(st.ConversationID) {
		t.Fatalf("abort left the conversation the lost open created on the server (local status %s)", s.state().Status)
	}
}

// The session folder is moved aside (the Ledger clone self-heal does this)
// while a drain is in flight. The drain must not report done. At its new path
// nothing is sent: a stream is trusted only in the folder its conversation is
// bound to, and a copy elsewhere is exactly what a Ledger forgery looks like.
// Moved back, it resumes from its acknowledged offset and converges.
func TestAdversarialSessionFolderMovedMidDrain(t *testing.T) {
	s := advOpen(t, nil)
	st := s.mustOpen()
	r := rand.New(rand.NewPCG(7, 7))
	if _, err := livesession.Spool(t.Context(), s.dir, advLines(r, 0, 30), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	s.srv.Latency(150 * time.Millisecond)
	type result struct {
		res livesession.DrainResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := livesession.Drain(context.Background(), s.cfg, s.dir, 5*time.Second)
		done <- result{res, err}
	}()
	for s.srv.InFlight() == 0 {
		select {
		case got := <-done:
			t.Fatalf("drain finished before the move could happen: %+v", got)
		default:
		}
		time.Sleep(time.Millisecond) // polling a condition, not asserting on time
	}
	moved := filepath.Join(filepath.Dir(s.dir), "moved-aside", filepath.Base(s.dir))
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.dir, moved); err != nil {
		t.Fatal(err)
	}
	got := <-done
	s.srv.Latency(0)
	if got.err == nil && got.res.Done {
		t.Fatalf("a drain whose session folder vanished reported done: %+v", got.res)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "stream-state.json")); err == nil {
		t.Fatal("the drain recreated stream state at the old path: a ghost session")
	}
	sent := len(s.srv.Calls())
	if _, err := livesession.Drain(t.Context(), s.cfg, moved, 2*time.Second); !errors.Is(err, livesession.ErrUntrustedState) {
		t.Fatalf("drain at the folder's new path: %v, want ErrUntrustedState", err)
	}
	if n := len(s.srv.Calls()) - sent; n != 0 {
		t.Fatalf("a stream outside its bound folder sent %d request(s)", n)
	}
	if err := os.Rename(moved, s.dir); err != nil {
		t.Fatal(err)
	}
	s.drainUntilDone()
	if !bytes.Equal(s.srv.Stored(t, st.ConversationID, s.state().LayerID), s.spool()) {
		t.Fatal("the moved session did not converge")
	}
}

// Every serial order of the five things that can happen to a live session at
// once (an abort, a daemon drain, stop's close, a teammate admin's delete on
// the web, a hook spooling a batch). Each holds live.lock for its whole run,
// so the serial orders ARE the possible interleavings at lock granularity.
// Laws: an abort always ends with the server copy deleted; once the client
// knows the session is deleted it sends nothing more; a delete leaves no
// stored bytes; a closed session's close hash matches its spool.
func TestAdversarialLifecycleOpsInEverySerialOrder(t *testing.T) {
	// with and without the admin delete: otherwise it alone could satisfy
	// "the server copy is gone" for every abort
	var orders [][]string
	for _, ops := range [][]string{
		{"abort", "drain", "close", "admin-delete", "spool"},
		{"abort", "drain", "close", "spool"},
	} {
		for _, perm := range advPermutations(len(ops)) {
			var order []string
			for _, i := range perm {
				order = append(order, ops[i])
			}
			orders = append(orders, order)
		}
	}
	if testing.Short() {
		orders = append(orders[:6], orders[len(orders)-6:]...)
	}
	for _, order := range orders {
		t.Run(strings.Join(order, ","), func(t *testing.T) {
			t.Parallel() // each order has its own server, folder and delete queue
			s := advOpen(t, nil)
			st := s.mustOpen()
			r := rand.New(rand.NewPCG(8, 8))
			if _, err := livesession.Spool(t.Context(), s.dir, advLines(r, 0, 4), livesession.SpoolOptions{}); err != nil {
				t.Fatal(err)
			}
			aborted := false
			for _, op := range order {
				deletedBefore := false
				if cur, err := livesession.ReadState(s.dir); err == nil && cur.Status == livesession.StatusDeleted {
					deletedBefore = true
				}
				callsBefore := len(s.srv.Calls())
				switch op {
				case "abort":
					aborted = true
					_, _ = livesession.Abort(t.Context(), s.cfg, s.dir, 2*time.Second)
				case "drain":
					_, _ = s.drain(2 * time.Second)
				case "close":
					_, _ = livesession.Close(t.Context(), s.cfg, s.dir, "", 2*time.Second)
				case "admin-delete":
					s.srv.DeleteSession(st.ConversationID)
				case "spool":
					_, _ = livesession.Spool(t.Context(), s.dir, advLines(r, 100, 2), livesession.SpoolOptions{})
				}
				if deletedBefore && op != "admin-delete" {
					for _, c := range s.srv.Calls()[callsBefore:] {
						if c.Method != http.MethodDelete {
							t.Fatalf("after the client knew the session was deleted, %s sent %s %s", op, c.Method, c.Path)
						}
					}
				}
			}
			_, _ = livesession.RetryPendingDeletes(t.Context(), s.cfg)
			if aborted && !s.srv.IsDeleted(st.ConversationID) {
				t.Fatal("an abort ran, but the server copy survives")
			}
			if s.srv.IsDeleted(st.ConversationID) {
				for _, layer := range s.srv.Layers(st.ConversationID) {
					if n := len(s.srv.Stored(t, st.ConversationID, layer)); n != 0 {
						t.Fatalf("a deleted session still has %d stored bytes", n)
					}
				}
			}
			if cur, err := livesession.ReadState(s.dir); err == nil && aborted && (cur.Status == livesession.StatusStreaming || cur.Status == livesession.StatusClosing) {
				t.Fatalf("after an abort the session still reads %s", cur.Status)
			}
			if closed, ok := s.srv.ClosedWith(st.ConversationID); ok && !s.srv.IsDeleted(st.ConversationID) {
				stored := s.srv.Stored(t, st.ConversationID, closed.LayerID)
				if closed.StreamSHA256 != sessionstream.BytesSHA256(stored) {
					t.Fatal("the close hash does not match the stored stream")
				}
			}
		})
	}
}

func advPermutations(n int) [][]int {
	if n == 1 {
		return [][]int{{0}}
	}
	var out [][]int
	for _, p := range advPermutations(n - 1) {
		for i := 0; i <= len(p); i++ {
			q := append(append(append([]int{}, p[:i]...), n-1), p[i:]...)
			out = append(out, q)
		}
	}
	return out
}
