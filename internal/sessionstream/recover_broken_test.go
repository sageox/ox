package sessionstream

import (
	"context"
	"errors"
	"testing"
)

// A caller must be able to tell a stored stream that can never be rebuilt
// (retrying is pointless and the loss needs a human) from a read that failed
// this time (retrying is the answer).
func TestRecoverCursorSeparatesABrokenStreamFromAFailedRead(t *testing.T) {
	ctx := context.Background()
	build := func(t *testing.T) (*MemoryStore, int64) {
		t.Helper()
		s := NewMemoryStore()
		cur := mustAppend(t, s, NewCursor(), 0, EmptyStreamSHA256, lines(0, 2))
		mid := cur.StoredThrough
		mustAppend(t, s, cur, mid, cur.PrefixSHA256, lines(2, 4))
		return s, mid
	}
	t.Run("a hole is broken", func(t *testing.T) {
		s, mid := build(t)
		first, _ := tKey(0)
		s.DeletePrefix(first)
		if _, err := RecoverCursor(ctx, s, s.Keys(mustPrefix(t)), NewCursor()); !errors.Is(err, ErrStoredStreamBroken) {
			t.Fatalf("hole before %d: got %v, want ErrStoredStreamBroken", mid, err)
		}
	})
	t.Run("a foreign key is broken", func(t *testing.T) {
		s, _ := build(t)
		keys := append(s.Keys(mustPrefix(t)), mustPrefix(t)+"index.json")
		if _, err := RecoverCursor(ctx, s, keys, NewCursor()); !errors.Is(err, ErrStoredStreamBroken) {
			t.Fatalf("got %v, want ErrStoredStreamBroken", err)
		}
	})
	t.Run("a failed read is not", func(t *testing.T) {
		s, _ := build(t)
		_, err := RecoverCursor(ctx, failingGet{s}, s.Keys(mustPrefix(t)), NewCursor())
		if err == nil || errors.Is(err, ErrStoredStreamBroken) || errors.Is(err, ErrRecoveryDiverged) {
			t.Fatalf("got %v, want a plain read failure", err)
		}
	})
}

type failingGet struct{ *MemoryStore }

func (failingGet) Get(context.Context, string) ([]byte, map[string]string, error) {
	return nil, nil, errors.New("s3 read timeout")
}
