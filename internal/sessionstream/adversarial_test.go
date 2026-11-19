package sessionstream

// Adversarial suite for the append algorithm (ADR-247 D3/D7): a reference
// model (the client's intended byte stream plus how much of it is stored)
// drives the real Append against a MemoryStore through random, seeded
// operation sequences that include every fault the contract promises to
// survive. Invariants are checked after EVERY operation so a failure names
// the op that broke it. Seeds are deterministic; a failure prints the seed
// and the op log so it replays exactly.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

func advKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

func advPub(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

func advNow() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }

func advUnix(s int64) time.Time { return time.Unix(s, 0) }

// advStream renders n NDJSON lines with a mix of ASCII and multi-byte UTF-8
// content and varied lengths, so batch boundaries land in awkward places.
func advStream(r *rand.Rand, n int) []byte {
	var b bytes.Buffer
	words := []string{"a", "héllo", "日本語", "🙂", "x\\u0000y", `quote\"d`, "tab\\tbed"}
	for i := range n {
		fmt.Fprintf(&b, `{"seq":%d,"text":"`, i)
		for range 1 + r.IntN(12) {
			b.WriteString(words[r.IntN(len(words))])
		}
		b.WriteString("\"}\n")
	}
	return b.Bytes()
}

// advLineEnd is the offset just past the k-th newline at or after from.
func advLineEnd(s []byte, from int64, k int) int64 {
	end := from
	for range k {
		i := bytes.IndexByte(s[end:], '\n')
		if i < 0 {
			return int64(len(s))
		}
		end += int64(i) + 1
		if end == int64(len(s)) {
			return end
		}
	}
	return end
}

func advKeyFor(team, cnv, layer string) KeyFunc {
	return func(off int64) (string, error) { return BytesKey(team, cnv, layer, off) }
}

// advModel is the oracle: the stream the client means to send and how far the
// server has proven it stored. Nothing else is needed to know what is right.
type advModel struct {
	src []byte
	// stored is the length of the acknowledged prefix the progress row must
	// report (after any recovery the server performs).
	stored int64
	// boundaries are offsets the server returned as stored_through; a correct
	// client only ever resends from one of these.
	boundaries []int64
}

func (m *advModel) addBoundary(off int64) {
	for _, b := range m.boundaries {
		if b == off {
			return
		}
	}
	m.boundaries = append(m.boundaries, off)
}

// advServer is the real algorithm's state: objects plus the progress row.
type advServer struct {
	store *MemoryStore
	cur   Cursor
	key   KeyFunc
}

func (s *advServer) append(off int64, prefix string, body []byte) (Cursor, *Refusal, error) {
	return Append(context.Background(), s.store, s.key, s.cur, AppendRequest{Offset: off, PrefixSHA256: prefix, Body: body, Signature: "v1; test"})
}

// checkInvariants asserts every law that must hold between ANY two ops.
func checkInvariants(t *testing.T, seed uint64, log []string, m *advModel, s *advServer, prefix string) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d after ops %v:\n"+format, append([]any{seed, log}, args...)...)
	}
	// I1: the row's cursor is exactly the model's stored length, and its hash
	// is the stream hash of exactly those bytes of the client's stream.
	if s.cur.StoredThrough != m.stored {
		fail("cursor %d, model stored %d", s.cur.StoredThrough, m.stored)
	}
	if want := BytesSHA256(m.src[:m.stored]); s.cur.PrefixSHA256 != want {
		fail("cursor hash %s, want %s", s.cur.PrefixSHA256, want)
	}
	if s.cur.StoredThrough > 0 {
		h, err := RestoreStreamHash(s.cur.HashState, s.cur.StoredThrough)
		if err != nil || h.Sum() != s.cur.PrefixSHA256 {
			fail("persisted hash state does not restore to the cursor hash: %v", err)
		}
	}
	// I2: every stored object, orphans past the cursor included, is the
	// client's bytes at that offset: no foreign byte was ever stored.
	keys := s.store.Keys(prefix)
	var objects []IndexObject
	for _, k := range keys {
		off, ok := ParseBytesKeyOffset(k)
		if !ok {
			fail("unparseable key %q", k)
		}
		b, _, _ := s.store.Get(context.Background(), k)
		if off+int64(len(b)) > int64(len(m.src)) || !bytes.Equal(b, m.src[off:off+int64(len(b))]) {
			fail("object at %d (%d bytes) is not the client's bytes there", off, len(b))
		}
		objects = append(objects, IndexObject{Offset: off, Size: int64(len(b))})
	}
	// I3: objects tile the stream with no gap and no overlap from 0 through at
	// least the cursor (conservation: stored = sum of object sizes).
	end, err := CheckContiguous(objects)
	if err != nil {
		fail("stored objects are not contiguous: %v", err)
	}
	if end < m.stored {
		fail("objects reach %d, cursor claims %d", end, m.stored)
	}
	// I4: a source-only rebuild from the objects alone reaches the same bytes
	// and never disagrees with the row it is checked against.
	rec, err := RecoverCursor(context.Background(), s.store, keys, s.cur)
	if err != nil {
		fail("recover from objects: %v", err)
	}
	if rec.StoredThrough != end || rec.PrefixSHA256 != BytesSHA256(m.src[:end]) {
		fail("recovered %d/%s, objects reach %d", rec.StoredThrough, rec.PrefixSHA256, end)
	}
}

