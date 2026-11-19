package sessionstream

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the reference client for the live-capture API. The ox CLI owns its
// production client; this one is the executable specification the twin, the
// server tests and the Tilt smoke run, so the contract is proven from both ends.
type Client struct {
	BaseURL string
	Token   string
	Key     ed25519.PrivateKey
	HTTP    *http.Client
	// Now is the clock used for signatures; nil means time.Now.
	Now func() time.Time
}

// APIError is a refusal decoded from an ErrorResponse.
type APIError struct {
	Status int
	Body   ErrorResponse
}

func (e *APIError) Error() string {
	return fmt.Sprintf("live capture: %d %s (%s): %s", e.Status, e.Body.Reason, e.Body.Action, e.Body.Error)
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// ErrMalformedResponse means a 2xx carried no decodable body where the contract
// promises one. A proxy or load balancer can send one; it proves nothing about
// what the server stored, so callers treat it like a lost reply and resend.
var ErrMalformedResponse = errors.New("live capture: malformed success response")

// Do sends one request, signed when the client has a key, and decodes a JSON
// response into out (nil to discard). Non-2xx returns *APIError; a 2xx that
// out cannot be decoded from returns ErrMalformedResponse.
func (c *Client) Do(ctx context.Context, method, requestURI string, body []byte, contentType string, extra http.Header, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+requestURI, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Key != nil {
		req.Header.Set(HeaderSignature, Sign(c.Key, method, requestURI, c.now(), body))
	}
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() // a close error cannot change what was read
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		apiErr := &APIError{Status: resp.StatusCode}
		if json.Unmarshal(raw, &apiErr.Body) != nil {
			apiErr.Body = ErrorResponse{Error: strings.TrimSpace(string(raw)), Reason: ReasonUnavailable, Action: ActionRetry}
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if len(raw) == 0 {
		return fmt.Errorf("%w: %d with an empty body", ErrMalformedResponse, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %d: %w", ErrMalformedResponse, resp.StatusCode, err)
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, uri string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	return c.Do(ctx, method, uri, body, "application/json", nil, out)
}

// RegisterKey registers the client's public key; the request is signed with it.
func (c *Client) RegisterKey(ctx context.Context, label string) (RegisterKeyResponse, error) {
	pub, _ := c.Key.Public().(ed25519.PublicKey)
	var out RegisterKeyResponse
	err := c.doJSON(ctx, http.MethodPost, PathKeys, RegisterKeyRequest{PublicKey: EncodePublicKey(pub), Label: label}, &out)
	return out, err
}

// Open opens or resumes a session.
func (c *Client) Open(ctx context.Context, cnv string, in OpenSessionRequest) (OpenSessionResponse, error) {
	var out OpenSessionResponse
	err := c.doJSON(ctx, http.MethodPut, SessionPath(cnv), in, &out)
	return out, err
}

// AppendRaw sends one append exactly as given.
func (c *Client) AppendRaw(ctx context.Context, cnv, layer string, offset int64, prefix string, body []byte) (AppendResponse, error) {
	var out AppendResponse
	h := http.Header{}
	h.Set(HeaderPrefixHash, prefix)
	err := c.Do(ctx, http.MethodPost, AppendURI(cnv, layer, offset), body, ContentTypeNDJSON, h, &out)
	return out, err
}

// Close ends a session.
func (c *Client) Close(ctx context.Context, cnv string, in CloseRequest) error {
	return c.doJSON(ctx, http.MethodPost, ClosePath(cnv), in, nil)
}

// Delete aborts a session everywhere.
func (c *Client) Delete(ctx context.Context, cnv string) error {
	return c.doJSON(ctx, http.MethodDelete, SessionPath(cnv), nil, nil)
}

// State reads a session's state.
func (c *Client) State(ctx context.Context, cnv string) (SessionState, error) {
	var out SessionState
	err := c.doJSON(ctx, http.MethodGet, SessionPath(cnv), nil, &out)
	return out, err
}

// ErrStreamDiverged means the server holds bytes this client did not send.
// The stream can never be continued; start a new layer.
var ErrStreamDiverged = errors.New("server stream diverged from local bytes")

// ErrResyncLimit means one Flush was answered resync more than maxResyncs
// times. The bytes stay held; the caller backs off and flushes again later.
var ErrResyncLimit = errors.New("too many resync answers in one flush")

// maxResyncs matches the ox drain's cap. An honest server needs one resync to
// point the client at its stored end; a server or proxy that keeps answering
// resync must not pin the client in a hot loop.
const maxResyncs = 16

// Streamer is the reference drain: it holds every unacknowledged byte, sends
// from the acknowledged offset, and frees bytes only after a 200 whose stream
// hash matches its own. A resync is never proof: it only says the server holds
// bytes past the acknowledged offset, which the next 200 must then prove. The
// acknowledged offset and hash are all it must persist across restarts; the
// bytes themselves live in the caller's spool.
type Streamer struct {
	Client       *Client
	Conversation string
	Layer        string

	acked   int64
	hash    *StreamHash
	pending []byte
}

// NewStreamer starts a streamer at offset 0.
func NewStreamer(c *Client, cnv, layer string) *Streamer {
	return &Streamer{Client: c, Conversation: cnv, Layer: layer, hash: NewStreamHash()}
}

// Write queues bytes (whole NDJSON lines) for sending. It never blocks on I/O.
func (s *Streamer) Write(p []byte) { s.pending = append(s.pending, p...) }

// Acked is the offset through which a 200 has proven the server stores our bytes.
func (s *Streamer) Acked() int64 { return s.acked }

// Pending is how many queued bytes no 200 has proven yet.
func (s *Streamer) Pending() int { return len(s.pending) }

// StreamSHA256 is the hash of every acknowledged byte.
func (s *Streamer) StreamSHA256() string { return s.hash.Sum() }

// Flush sends every complete pending line in batches until none remain or a
// refusal stops it. A trailing partial line stays queued until it is finished.
// It returns nil only once a 200 has proven every complete line it held.
func (s *Streamer) Flush(ctx context.Context) error {
	// probe is set once a resync says the server holds bytes past acked. Those
	// bytes are unproven, so the next request resends only the first held line
	// from acked: the server checks it against the object stored there (one
	// read, so never too_large) and answers 200 with its whole stored end and
	// that end's stream hash, which ack checks against our own bytes. Sending
	// from the resync offset instead would leave nothing to send, and so no
	// 200 to prove anything, whenever the server claims every held line.
	probe := false
	for resyncs := 0; ; {
		n := batchLen(s.pending)
		if n == 0 {
			return nil
		}
		if probe {
			n = bytes.IndexByte(s.pending, '\n') + 1
		}
		resp, err := s.Client.AppendRaw(ctx, s.Conversation, s.Layer, s.acked, s.hash.Sum(), s.pending[:n])
		var apiErr *APIError
		switch {
		case err == nil:
			if err := s.ack(resp); err != nil {
				return err
			}
			probe = false
		case errors.As(err, &apiErr) && apiErr.Body.Action == ActionResync && apiErr.Body.StoredThrough != nil:
			if resyncs++; resyncs > maxResyncs {
				return fmt.Errorf("%w at offset %d: %w", ErrResyncLimit, s.acked, err)
			}
			if probe, err = s.resyncNeedsProbe(*apiErr.Body.StoredThrough); err != nil {
				return err
			}
		default:
			return err
		}
	}
}

// resyncNeedsProbe reports whether a resync to stored needs a probe: true when
// the server claims held bytes past acked. It frees nothing.
func (s *Streamer) resyncNeedsProbe(stored int64) (bool, error) {
	if stored < s.acked {
		return false, fmt.Errorf("%w: server stored_through %d is behind acknowledged %d", ErrStreamDiverged, stored, s.acked)
	}
	if held := s.acked + int64(len(s.pending)); stored > held {
		return false, fmt.Errorf("%w: server stored_through %d is past every held byte (%d)", ErrStreamDiverged, stored, held)
	}
	return stored > s.acked, nil
}

// ack frees bytes up to a 200's stored_through, and only when its stream hash
// proves the server's bytes are ours. A 200 that does not move past acked
// acknowledges nothing; accepting it would resend the same batch forever.
func (s *Streamer) ack(resp AppendResponse) error {
	stored := resp.StoredThrough
	if resp.PrefixSHA256 == "" {
		return fmt.Errorf("%w: 200 at stored_through %d carries no stream hash", ErrMalformedResponse, stored)
	}
	n := stored - s.acked
	if n <= 0 || n > int64(len(s.pending)) {
		return fmt.Errorf("%w: stored_through %d outside held range (%d, %d]", ErrStreamDiverged, stored, s.acked, s.acked+int64(len(s.pending)))
	}
	next, err := s.hash.Clone()
	if err != nil {
		return err
	}
	next.Write(s.pending[:n])
	if next.Sum() != resp.PrefixSHA256 {
		return fmt.Errorf("%w: hash at %d differs", ErrStreamDiverged, stored)
	}
	s.hash, s.acked, s.pending = next, stored, s.pending[n:]
	return nil
}

// batchLen is the longest prefix of whole lines within MaxBatchBytes, or 0
// when no complete line is queued. A single line longer than the cap is sent
// alone and refused as too large; the spool must split oversized content
// (media goes out of band) before it gets here.
func batchLen(p []byte) int {
	limit := min(len(p), MaxBatchBytes)
	if i := bytes.LastIndexByte(p[:limit], '\n'); i >= 0 {
		return i + 1
	}
	if i := bytes.IndexByte(p, '\n'); i >= 0 {
		return i + 1
	}
	return 0
}
