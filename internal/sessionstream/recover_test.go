package sessionstream

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"
)

// buildStream appends stream in random batch sizes and returns the store and
// the cursor the append path recorded at every acknowledged batch.
func buildStream(t *testing.T, r *rand.Rand, stream []byte) (*MemoryStore, []Cursor) {
	t.Helper()
	s := NewMemoryStore()
	cur := NewCursor()
	cursors := []Cursor{cur}
	for cur.StoredThrough < int64(len(stream)) {
		end := nextLineEnd(stream, cur.StoredThrough, 1+r.IntN(6))
		next, ref, err := Append(context.Background(), s, tKey, cur, AppendRequest{
			Offset: cur.StoredThrough, PrefixSHA256: cur.PrefixSHA256, Body: stream[cur.StoredThrough:end],
		})
		if err != nil || ref != nil {
			t.Fatalf("append: %v %+v", err, ref)
		}
		cur = next
		cursors = append(cursors, cur)
	}
	return s, cursors
}

// Property: a cursor rebuilt from the objects alone equals the one the append
// path recorded, whatever the caller still knew, and appending on from it
// produces the same stream as never having lost the row.
func TestRecoverCursorRebuildsTheRecordedCursorFromObjectsAlone(t *testing.T) {
	ctx := context.Background()
	for seed := uint64(1); seed <= 100; seed++ {
		r := rand.New(rand.NewPCG(seed, seed*13))
		stream := lines(0, 10+r.IntN(60))
		s, cursors := buildStream(t, r, stream)
		final := cursors[len(cursors)-1]
		known := cursors[r.IntN(len(cursors))]

		got, err := RecoverCursor(ctx, s, s.Keys(mustPrefix(t)), known)
		if err != nil {
			t.Fatalf("seed %d: recover: %v", seed, err)
		}
		if got.StoredThrough != final.StoredThrough || got.PrefixSHA256 != final.PrefixSHA256 {
			t.Fatalf("seed %d: recovered %d/%s, want %d/%s", seed, got.StoredThrough, got.PrefixSHA256, final.StoredThrough, final.PrefixSHA256)
		}
		more := lines(1000, 1003)
		next, ref, err := Append(ctx, s, tKey, got, AppendRequest{Offset: got.StoredThrough, PrefixSHA256: got.PrefixSHA256, Body: more})
		if err != nil || ref != nil {
			t.Fatalf("seed %d: append after recovery: %v %+v", seed, err, ref)
		}
		if want := BytesSHA256(append(append([]byte{}, stream...), more...)); next.PrefixSHA256 != want {
			t.Fatalf("seed %d: hash state did not survive recovery", seed)
		}
	}
}

func TestRecoverCursorRefusesAGapInTheStoredStream(t *testing.T) {
	ctx := context.Background()
	s, _ := buildStream(t, rand.New(rand.NewPCG(7, 7)), lines(0, 40))
	keys := s.Keys(mustPrefix(t))
	if len(keys) < 3 {
		t.Fatalf("need several objects, got %d", len(keys))
	}
	withGap := append(append([]string{}, keys[:1]...), keys[2:]...)

	if _, err := RecoverCursor(ctx, s, withGap, NewCursor()); err == nil {
		t.Fatal("a stream with a missing object must not recover")
	}
}

// The row and the store must agree on the bytes they share, checked at the
// row's exact byte even when it falls inside an object.
func TestRecoverCursorRefusesAStoreThatDisagreesWithTheKnownCursor(t *testing.T) {
	ctx := context.Background()
	stream := lines(0, 30)
	s, cursors := buildStream(t, rand.New(rand.NewPCG(3, 9)), stream)
	known := cursors[len(cursors)/2]

	stale := known
	stale.PrefixSHA256 = BytesSHA256([]byte("not the stored bytes"))
	if _, err := RecoverCursor(ctx, s, s.Keys(mustPrefix(t)), stale); !errors.Is(err, ErrRecoveryDiverged) {
		t.Fatalf("err = %v, want ErrRecoveryDiverged", err)
	}

	midObject := Cursor{StoredThrough: known.StoredThrough + 1, PrefixSHA256: BytesSHA256(stream[:known.StoredThrough+1])}
	got, err := RecoverCursor(ctx, s, s.Keys(mustPrefix(t)), midObject)
	if err != nil || got.StoredThrough != int64(len(stream)) {
		t.Fatalf("a known cursor inside an object must be checked at its byte: %v %+v", err, got)
	}

	beyond := Cursor{StoredThrough: int64(len(stream)) + 10, PrefixSHA256: BytesSHA256(stream)}
	if _, err := RecoverCursor(ctx, s, s.Keys(mustPrefix(t)), beyond); !errors.Is(err, ErrRecoveryDiverged) {
		t.Fatalf("a store holding less than the known cursor must not recover: %v", err)
	}
}