// expectRefusal asserts a deterministic no that leaves state untouched and
// tells the client where the server really is (never behind the cursor).
func expectRefusal(t *testing.T, seed uint64, log []string, s *advServer, got Cursor, ref *Refusal, err error, reason string, action Action) {
	t.Helper()
	if err != nil || ref == nil {
		t.Fatalf("seed %d after %v: want refusal %s, got err=%v refusal=%v", seed, log, reason, err, ref)
	}
	if ref.Body.Reason != reason || ref.Body.Action != action {
		t.Fatalf("seed %d after %v: refusal %s/%s, want %s/%s", seed, log, ref.Body.Reason, ref.Body.Action, reason, action)
	}
	if ref.Body.StoredThrough == nil || *ref.Body.StoredThrough != s.cur.StoredThrough {
		t.Fatalf("seed %d after %v: refusal stored_through %v, cursor %d", seed, log, ref.Body.StoredThrough, s.cur.StoredThrough)
	}
	if got.StoredThrough != s.cur.StoredThrough || got.PrefixSHA256 != s.cur.PrefixSHA256 {
		t.Fatalf("seed %d after %v: a refusal moved the cursor", seed, log)
	}
}

// TestAdversarialAppendModel is the model-based property test. Each seed runs
// a random op sequence; every op is something a real client, network or
// storage layer can do. Red-first: neutering the prefix check in Append's
// offset==stored branch, the bytes.Equal check in resendFrom, or the
// "existing object" reconcile in appendAtEnd each turns this red.
func TestAdversarialAppendModel(t *testing.T) {
	const team, cnv, layer = "team_xlcr6yzpec", "cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123"
	prefix, err := BytesPrefix(team, cnv, layer)
	if err != nil {
		t.Fatal(err)
	}
	seeds := uint64(120)
	if testing.Short() {
		seeds = 30
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		m := &advModel{src: advStream(r, 40+r.IntN(160)), boundaries: []int64{0}}
		s := &advServer{store: NewMemoryStore(), cur: NewCursor(), key: advKeyFor(team, cnv, layer)}
		var log []string
		srcLen := int64(len(m.src))
		for step := 0; step < 400 && m.stored < srcLen; step++ {
			switch op := r.IntN(15); op {
			case 0, 1, 2, 3: // append the next lines at the stored end
				end := advLineEnd(m.src, m.stored, 1+r.IntN(8))
				log = append(log, fmt.Sprintf("append[%d,%d)", m.stored, end))
				advAppendOK(t, seed, log, m, s, prefix, m.stored, end)
			case 4: // drop after write: stored, but the caller saw a failure
				end := advLineEnd(m.src, m.stored, 1+r.IntN(8))
				log = append(log, fmt.Sprintf("dropAfterWrite[%d,%d)", m.stored, end))
				s.store.FailAfterPut = errors.New("connection reset after write")
				next, ref, err := s.append(m.stored, BytesSHA256(m.src[:m.stored]), m.src[m.stored:end])
				// The fault fires only if a NEW object was written; over an
				// existing orphan the append may reconcile without writing.
				s.store.FailAfterPut = nil
				switch {
				case ref != nil:
					t.Fatalf("seed %d after %v: refused: %+v", seed, log, ref)
				case err == nil:
					advAccept(t, seed, log, m, s, prefix, next, end)
				}
			case 5: // storage fails before anything lands
				end := advLineEnd(m.src, m.stored, 1+r.IntN(8))
				log = append(log, fmt.Sprintf("failPut[%d,%d)", m.stored, end))
				s.store.FailNextPut = errors.New("s3 503")
				if _, _, err := s.append(m.stored, BytesSHA256(m.src[:m.stored]), m.src[m.stored:end]); err == nil {
					t.Fatalf("seed %d after %v: injected put failure not surfaced", seed, log)
				}
				s.store.FailNextPut = nil
			case 6: // duplicate / overlapping resend from any earlier boundary,
				// possibly running past the stored end (stores only the tail)
				from := m.boundaries[r.IntN(len(m.boundaries))]
				end := advLineEnd(m.src, from, 1+r.IntN(20))
				if end-from > MaxBatchBytes {
					continue
				}
				log = append(log, fmt.Sprintf("resend[%d,%d)", from, end))
				next, ref, err := s.append(from, BytesSHA256(m.src[:from]), m.src[from:end])
				switch {
				case err != nil:
					t.Fatalf("seed %d after %v: resend errored: %v", seed, log, err)
				case ref != nil:
					// only a too-long replay may be refused, and it must point
					// the client forward to the true end
					expectRefusal(t, seed, log, s, next, ref, err, ReasonTooLarge, ActionResync)
				default:
					advAccept(t, seed, log, m, s, prefix, next, end)
				}
			case 7: // out of order: an append past the stored end
				gapEnd := advLineEnd(m.src, m.stored, 1+r.IntN(3))
				if gapEnd >= srcLen {
					continue
				}
				end := advLineEnd(m.src, gapEnd, 1)
				log = append(log, fmt.Sprintf("pastEnd@%d", gapEnd))
				next, ref, err := s.append(gapEnd, BytesSHA256(m.src[:gapEnd]), m.src[gapEnd:end])
				expectRefusal(t, seed, log, s, next, ref, err, ReasonOffsetGap, ActionResync)
			case 8: // an offset strictly inside a stored object
				if m.stored < 2 {
					continue
				}
				off := 1 + r.Int64N(m.stored-1)
				if advObjectStarts(s.store, prefix)[off] {
					continue
				}
				end := advLineEnd(m.src, off, 1)
				log = append(log, fmt.Sprintf("midObject@%d", off))
				next, ref, err := s.append(off, BytesSHA256(m.src[:off]), m.src[off:end])
				if err != nil || ref == nil {
					t.Fatalf("seed %d after %v: mid-object resend accepted (err=%v)", seed, log, err)
				}
				if ref.Body.Action != ActionResync && ref.Body.Action != ActionNewLayer && ref.Body.Action != ActionFix {
					t.Fatalf("seed %d after %v: mid-object resend answered %s", seed, log, ref.Body.Action)
				}
				if next.StoredThrough != s.cur.StoredThrough {
					t.Fatalf("seed %d after %v: mid-object resend moved the cursor", seed, log)
				}
			case 9: // a different stream claims the same history
				end := advLineEnd(m.src, m.stored, 1)
				log = append(log, fmt.Sprintf("foreignPrefix@%d", m.stored))
				next, ref, err := s.append(m.stored, BytesSHA256([]byte("someone else's stream")), m.src[m.stored:end])
				expectRefusal(t, seed, log, s, next, ref, err, ReasonPrefixMismatch, ActionNewLayer)
			case 10: // different bytes resent over a stored range
				from := m.boundaries[r.IntN(len(m.boundaries))]
				if from >= m.stored {
					continue
				}
				log = append(log, fmt.Sprintf("conflict@%d", from))
				evil := []byte(`{"seq":-1,"text":"not what was sent"}` + "\n")
				next, ref, err := s.append(from, BytesSHA256(m.src[:from]), evil)
				expectRefusal(t, seed, log, s, next, ref, err, ReasonBytesConflict, ActionNewLayer)
			case 14: // the right bytes resent under a forged history: a stream
				// that merely shares a suffix must never be spliced in
				from := m.boundaries[r.IntN(len(m.boundaries))]
				if from >= m.stored {
					continue
				}
				end := min(advLineEnd(m.src, from, 1+r.IntN(4)), m.stored)
				log = append(log, fmt.Sprintf("forgedPrefixResend@%d", from))
				next, ref, err := s.append(from, BytesSHA256([]byte("forged")), m.src[from:end])
				expectRefusal(t, seed, log, s, next, ref, err, ReasonPrefixMismatch, ActionNewLayer)
			case 11: // the progress row is lost (restore, reset); the server
				// rebuilds it from the objects alone, as api-go does
				log = append(log, "rowLost")
				advRecover(t, seed, log, m, s, prefix, NewCursor())
			case 12: // the row is restored from an older backup
				old := m.boundaries[r.IntN(len(m.boundaries))]
				if old > m.stored {
					continue
				}
				log = append(log, fmt.Sprintf("rowRollback@%d", old))
				advRecover(t, seed, log, m, s, prefix, advCursorAt(t, m.src, old))
			case 13: // malformed body at the end: refused, nothing written
				bad := [][]byte{nil, []byte(`{"a":1}`), []byte("not json\n"), []byte("{\"a\":\"\xff\"}\n"), []byte("{\"a\":\"\x00\"}\n"), []byte("[1]\n")}
				body := bad[r.IntN(len(bad))]
				log = append(log, fmt.Sprintf("malformed(%q)", body))
				keysBefore := len(s.store.Keys(prefix))
				next, ref, err := s.append(m.stored, BytesSHA256(m.src[:m.stored]), body)
				if err != nil || ref == nil || ref.Body.Action != ActionFix {
					t.Fatalf("seed %d after %v: malformed body not refused with fix: err=%v ref=%+v", seed, log, err, ref)
				}
				if next.StoredThrough != s.cur.StoredThrough || len(s.store.Keys(prefix)) != keysBefore {
					t.Fatalf("seed %d after %v: malformed body changed state", seed, log)
				}
			}
			checkInvariants(t, seed, log, m, s, prefix)
		}
		// Liveness: once faults stop, a conforming client drains the rest.
		for guard := 0; m.stored < srcLen; guard++ {
			if guard > 10_000 {
				t.Fatalf("seed %d: never drained", seed)
			}
			end := advLineEnd(m.src, m.stored, 50)
			log = append(log, fmt.Sprintf("drain[%d,%d)", m.stored, end))
			advAppendOK(t, seed, log, m, s, prefix, m.stored, end)
		}
		checkInvariants(t, seed, log, m, s, prefix)
		if s.cur.PrefixSHA256 != BytesSHA256(m.src) {
			t.Fatalf("seed %d: final whole-stream hash differs", seed)
		}
	}
}

