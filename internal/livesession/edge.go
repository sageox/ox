package livesession

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/sessionstream"
)

// ErrEdgeBlocked means a request never reached SageOx: the network edge in
// front of it (CloudFront, the regional WAF) refused it with a 403 that is no
// live-capture answer. The bytes stay spooled and the caller backs off as for
// any retryable failure. The error never carries the edge's body: an HTML
// error page logged on every retry buried everything else in the log.
var ErrEdgeBlocked = errors.New("live stream blocked by the network edge")

// reasonSecretRefused is api-go's refusal of an append whose batch holds a
// likely credential (F11): 422, action stop, nothing stored. Not yet in the
// vendored wire contract, which must not be edited here.
const reasonSecretRefused = "secret_refused"

const (
	// stallSplitAfter is how many consecutive sends at one offset may time out
	// or be edge-blocked before the next batch is halved. One or two can be a
	// passing blip; the same 256 KiB batch resent 55 times in one outage is
	// a batch the link cannot carry in one request timeout.
	stallSplitAfter = 3
	// minBatchBytes floors the halving: below it, per-request overhead
	// dominates and a stall is no longer about batch size. A single line
	// longer than this is still sent whole; a line is never split.
	minBatchBytes = 16 << 10
)

// edgeShape reports whether api is the edge's answer rather than api-go's,
// and which kind: "html" (CloudFront's error page), "text" (any other non-JSON
// body) or "foreign_json" (the regional WAF's custom JSON body, which has no
// reason). api-go answers every 403 with a reason and never a 403 unavailable,
// and the vendored client marks a body it cannot decode as unavailable/retry,
// so either shape on a 403 means the request never reached it. The action is
// checked first: clients branch on it, never on the reason, and an answer
// naming any action but retry is api-go's own (stop_and_wipe must reach the
// delete path, whatever its reason says).
func edgeShape(api *sessionstream.APIError) (string, bool) {
	if api.Status != http.StatusForbidden {
		return "", false
	}
	if api.Body.Action != "" && api.Body.Action != sessionstream.ActionRetry {
		return "", false
	}
	switch api.Body.Reason {
	case "":
		return "foreign_json", true
	case sessionstream.ReasonUnavailable:
		if strings.HasPrefix(api.Body.Error, "<") {
			return "html", true
		}
		return "text", true
	}
	return "", false
}

// edgeBlocked records an edge refusal: the first of a streak sets
// EdgeBlockedSince and logs once at Error, with ids, status, body size and
// shape, never the body. The caller persists the state.
func (s *session) edgeBlocked(ctx context.Context, api *sessionstream.APIError, shape string) error {
	if s.st.EdgeBlockedSince.IsZero() {
		s.st.EdgeBlockedSince = time.Now().UTC()
		// body_bytes: the trimmed body when it was not JSON, else its error field
		s.log().ErrorContext(ctx, "live_session_edge_blocked", "classification", "edge_blocked",
			"shape", shape, "status", api.Status, "body_bytes", len(api.Body.Error), "offset", s.st.Acked,
			"impact", "nothing streams until the edge lets requests through; the spool keeps every byte and retries")
	}
	return ErrEdgeBlocked
}

// edgeCleared ends an edge-blocked streak once SageOx itself answers. It
// reports whether the state changed; the caller persists it.
func (s *session) edgeCleared(ctx context.Context) bool {
	if s.st.EdgeBlockedSince.IsZero() {
		return false
	}
	s.log().InfoContext(ctx, "live_session_edge_unblocked", "offset", s.st.Acked,
		"blocked_since", s.st.EdgeBlockedSince, "blocked_ms", time.Since(s.st.EdgeBlockedSince).Milliseconds())
	s.st.EdgeBlockedSince = time.Time{}
	return true
}

// isTimeout reports whether err is a request that ran out of its own time,
// not one the caller's budget cut off: a send cut short by the drain budget
// says nothing about whether the batch fits the link.
func isTimeout(ctx context.Context, err error) bool {
	var ne net.Error
	return ctx.Err() == nil && errors.As(err, &ne) && ne.Timeout()
}

// stalled counts one timed-out or edge-blocked send of an n-byte batch at
// acked. Every stallSplitAfter in a row at that offset, the next batch is
// halved (floor minBatchBytes), so a batch the link cannot carry stops being
// resent whole forever. The state is persisted here; a write failure only
// costs the count, never the bytes.
func (s *session) stalled(ctx context.Context, n int, cause string) {
	if s.st.Stalls == 0 || s.st.StalledAt != s.st.Acked {
		s.st.StalledAt, s.st.Stalls = s.st.Acked, 0
	}
	if s.st.Stalls++; s.st.Stalls >= stallSplitAfter {
		s.st.Stalls = 0
		if next := max(minBatchBytes, n/2); next < n {
			s.st.BatchLimit = next
			s.log().WarnContext(ctx, "live_session_batch_split", "offset", s.st.Acked, "cause", cause,
				"stalls", stallSplitAfter, "failed_bytes", n, "next_max_bytes", next)
		}
	}
	if err := s.persist(); err != nil {
		s.log().WarnContext(ctx, "live_session_state_write_failed", "during", "stall_count", "error", err)
	}
}

// unstalled resets the stall count and batch cap after a stored append. The
// caller persists the state.
func (s *session) unstalled() {
	s.st.StalledAt, s.st.Stalls, s.st.BatchLimit = 0, 0, 0
}

// batchLimit is the most bytes the next batch may carry.
func (s *session) batchLimit() int {
	if s.st.BatchLimit > 0 {
		return min(s.st.BatchLimit, sessionstream.MaxBatchBytes)
	}
	return sessionstream.MaxBatchBytes
}

// StreamNotice is the one line status output shows about the live stream in
// dir, or "" when there is nothing to say: today, only that the network edge
// is blocking it, from its open (pending) to its close.
func StreamNotice(dir string) string {
	st, err := ReadState(dir)
	if err != nil || (!st.active() && st.Status != StatusPending) || st.EdgeBlockedSince.IsZero() {
		return ""
	}
	return "Live streaming blocked by the network edge since " + st.EdgeBlockedSince.Local().Format("Jan 2 15:04") +
		"; ox keeps retrying and nothing is lost."
}
