package sessionstream

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ErrRecoveryDiverged means the stored objects do not pass through the cursor
// the caller still trusts: the store and the progress row disagree about the
// bytes they share, so neither may be used to move the other.
var ErrRecoveryDiverged = errors.New("stored stream does not match the known cursor")

// RecoverCursor rebuilds a layer's cursor from its stored objects alone. This
// is the source-only recovery ADR-247 promises: the progress row is
// rebuildable state, so losing it (a restore, a reset, a bug) must never strand
// a client whose acknowledged bytes are already freed.
//
// keys are the layer's BytesKeys, in any order. The objects must be contiguous
// from offset 0; a gap or overlap is an error, because a cursor built across
// one would claim bytes nobody stored. known is the cursor the caller still
// trusts (NewCursor when it knows nothing): the rebuilt stream must reproduce
// known's prefix hash at known's offset, or ErrRecoveryDiverged is returned.
func RecoverCursor(ctx context.Context, store ObjectStore, keys []string, known Cursor) (Cursor, error) {
	type object struct {
		offset int64
		key    string
	}
	objects := make([]object, 0, len(keys))
	for _, k := range keys {
		off, ok := ParseBytesKeyOffset(k)
		if !ok {
			return Cursor{}, fmt.Errorf("not a stream object key: %q", k)
		}
		objects = append(objects, object{offset: off, key: k})
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].offset < objects[j].offset })

	hash := NewStreamHash()
	checked := known.StoredThrough == 0 && known.PrefixSHA256 == EmptyStreamSHA256
	for _, o := range objects {
		if o.offset != hash.Len() {
			return Cursor{}, fmt.Errorf("stream is not contiguous at offset %d (next object starts at %d)", hash.Len(), o.offset)
		}
		b, _, err := store.Get(ctx, o.key)
		if err != nil {
			return Cursor{}, fmt.Errorf("read %s: %w", o.key, err)
		}
		if len(b) == 0 {
			return Cursor{}, fmt.Errorf("empty stream object at offset %d", o.offset)
		}
		// Check the known cursor at its exact byte, even mid-object.
		if !checked && known.StoredThrough < hash.Len()+int64(len(b)) {
			split := known.StoredThrough - hash.Len()
			hash.Write(b[:split])
			if hash.Sum() != known.PrefixSHA256 {
				return Cursor{}, ErrRecoveryDiverged
			}
			checked = true
			b = b[split:]
		}
		hash.Write(b)
	}
	if !checked {
		if hash.Len() != known.StoredThrough || hash.Sum() != known.PrefixSHA256 {
			return Cursor{}, ErrRecoveryDiverged
		}
	}
	state, err := hash.MarshalState()
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{StoredThrough: hash.Len(), HashState: state, PrefixSHA256: hash.Sum()}, nil
}