// advAppendOK sends src[from:end] and requires a 200.
func advAppendOK(t *testing.T, seed uint64, log []string, m *advModel, s *advServer, prefix string, from, end int64) {
	t.Helper()
	next, ref, err := s.append(from, BytesSHA256(m.src[:from]), m.src[from:end])
	if err != nil || ref != nil {
		t.Fatalf("seed %d after %v: clean append refused: %v %+v", seed, log, err, ref)
	}
	advAccept(t, seed, log, m, s, prefix, next, end)
}

// advAccept checks a 200's cursor against the oracle, then adopts it. A 200
// for bytes ending at end must cover at least max(stored, end), may run past
// end only over objects already stored (an orphan from a lost reply), and a
// pure replay of stored bytes must not move the cursor at all.
func advAccept(t *testing.T, seed uint64, log []string, m *advModel, s *advServer, prefix string, next Cursor, end int64) {
	t.Helper()
	objEnd := advObjectsEnd(s.store, prefix)
	switch {
	case next.StoredThrough < max(m.stored, end):
		t.Fatalf("seed %d after %v: 200 at %d does not cover the sent bytes (end %d, stored %d)", seed, log, next.StoredThrough, end, m.stored)
	case next.StoredThrough > objEnd:
		t.Fatalf("seed %d after %v: 200 at %d claims bytes no object holds (objects end at %d)", seed, log, next.StoredThrough, objEnd)
	case end <= m.stored && next.StoredThrough != m.stored:
		t.Fatalf("seed %d after %v: a replay of stored bytes moved the cursor %d -> %d", seed, log, m.stored, next.StoredThrough)
	}
	s.cur, m.stored = next, next.StoredThrough
	m.addBoundary(m.stored)
}

