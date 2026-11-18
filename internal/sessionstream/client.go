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

// Do sends one request, signed when the client has a key, and decodes a JSON
// response into out (nil to discard). Non-2xx returns *APIError.
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
	defer resp.Body.Close()
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
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
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

// Streamer is the reference drain: it holds every unacknowledged byte, sends
// from the acknowledged offset, and frees bytes only after a 200 whose stream
// hash matches its own. The acknowledged offset and hash are all it must
// persist across restarts; the bytes themselves live in the caller's spool.
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

// Acked is the offset every byte before which the server has stored.
func (s *Streamer) Acked() int64 { return s.acked }

// Pending is how many queued bytes the server has not acknowledged.
func (s *Streamer) Pending() int { return len(s.pending) }

// StreamSHA256 is the hash of every acknowledged byte.
func (s *Streamer) StreamSHA256() string { return s.hash.Sum() }

// Flush sends every complete pending line in batches until none remain or a
// refusal stops it. A trailing partial line stays queued until it is finished.
func (s *Streamer) Flush(ctx context.Context) error {
	for {
		n := batchLen(s.pending)
		if n == 0 {
			return nil
		}
		resp, err := s.Client.AppendRaw(ctx, s.Conversation, s.Layer, s.acked, s.hash.Sum(), s.pending[:n])
		var apiErr *APIError
		switch {
		case err == nil:
			if err := s.ack(resp.StoredThrough, resp.PrefixSHA256); err != nil {
				return err
			}
		case errors.As(err, &apiErr) && apiErr.Body.Action == ActionResync && apiErr.Body.StoredThrough != nil:
			// Resync only forward over bytes we hold; the hash check in the next
			// append proves the server's bytes are ours.
			if *apiErr.Body.StoredThrough < s.acked {
				return fmt.Errorf("%w: server stored_through %d is behind acknowledged %d", ErrStreamDiverged, *apiErr.Body.StoredThrough, s.acked)
			}
			if err := s.ack(*apiErr.Body.StoredThrough, ""); err != nil {
				return err
			}
		default:
			return err
		}
	}
}

// ack frees bytes up to stored after checking the server's stream hash.
func (s *Streamer) ack(stored int64, serverPrefix string) error {
	n := stored - s.acked
	if n < 0 || n > int64(len(s.pending)) {
		return fmt.Errorf("%w: stored_through %d outside held range [%d, %d]", ErrStreamDiverged, stored, s.acked, s.acked+int64(len(s.pending)))
	}
	next, err := s.hash.Clone()
	if err != nil {
		return err
	}
	next.Write(s.pending[:n])
	if serverPrefix != "" && next.Sum() != serverPrefix {
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
