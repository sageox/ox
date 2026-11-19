package sessionstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"
)

const (
	tTeam  = "team_xlcr6yzpec"
	tConv  = "cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122"
	tLayer = "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123"
)

func tKey(off int64) (string, error) { return BytesKey(tTeam, tConv, tLayer, off) }

func lines(from, to int) []byte {
	var b bytes.Buffer
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "{\"seq\":%d,\"type\":\"user\",\"content\":\"line %d\"}\n", i, i)
	}
	return b.Bytes()
}

func mustAppend(t *testing.T, s ObjectStore, cur Cursor, off int64, prefix string, body []byte) Cursor {
	t.Helper()
	next, ref, err := Append(context.Background(), s, tKey, cur, AppendRequest{Offset: off, PrefixSHA256: prefix, Body: body})
	if err != nil || ref != nil {
		t.Fatalf("append at %d: err=%v refusal=%+v", off, err, ref)
	}
	return next
}

func TestAppendHappyPathAndIdempotentResend(t *testing.T) {
	s := NewMemoryStore()
	a, b := lines(0, 3), lines(3, 5)
	cur := mustAppend(t, s, NewCursor(), 0, EmptyStreamSHA256, a)
	cur = mustAppend(t, s, cur, int64(len(a)), BytesSHA256(a), b)
	again := mustAppend(t, s, cur, 0, EmptyStreamSHA256, a)
	if again.StoredThrough != cur.StoredThrough {
		t.Fatalf("resend moved the cursor: %d -> %d", cur.StoredThrough, again.StoredThrough)
	}
	got, _, _ := s.Reassemble(context.Background(), mustPrefix(t))
	if want := append(append([]byte{}, a...), b...); !bytes.Equal(got, want) || cur.PrefixSHA256 != BytesSHA256(want) {
		t.Fatalf("stored stream differs from what was sent")
	}
	if len(s.Keys(mustPrefix(t))) != 2 {
		t.Fatalf("resend wrote a new object")
	}
}

func mustPrefix(t *testing.T) string {
	t.Helper()
	p, err := BytesPrefix(tTeam, tConv, tLayer)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAppendRefusals(t *testing.T) {
	s := NewMemoryStore()
	a := lines(0, 3)
	cur := mustAppend(t, s, NewCursor(), 0, EmptyStreamSHA256, a)
	end := int64(len(a))
	cases := []struct {
		name   string
		req    AppendRequest
		reason string
		action Action
	}{
		{"gap past the end", AppendRequest{Offset: end + 5, PrefixSHA256: cur.PrefixSHA256, Body: lines(9, 10)}, ReasonOffsetGap, ActionResync},
		{"another stream at the end", AppendRequest{Offset: end, PrefixSHA256: BytesSHA256([]byte("other")), Body: lines(3, 4)}, ReasonPrefixMismatch, ActionNewLayer},
		{"different bytes on resend", AppendRequest{Offset: 0, PrefixSHA256: EmptyStreamSHA256, Body: lines(7, 10)}, ReasonBytesConflict, ActionNewLayer},
		{"resend with a foreign prefix", AppendRequest{Offset: 0, PrefixSHA256: "sha256:00", Body: a}, ReasonPrefixMismatch, ActionNewLayer},
		{"non-boundary resend", AppendRequest{Offset: 1, PrefixSHA256: EmptyStreamSHA256, Body: lines(0, 1)}, ReasonOffsetGap, ActionResync},
		{"torn tail", AppendRequest{Offset: end, PrefixSHA256: cur.PrefixSHA256, Body: []byte("{\"a\":")}, ReasonTornTail, ActionFix},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			after, ref, err := Append(context.Background(), s, tKey, cur, tc.req)
			if err != nil || ref == nil || ref.Body.Reason != tc.reason || ref.Body.Action != tc.action {
				t.Fatalf("got err=%v refusal=%+v, want %s/%s", err, ref, tc.reason, tc.action)
			}
			if after.StoredThrough != cur.StoredThrough || *ref.Body.StoredThrough != cur.StoredThrough {
				t.Fatalf("refusal moved the cursor")
			}
		})
	}
}