// advRecover rebuilds the row from objects the way api-go does after losing it.
func advRecover(t *testing.T, seed uint64, log []string, m *advModel, s *advServer, prefix string, known Cursor) {
	t.Helper()
	rec, err := RecoverCursor(context.Background(), s.store, s.store.Keys(prefix), known)
	if err != nil {
		t.Fatalf("seed %d after %v: recovery from objects failed: %v", seed, log, err)
	}
	if rec.StoredThrough < m.stored {
		t.Fatalf("seed %d after %v: recovery lost acknowledged bytes: %d < %d", seed, log, rec.StoredThrough, m.stored)
	}
	// orphans (drop-after-write) are the client's bytes; recovery may run past the old row
	s.cur, m.stored = rec, rec.StoredThrough
	m.addBoundary(m.stored)
}

func advObjectStarts(s *MemoryStore, prefix string) map[int64]bool {
	out := map[int64]bool{}
	for _, k := range s.Keys(prefix) {
		if off, ok := ParseBytesKeyOffset(k); ok {
			out[off] = true
		}
	}
	return out
}

func advObjectsEnd(s *MemoryStore, prefix string) int64 {
	var end int64
	for _, k := range s.Keys(prefix) {
		off, _ := ParseBytesKeyOffset(k)
		b, _, _ := s.Get(context.Background(), k)
		end = max(end, off+int64(len(b)))
	}
	return end
}

func advCursorAt(t *testing.T, src []byte, off int64) Cursor {
	t.Helper()
	if off == 0 {
		return NewCursor()
	}
	h := NewStreamHash()
	h.Write(src[:off])
	state, err := h.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	return Cursor{StoredThrough: off, HashState: state, PrefixSHA256: h.Sum()}
}

