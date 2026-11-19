package sessionstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
)

// ErrObjectNotFound is returned by ObjectStore.Get for a missing key.
var ErrObjectNotFound = errors.New("stream object not found")

// Object metadata keys written beside every stream object.
const (
	// MetaPrefixSHA256 is the stream hash of bytes [0, start): it lets a resend
	// at an earlier offset prove it belongs to the same stream.
	MetaPrefixSHA256 = "prefix-sha256"
	// MetaSignature is the Ox-Signature of the request that wrote this object,
	// kept so the batch stays verifiable later (ADR-247 D8).
	MetaSignature = "ox-signature"
	// MetaPartial marks an object that holds only the new tail of a signed
	// request (see Append): its bytes are not the whole signed body.
	MetaPartial = "partial"
	// MetaSignedFrom is, on a partial object, the stream offset where the
	// signed request began. Its signed body is the stored stream bytes
	// [signed-from, end of this object), sent to AppendURI(..., signed-from), so
	// a verifier can rebuild and check it exactly.
	MetaSignedFrom = "signed-from"
)

// ObjectStore is all the append algorithm needs from storage. S3 implements it
// in api-go; MemoryStore implements it for the twin and tests.
type ObjectStore interface {
	// PutIfAbsent writes key only when it does not exist (S3 If-None-Match: *).
	// created is false when the key already existed; that is not an error.
	PutIfAbsent(ctx context.Context, key string, body []byte, meta map[string]string) (created bool, err error)
	// Get returns an object's bytes and metadata, or ErrObjectNotFound.
	Get(ctx context.Context, key string) ([]byte, map[string]string, error)
}

// Cursor is a layer's durable progress: how many bytes are stored and the
// stream hash state at that point. The server keeps it in a rebuildable row.
type Cursor struct {
	StoredThrough int64
	HashState     []byte
	PrefixSHA256  string
}

// NewCursor is the cursor of an empty layer.
func NewCursor() Cursor { return Cursor{PrefixSHA256: EmptyStreamSHA256} }

// AppendRequest is one verified append, already authenticated by the caller.
type AppendRequest struct {
	Offset int64
	// PrefixSHA256 is the client's Ox-Prefix-Hash for bytes [0, Offset).
	PrefixSHA256 string
	Body         []byte
	// Signature is the request's Ox-Signature header, stored with the object.
	Signature string
}

// Refusal is a deterministic "no" the client must act on. It is not an error:
// storage failures are returned as errors and map to a retry.
type Refusal struct {
	Status int
	Body   ErrorResponse
}

func refuse(status int, reason string, action Action, msg string, stored int64) *Refusal {
	st := stored
	return &Refusal{Status: status, Body: ErrorResponse{Error: msg, Reason: reason, Action: action, StoredThrough: &st}}
}

// KeyFunc names the object for a start offset (BytesKey bound to a layer).
type KeyFunc func(offset int64) (string, error)

// Append is the append algorithm shared by api-go and the twin. It is
// idempotent: any resend of bytes already stored succeeds without writing, a
// resend that runs past the stored end stores only the new tail, and the cursor
// it returns is always the stored end. Objects are written before the cursor
// moves, so a crash between the two is repaired by the next resend (the object
// already exists, its bytes match, the cursor catches up).
func Append(ctx context.Context, store ObjectStore, key KeyFunc, cur Cursor, req AppendRequest) (Cursor, *Refusal, error) {
	if err := ValidateBatch(req.Body); err != nil {
		var be *BatchError
		if errors.As(err, &be) {
			line := be.Line
			r := refuse(400, be.Reason, ActionFix, be.Msg, cur.StoredThrough)
			if line > 0 {
				r.Body.Line = &line
			}
			if be.Reason == ReasonTooLarge {
				r.Status = 413
			}
			return cur, r, nil
		}
		return cur, nil, err
	}
	switch {
	case req.Offset > cur.StoredThrough:
		return cur, refuse(409, ReasonOffsetGap, ActionResync, "offset is past the stored end", cur.StoredThrough), nil
	case req.Offset == cur.StoredThrough:
		if req.PrefixSHA256 != cur.PrefixSHA256 {
			return cur, refuse(409, ReasonPrefixMismatch, ActionNewLayer, "prefix hash does not match the stored stream", cur.StoredThrough), nil
		}
		return appendAtEnd(ctx, store, key, cur, req.Body, req.Signature, req.Offset)
	default:
		return resendFrom(ctx, store, key, cur, req)
	}
}