// The marquee fault: the object lands in storage but the caller sees a
// failure, so the cursor never moves. The client resends; nothing is lost or
// duplicated and the cursor catches up.
func TestAppendRecoversFromDropAfterWrite(t *testing.T) {
	s := NewMemoryStore()
	a := lines(0, 4)
	s.FailAfterPut = errors.New("connection reset after write")
	cur := NewCursor()
	if _, _, err := Append(context.Background(), s, tKey, cur, AppendRequest{Offset: 0, PrefixSHA256: EmptyStreamSHA256, Body: a}); err == nil {
		t.Fatal("injected failure not surfaced")
	}
	cur = mustAppend(t, s, cur, 0, EmptyStreamSHA256, a)
	if cur.StoredThrough != int64(len(a)) || len(s.Keys(mustPrefix(t))) != 1 {
		t.Fatalf("resend after drop-after-write did not reconcile: %+v", cur)
	}
}

// A resend that runs past the stored end stores only the new tail.
func TestAppendResendWithMoreBytesStoresOnlyTheTail(t *testing.T) {
	s := NewMemoryStore()
	a, b := lines(0, 2), lines(2, 6)
	cur := mustAppend(t, s, NewCursor(), 0, EmptyStreamSHA256, a)
	cur = mustAppend(t, s, cur, 0, EmptyStreamSHA256, append(append([]byte{}, a...), b...))
	got, _, _ := s.Reassemble(context.Background(), mustPrefix(t))
	if want := append(append([]byte{}, a...), b...); !bytes.Equal(got, want) || cur.StoredThrough != int64(len(want)) {
		t.Fatalf("tail not stored exactly once")
	}
}

// Property: however a stream is split, duplicated, retried and dropped after
// write, the stored stream equals what the client sent, exactly once, in order.
func TestAppendPropertyRandomSplitsAndFaults(t *testing.T) {
	for seed := uint64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewPCG(seed, seed*7))
		stream := lines(0, 20+r.IntN(80))
		s := NewMemoryStore()
		cur := NewCursor()
		h := NewStreamHash()
		sent := int64(0)
		for sent < int64(len(stream)) {
			end := nextLineEnd(stream, sent, 1+r.IntN(6))
			body := stream[sent:end]
			if r.IntN(4) == 0 {
				s.FailAfterPut = errors.New("drop after write")
			}
			next, ref, err := Append(context.Background(), s, tKey, cur, AppendRequest{Offset: sent, PrefixSHA256: h.Sum(), Body: body})
			if ref != nil {
				t.Fatalf("seed %d: unexpected refusal %+v", seed, ref)
			}
			if err != nil {
				continue // client retries the same range
			}
			cur = next
			if r.IntN(3) == 0 && sent > 0 { // duplicate an acknowledged batch
				_, ref, err := Append(context.Background(), s, tKey, cur, AppendRequest{Offset: 0, PrefixSHA256: EmptyStreamSHA256, Body: stream[:sent]})
				switch {
				case err != nil:
					t.Fatalf("seed %d: duplicate resend failed: %v", seed, err)
				case ref != nil && (ref.Body.Reason != ReasonTooLarge || ref.Body.Action != ActionResync || *ref.Body.StoredThrough != cur.StoredThrough):
					// Only a replay longer than MaxResendObjects may be refused,
					// and only with a resync to the stored end.
					t.Fatalf("seed %d: duplicate resend refused: %+v", seed, ref)
				}
			}
			h.Write(stream[sent:cur.StoredThrough])
			sent = cur.StoredThrough
		}
		got, idx, err := s.Reassemble(context.Background(), mustPrefix(t))
		if err != nil || !bytes.Equal(got, stream) || cur.PrefixSHA256 != BytesSHA256(stream) {
			t.Fatalf("seed %d: stored stream differs (err=%v)", seed, err)
		}
		if _, err := CheckContiguous(idx); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
	}
}

func nextLineEnd(b []byte, from int64, n int) int64 {
	i := from
	for ; n > 0 && i < int64(len(b)); n-- {
		j := bytes.IndexByte(b[i:], '\n')
		i += int64(j) + 1
	}
	return i
}

// countingStore counts reads, so a test can bound the storage work one request
// may cause.
type countingStore struct {
	*MemoryStore
	gets int
}

func (c *countingStore) Get(ctx context.Context, key string) ([]byte, map[string]string, error) {
	c.gets++
	return c.MemoryStore.Get(ctx, key)
}