// Crash at every step of one append: the store write and the cursor move are
// two persisted steps. Whatever subset of them survived, the next resend must
// converge to the same stream with each byte stored exactly once.
func TestAdversarialAppendCrashAtEveryStep(t *testing.T) {
	const team, cnv, layer = "team_xlcr6yzpec", "cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123"
	prefix, _ := BytesPrefix(team, cnv, layer)
	r := rand.New(rand.NewPCG(7, 11))
	src := advStream(r, 30)
	a, b := advLineEnd(src, 0, 10), advLineEnd(src, advLineEnd(src, 0, 10), 10)
	type crash struct {
		name      string
		putLands  bool // the object write reached storage
		rowMoves  bool // the cursor persisted
		resendBig bool // the client's resend covers more than the lost batch
	}
	var crashes []crash
	for _, put := range []bool{false, true} {
		for _, row := range []bool{false, true} {
			if row && !put {
				continue // the cursor never moves before the object exists
			}
			for _, big := range []bool{false, true} {
				crashes = append(crashes, crash{fmt.Sprintf("put=%v/row=%v/bigResend=%v", put, row, big), put, row, big})
			}
		}
	}
	for _, c := range crashes {
		t.Run(c.name, func(t *testing.T) {
			s := &advServer{store: NewMemoryStore(), cur: NewCursor(), key: advKeyFor(team, cnv, layer)}
			cur, ref, err := s.append(0, EmptyStreamSHA256, src[:a])
			if err != nil || ref != nil {
				t.Fatal(err, ref)
			}
			s.cur = cur
			// the batch [a, b) suffers the crash
			if c.putLands {
				next, ref, err := s.append(a, BytesSHA256(src[:a]), src[a:b])
				if err != nil || ref != nil {
					t.Fatal(err, ref)
				}
				if c.rowMoves {
					s.cur = next
				}
			}
			// the client never heard; it resends from its last ack (a)
			end := b
			if c.resendBig {
				end = int64(len(src))
			}
			next, ref, err := s.append(a, BytesSHA256(src[:a]), src[a:end])
			if err != nil || ref != nil {
				t.Fatalf("resend after crash refused: %v %+v", err, ref)
			}
			s.cur = next
			m := &advModel{src: src, stored: end}
			checkInvariants(t, 0, []string{c.name}, m, s, prefix)
		})
	}
}

// Boundaries of what one append body may be. Every refusal is ActionFix and
// every acceptance is exact; the cap is inclusive.
func TestAdversarialValidateBatchBoundaries(t *testing.T) {
	line := func(n int) []byte { // one JSON line of exactly n bytes incl. "\n"
		if n < len(`{"p":""}`)+1 {
			t.Fatalf("line too short: %d", n)
		}
		pad := n - len(`{"p":""}`) - 1
		return []byte(`{"p":"` + strings.Repeat("x", pad) + `"}` + "\n")
	}
	// a line whose 4-byte runes straddle where a byte-counting batcher might cut
	emoji := []byte(`{"p":"` + strings.Repeat("🙂", (MaxBatchBytes-16)/4) + `"}` + "\n")
	deep := []byte(strings.Repeat(`{"a":`, 20_000) + "1" + strings.Repeat("}", 20_000) + "\n")
	cases := []struct {
		name   string
		body   []byte
		reason string // "" accepts
		line   int
	}{
		{"empty", nil, ReasonEmptyBody, 0},
		{"exactly the cap", line(MaxBatchBytes), "", 0},
		{"one byte over the cap", line(MaxBatchBytes + 1), ReasonTooLarge, 0},
		{"one 27 KB line", line(27 * 1024), "", 0},
		{"multi-byte runes up to the cap", emoji, "", 0},
		{"torn tail", []byte(`{"a":1}` + "\n" + `{"b":`), ReasonTornTail, 2},
		{"only a newline", []byte("\n"), "", 0},
		{"blank lines between objects", []byte("{}\n\n  \n{}\n"), "", 0},
		{"CRLF endings", []byte("{\"a\":1}\r\n{\"b\":2}\r\n"), "", 0},
		{"invalid UTF-8 byte", []byte("{\"a\":\"\xff\"}\n"), ReasonNotNDJSON, 0},
		{"overlong UTF-8 NUL", []byte("{\"a\":\"\xc0\x80\"}\n"), ReasonNotNDJSON, 0},
		{"UTF-8 encoded surrogate half", []byte("{\"a\":\"\xed\xa0\x80\"}\n"), ReasonNotNDJSON, 0},
		{"raw NUL in a string", []byte("{\"a\":\"\x00\"}\n"), ReasonNotNDJSON, 1},
		{"escaped NUL", []byte(`{"a":"\u0000"}` + "\n"), "", 0},
		{"NUL between lines", []byte("{}\n\x00\n"), ReasonNotNDJSON, 2},
		{"array line", []byte("{}\n[1]\n"), ReasonNotNDJSON, 2},
		{"two objects on one line", []byte("{} {}\n"), ReasonNotNDJSON, 1},
		{"trailing garbage", []byte("{}x\n"), ReasonNotNDJSON, 1},
		{"leading BOM", []byte("\ufeff{}\n"), ReasonNotNDJSON, 1},
		{"nesting past the JSON depth limit", deep, ReasonNotNDJSON, 1},
		{"third line bad", []byte("{}\n{}\nnope\n"), ReasonNotNDJSON, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBatch(tc.body)
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var be *BatchError
			if !errors.As(err, &be) || be.Reason != tc.reason {
				t.Fatalf("got %v, want reason %s", err, tc.reason)
			}
			if tc.line > 0 && be.Line != tc.line {
				t.Fatalf("line %d, want %d", be.Line, tc.line)
			}
			// the same body through Append is refused with fix and nothing lands
			s := NewMemoryStore()
			_, ref, aerr := Append(context.Background(), s, advKeyFor("team_xlcr6yzpec", "cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123"),
				NewCursor(), AppendRequest{Offset: 0, PrefixSHA256: EmptyStreamSHA256, Body: tc.body})
			if aerr != nil || ref == nil || ref.Body.Action != ActionFix {
				t.Fatalf("append: err=%v ref=%+v", aerr, ref)
			}
			if want := 400; tc.reason == ReasonTooLarge {
				if ref.Status != 413 {
					t.Fatalf("too large answered %d", ref.Status)
				}
			} else if ref.Status != want {
				t.Fatalf("status %d", ref.Status)
			}
			if len(s.Keys("")) != 0 {
				t.Fatal("a refused body was stored")
			}
		})
	}
}