// appendAtEnd stores body at cur.StoredThrough. An existing object there means
// an earlier write landed but the cursor never moved; reconcile against it.
// signedFrom is the offset the signed request began at; when it is before the
// stored end, body is only that request's new tail.
func appendAtEnd(ctx context.Context, store ObjectStore, key KeyFunc, cur Cursor, body []byte, sig string, signedFrom int64) (Cursor, *Refusal, error) {
	k, err := key(cur.StoredThrough)
	if err != nil {
		return cur, nil, err
	}
	meta := map[string]string{MetaPrefixSHA256: cur.PrefixSHA256}
	if sig != "" {
		meta[MetaSignature] = sig
	}
	if signedFrom < cur.StoredThrough {
		meta[MetaPartial] = "true"
		meta[MetaSignedFrom] = strconv.FormatInt(signedFrom, 10)
	}
	created, err := store.PutIfAbsent(ctx, k, body, meta)
	if err != nil {
		return cur, nil, fmt.Errorf("store stream object at %d: %w", cur.StoredThrough, err)
	}
	if created {
		next, err := advance(cur, body)
		return next, nil, err
	}
	existing, _, err := store.Get(ctx, k)
	if err != nil {
		return cur, nil, fmt.Errorf("read existing stream object at %d: %w", cur.StoredThrough, err)
	}
	return reconcile(ctx, store, key, cur, existing, body, sig, signedFrom)
}

// reconcile handles an object already present at the stored end.
func reconcile(ctx context.Context, store ObjectStore, key KeyFunc, cur Cursor, existing, body []byte, sig string, signedFrom int64) (Cursor, *Refusal, error) {
	switch {
	case bytes.Equal(existing, body), bytes.HasPrefix(existing, body):
		next, err := advance(cur, existing)
		return next, nil, err
	case bytes.HasPrefix(body, existing):
		next, err := advance(cur, existing)
		if err != nil {
			return cur, nil, err
		}
		return appendAtEnd(ctx, store, key, next, body[len(existing):], sig, signedFrom)
	default:
		return cur, refuse(409, ReasonBytesConflict, ActionNewLayer, "different bytes are already stored at this offset", cur.StoredThrough), nil
	}
}

// resendFrom verifies a resend that starts before the stored end against the
// stored objects, then stores whatever runs past the end. A correct client
// only resends from an offset the server returned, which is always an object
// boundary; anything else is refused.
func resendFrom(ctx context.Context, store ObjectStore, key KeyFunc, cur Cursor, req AppendRequest) (Cursor, *Refusal, error) {
	offset, body := req.Offset, req.Body
	first := true
	for reads := 0; len(body) > 0 && offset < cur.StoredThrough; reads++ {
		if reads == MaxResendObjects {
			return cur, refuse(409, ReasonTooLarge, ActionResync, "resend re-reads too many stored objects; resend from stored_through", cur.StoredThrough), nil
		}
		k, err := key(offset)
		if err != nil {
			return cur, nil, err
		}
		obj, meta, err := store.Get(ctx, k)
		if errors.Is(err, ErrObjectNotFound) {
			return cur, refuse(409, ReasonOffsetGap, ActionResync, "offset is not a stored object boundary", cur.StoredThrough), nil
		}
		if err != nil {
			return cur, nil, fmt.Errorf("read stream object at %d: %w", offset, err)
		}
		if first && meta[MetaPrefixSHA256] != req.PrefixSHA256 {
			return cur, refuse(409, ReasonPrefixMismatch, ActionNewLayer, "prefix hash does not match the stored stream", cur.StoredThrough), nil
		}
		first = false
		n := min(len(obj), len(body))
		if !bytes.Equal(obj[:n], body[:n]) {
			return cur, refuse(409, ReasonBytesConflict, ActionNewLayer, "resent bytes differ from the stored stream", cur.StoredThrough), nil
		}
		offset += int64(len(obj))
		body = body[n:]
	}
	if len(body) == 0 {
		return cur, nil, nil
	}
	return appendAtEnd(ctx, store, key, cur, body, req.Signature, req.Offset)
}

func advance(cur Cursor, b []byte) (Cursor, error) {
	h, err := RestoreStreamHash(cur.HashState, cur.StoredThrough)
	if err != nil {
		return cur, err
	}
	h.Write(b)
	state, err := h.MarshalState()
	if err != nil {
		return cur, err
	}
	return Cursor{StoredThrough: h.Len(), HashState: state, PrefixSHA256: h.Sum()}, nil
}