// Replaying a long range of many small stored batches must not cost one read
// per batch: past MaxResendObjects the server answers resync to the stored end.
func TestResendOfManyStoredObjectsIsBoundedAndResyncs(t *testing.T) {
	ctx := context.Background()
	s := &countingStore{MemoryStore: NewMemoryStore()}
	cur := NewCursor()
	stream := lines(0, 40)
	for cur.StoredThrough < int64(len(stream)) {
		end := nextLineEnd(stream, cur.StoredThrough, 1)
		next, ref, err := Append(ctx, s, tKey, cur, AppendRequest{Offset: cur.StoredThrough, PrefixSHA256: cur.PrefixSHA256, Body: stream[cur.StoredThrough:end]})
		if err != nil || ref != nil {
			t.Fatalf("append: %v %+v", err, ref)
		}
		cur = next
	}
	s.gets = 0

	_, ref, err := Append(ctx, s, tKey, cur, AppendRequest{Offset: 0, PrefixSHA256: EmptyStreamSHA256, Body: stream})

	if err != nil || ref == nil || ref.Body.Reason != ReasonTooLarge || ref.Body.Action != ActionResync || *ref.Body.StoredThrough != cur.StoredThrough {
		t.Fatalf("got %v %+v, want too_large/resync to %d", err, ref, cur.StoredThrough)
	}
	if s.gets > MaxResendObjects {
		t.Fatalf("reads = %d, want at most %d", s.gets, MaxResendObjects)
	}
	// A short resend inside the cap still succeeds without a write.
	short := stream[:nextLineEnd(stream, 0, 2)]
	if _, ref, err := Append(ctx, s, tKey, cur, AppendRequest{Offset: 0, PrefixSHA256: EmptyStreamSHA256, Body: short}); err != nil || ref != nil {
		t.Fatalf("short resend: %v %+v", err, ref)
	}
}

// A resend that runs past the stored end stores only its new tail. That tail
// must still carry the request's signature and where the signed body began,
// so a later verifier can rebuild the exact signed bytes and check them
// (ADR-247 D8): no acknowledged byte is left without its endpoint proof.
func TestPartialTailKeepsAVerifiableSignature(t *testing.T) {
	ctx := context.Background()
	pub, priv := testKey(t)
	now := time.Unix(1_800_000_000, 0)
	s := NewMemoryStore()
	a, b := lines(0, 2), lines(2, 4)
	signA := Sign(priv, "POST", AppendURI(tConv, tLayer, 0), now, a)
	cur, ref, err := Append(ctx, s, tKey, NewCursor(), AppendRequest{Offset: 0, PrefixSHA256: EmptyStreamSHA256, Body: a, Signature: signA})
	if err != nil || ref != nil {
		t.Fatalf("append A: %v %+v", err, ref)
	}
	ab := append(append([]byte{}, a...), b...)
	signAB := Sign(priv, "POST", AppendURI(tConv, tLayer, 0), now, ab)
	if _, ref, err := Append(ctx, s, tKey, cur, AppendRequest{Offset: 0, PrefixSHA256: EmptyStreamSHA256, Body: ab, Signature: signAB}); err != nil || ref != nil {
		t.Fatalf("resend A||B: %v %+v", err, ref)
	}

	tailKey, _ := tKey(int64(len(a)))
	tail, meta, err := s.Get(ctx, tailKey)
	if err != nil || !bytes.Equal(tail, b) || meta[MetaPartial] != "true" {
		t.Fatalf("tail object = %q %v %v, want B marked partial", tail, meta, err)
	}
	from, err := strconv.ParseInt(meta[MetaSignedFrom], 10, 64)
	if err != nil || meta[MetaSignature] != signAB {
		t.Fatalf("tail meta = %v, want the A||B signature and its start offset", meta)
	}
	stored, _, err := s.Reassemble(ctx, mustPrefix(t))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := ParseSignature(meta[MetaSignature])
	if err != nil {
		t.Fatal(err)
	}
	signedBody := stored[from : int64(len(a))+int64(len(tail))]
	if err := Verify(pub, sig, "POST", AppendURI(tConv, tLayer, from), signedBody, time.Unix(sig.Unix, 0)); err != nil {
		t.Fatalf("rebuilt signed body does not verify: %v", err)
	}
}