// Property: the reference client's batcher never splits a line, so every batch
// it emits passes ValidateBatch however multi-byte content falls against the
// 256 KiB cap.
func TestAdversarialBatchLenNeverSplitsALine(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewPCG(seed, 3))
		var p []byte
		for len(p) < 3*MaxBatchBytes {
			runes := r.IntN(10_000)
			switch r.IntN(12) {
			case 0: // lines that end within a few bytes of the cap
				runes = (MaxBatchBytes-16)/4 - r.IntN(4)
			case 1: // a line no batch can carry
				runes = MaxBatchBytes/4 + 1
			}
			p = append(p, `{"p":"`...)
			p = append(p, strings.Repeat("x", r.IntN(4))...)
			p = append(p, strings.Repeat("🙂", runes)...)
			p = append(p, `"}`+"\n"...)
		}
		p = append(p, `{"torn":`...) // a trailing partial line stays queued
		for off := 0; ; {
			n := batchLen(p[off:])
			if n == 0 {
				if !bytes.Equal(p[off:], []byte(`{"torn":`)) {
					t.Fatalf("seed %d: batcher stopped with %d bytes of whole lines left", seed, len(p[off:])-len(`{"torn":`))
				}
				break
			}
			batch := p[off : off+n]
			if len(batch) <= MaxBatchBytes {
				if err := ValidateBatch(batch); err != nil {
					t.Fatalf("seed %d: batch at %d invalid: %v", seed, off, err)
				}
			} else if bytes.Count(batch, []byte{'\n'}) != 1 {
				t.Fatalf("seed %d: an oversized batch carries more than one line", seed)
			}
			off += n
		}
	}
}

// Signatures: what a replay, a tamper, a clock or a foreign key may and may
// not achieve.
func TestAdversarialSignatures(t *testing.T) {
	priv := advKey(1)
	other := advKey(2)
	now := advNow()
	body := []byte(`{"a":1}` + "\n")
	uri := AppendURI("cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123", 40)
	sig := Sign(priv, "POST", uri, now, body)
	pub := advPub(priv)
	parsed, err := ParseSignature(sig)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		pub    ed25519.PublicKey
		method string
		uri    string
		body   []byte
		at     func() int64 // server clock as unix seconds
		want   error
	}{
		{"as sent", pub, "POST", uri, body, nil, nil},
		{"lower-case method verifies (method is canonicalized)", pub, "post", uri, body, nil, nil},
		{"replayed within the window verifies (idempotent append absorbs it)", pub, "POST", uri, body, nil, nil},
		{"body modified by one byte", pub, "POST", uri, []byte(`{"a":2}` + "\n"), nil, ErrSignatureInvalid},
		{"empty body swapped in", pub, "POST", uri, nil, nil, ErrSignatureInvalid},
		{"another offset", pub, "POST", AppendURI("cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123", 0), body, nil, ErrSignatureInvalid},
		{"another layer", pub, "POST", AppendURI("cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29124", 40), body, nil, ErrSignatureInvalid},
		{"duplicated query parameter", pub, "POST", uri + "&offset=0", body, nil, ErrSignatureInvalid},
		{"another method (DELETE)", pub, "DELETE", uri, body, nil, ErrSignatureInvalid},
		{"a key from another login", advPub(other), "POST", uri, body, nil, ErrSignatureInvalid},
		{"exactly 5 minutes late", pub, "POST", uri, body, func() int64 { return now.Unix() + 300 }, nil},
		{"5 minutes and 1 second late", pub, "POST", uri, body, func() int64 { return now.Unix() + 301 }, ErrSignatureExpired},
		{"exactly 5 minutes early", pub, "POST", uri, body, func() int64 { return now.Unix() - 300 }, nil},
		{"5 minutes and 1 second early", pub, "POST", uri, body, func() int64 { return now.Unix() - 301 }, ErrSignatureExpired},
		{"expired AND foreign key reports expired (client re-signs, not fix)", advPub(other), "POST", uri, body, func() int64 { return now.Unix() + 3600 }, ErrSignatureExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := now
			if tc.at != nil {
				at = advUnix(tc.at())
			}
			err := Verify(tc.pub, parsed, tc.method, tc.uri, tc.body, at)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}

// Malformed Ox-Signature headers never panic and never verify.
func TestAdversarialSignatureHeaderParsing(t *testing.T) {
	good := Sign(advKey(1), "POST", "/x", advNow(), nil)
	parts := strings.Split(good, "; ")
	keyPart, tPart, sigPart := parts[1], parts[2], parts[3]
	headers := map[string]error{
		"":    ErrSignatureMissing,
		"   ": ErrSignatureMissing,
		"v2; " + keyPart + "; " + tPart + "; " + sigPart: ErrSignatureMalformed,
		"v1":                            ErrSignatureMalformed,
		"v1; " + keyPart + "; " + tPart: ErrSignatureMalformed,
		"v1; " + keyPart + "; " + keyPart + "; " + tPart + "; " + sigPart:    ErrSignatureMalformed,
		"v1; " + keyPart + "; t=0; " + sigPart:                               ErrSignatureMalformed,
		"v1; " + keyPart + "; t=abc; " + sigPart:                             ErrSignatureMalformed,
		"v1; " + keyPart + "; t=99999999999999999999; " + sigPart:            ErrSignatureMalformed,
		"v1; " + keyPart + "; " + tPart + "; sig=AAAA":                       ErrSignatureMalformed,
		"v1; " + keyPart + "; " + tPart + "; sig=" + strings.Repeat("+", 86): ErrSignatureMalformed,
		"v1; key=; " + tPart + "; " + sigPart:                                ErrSignatureMalformed,
		"v1;" + keyPart + ";" + tPart + ";" + sigPart:                        nil, // tight spacing is fine
		"v1; " + sigPart + "; " + tPart + "; " + keyPart:                     nil, // order is free
		"v1; " + keyPart + "; " + tPart + "; " + sigPart + "; future=1":      nil, // unknown params ignored
		"v1; " + keyPart + "; " + tPart + "; " + sigPart + "; junk":          ErrSignatureMalformed,
	}
	for h, want := range headers {
		got, err := ParseSignature(h)
		if !errors.Is(err, want) || (want == nil && err != nil) {
			t.Errorf("ParseSignature(%q) = %v, want %v", h, err, want)
			continue
		}
		if err == nil && Verify(advPub(advKey(1)), got, "POST", "/x", nil, advNow()) != nil {
			t.Errorf("%q parsed but did not verify", h)
		}
	}
	// extreme timestamps are expired, never a panic or an overflow to "valid"
	for _, ts := range []int64{1, -1, 1 << 62, -(1 << 62), 9223372036854775807, -9223372036854775808} {
		h := "v1; " + keyPart + "; t=" + fmt.Sprint(ts) + "; " + sigPart
		if sig, err := ParseSignature(h); err == nil {
			if err := Verify(advPub(advKey(1)), sig, "POST", "/x", nil, advNow()); !errors.Is(err, ErrSignatureExpired) {
				t.Errorf("t=%d: Verify = %v, want expired", ts, err)
			}
		}
	}
}

// Offsets on the wire: anything but a canonical non-negative integer is
// refused; nothing overflows into a valid offset.
func TestAdversarialParseOffset(t *testing.T) {
	ok := map[string]int64{"0": 0, "1": 1, "9223372036854775807": 9223372036854775807, "040": 40, "+5": 5}
	bad := []string{"", "-1", "-0x1", "1e3", " 5", "5 ", "9223372036854775808", "0x10", "5.0", "NaN", "--1", "5&offset=6"}
	for raw, want := range ok {
		if got, err := ParseOffset(raw); err != nil || got != want {
			t.Errorf("ParseOffset(%q) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range bad {
		if got, err := ParseOffset(raw); err == nil {
			t.Errorf("ParseOffset(%q) accepted as %d", raw, got)
		}
	}
	// a huge offset is a deterministic gap, never a key-builder panic
	s := NewMemoryStore()
	_, ref, err := Append(context.Background(), s, advKeyFor("team_xlcr6yzpec", "cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123"),
		NewCursor(), AppendRequest{Offset: 9223372036854775807, PrefixSHA256: EmptyStreamSHA256, Body: []byte("{}\n")})
	if err != nil || ref == nil || ref.Body.Reason != ReasonOffsetGap {
		t.Fatalf("max offset: err=%v ref=%+v", err, ref)
	}
}

// Source-only recovery under hostile store contents and hostile "known"
// cursors: it either rebuilds exactly or refuses; it never panics and never
// returns a cursor that claims bytes nobody stored.
func TestAdversarialRecoverCursor(t *testing.T) {
	const team, cnv, layer = "team_xlcr6yzpec", "cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123"
	key := advKeyFor(team, cnv, layer)
	prefix, _ := BytesPrefix(team, cnv, layer)
	r := rand.New(rand.NewPCG(5, 5))
	src := advStream(r, 30)
	a, b := advLineEnd(src, 0, 10), advLineEnd(src, advLineEnd(src, 0, 10), 10)
	build := func() *MemoryStore {
		s := NewMemoryStore()
		cur := NewCursor()
		for _, rng := range [][2]int64{{0, a}, {a, b}, {b, int64(len(src))}} {
			next, ref, err := Append(context.Background(), s, key, cur, AppendRequest{Offset: rng[0], PrefixSHA256: cur.PrefixSHA256, Body: src[rng[0]:rng[1]]})
			if err != nil || ref != nil {
				t.Fatal(err, ref)
			}
			cur = next
		}
		return s
	}
	mustKey := func(off int64) string { k, _ := key(off); return k }

	t.Run("a hole in the middle is refused", func(t *testing.T) {
		s := build()
		s.DeletePrefix(mustKey(a))
		if _, err := RecoverCursor(context.Background(), s, s.Keys(prefix), NewCursor()); err == nil {
			t.Fatal("rebuilt a cursor across a hole")
		}
	})
	t.Run("an overlapping object is refused", func(t *testing.T) {
		s := build()
		if _, err := s.PutIfAbsent(context.Background(), mustKey(a-1), []byte("}\n"), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := RecoverCursor(context.Background(), s, s.Keys(prefix), NewCursor()); err == nil {
			t.Fatal("rebuilt a cursor across overlapping objects")
		}
	})
	t.Run("an empty object is refused", func(t *testing.T) {
		s := build()
		_, _ = s.PutIfAbsent(context.Background(), mustKey(int64(len(src))), nil, nil)
		if _, err := RecoverCursor(context.Background(), s, s.Keys(prefix), NewCursor()); err == nil {
			t.Fatal("an empty object was accepted")
		}
	})
	t.Run("a known cursor past the stored end diverges", func(t *testing.T) {
		s := build()
		long := append(append([]byte{}, src...), []byte("{\"more\":1}\n")...)
		if _, err := RecoverCursor(context.Background(), s, s.Keys(prefix), advCursorAt(t, long, int64(len(long)))); !errors.Is(err, ErrRecoveryDiverged) {
			t.Fatalf("got %v, want diverged", err)
		}
	})
	t.Run("a known cursor mid-object with the right hash rebuilds", func(t *testing.T) {
		s := build()
		mid := advLineEnd(src, 0, 3) // inside the first object
		got, err := RecoverCursor(context.Background(), s, s.Keys(prefix), advCursorAt(t, src, mid))
		if err != nil || got.StoredThrough != int64(len(src)) {
			t.Fatalf("got %d, %v", got.StoredThrough, err)
		}
	})
	t.Run("a known cursor with a foreign hash diverges", func(t *testing.T) {
		s := build()
		bad := advCursorAt(t, src, a)
		bad.PrefixSHA256 = BytesSHA256([]byte("other"))
		if _, err := RecoverCursor(context.Background(), s, s.Keys(prefix), bad); !errors.Is(err, ErrRecoveryDiverged) {
			t.Fatalf("got %v, want diverged", err)
		}
	})
	t.Run("a non-stream key is refused, not skipped", func(t *testing.T) {
		s := build()
		keys := append(s.Keys(prefix), prefix+"index.json")
		if _, err := RecoverCursor(context.Background(), s, keys, NewCursor()); err == nil {
			t.Fatal("a foreign key was silently accepted")
		}
	})
	t.Run("a negative known offset is an error, not a panic", func(t *testing.T) {
		s := build()
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("RecoverCursor panicked on a negative known offset: %v", p)
			}
		}()
		bad := Cursor{StoredThrough: -1, PrefixSHA256: EmptyStreamSHA256}
		if _, err := RecoverCursor(context.Background(), s, s.Keys(prefix), bad); err == nil {
			t.Fatal("a negative known offset was accepted")
		}
	})
	t.Run("a store read failure surfaces as an error", func(t *testing.T) {
		s := build()
		if _, err := RecoverCursor(context.Background(), advFailingGet{s}, s.Keys(prefix), NewCursor()); err == nil {
			t.Fatal("a read failure produced a cursor")
		}
	})
}

type advFailingGet struct{ *MemoryStore }

func (advFailingGet) Get(context.Context, string) ([]byte, map[string]string, error) {
	return nil, nil, errors.New("s3 read timeout")
}

// Index contiguity: the finalize gate must refuse every shape that is not a
// gapless tiling from 0.
func TestAdversarialCheckContiguous(t *testing.T) {
	bad := map[string][]IndexObject{
		"starts past 0":   {{Offset: 1, Size: 5}},
		"gap":             {{Offset: 0, Size: 5}, {Offset: 6, Size: 5}},
		"overlap":         {{Offset: 0, Size: 5}, {Offset: 4, Size: 5}},
		"duplicate":       {{Offset: 0, Size: 5}, {Offset: 0, Size: 5}},
		"zero size":       {{Offset: 0, Size: 0}},
		"negative size":   {{Offset: 0, Size: 5}, {Offset: 5, Size: -5}},
		"out of order":    {{Offset: 5, Size: 5}, {Offset: 0, Size: 5}},
		"overflowing sum": {{Offset: 0, Size: 9223372036854775807}, {Offset: -9223372036854775808, Size: 1}},
	}
	for name, objs := range bad {
		if end, err := CheckContiguous(objs); err == nil {
			t.Errorf("%s: accepted, end %d", name, end)
		}
	}
	if end, err := CheckContiguous(nil); err != nil || end != 0 {
		t.Errorf("empty: %d, %v", end, err)
	}
}
