package livesession_test

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/sessionstream"
)

// The two edge answers seen in the 2026-11-19 firewall outage: CloudFront's
// HTML error page, and the regional WAF's custom JSON body with no reason.
const (
	cloudFrontPage = `<!DOCTYPE HTML PUBLIC "-//W3C//DTD HTML 4.01 Transitional//EN"><HTML><HEAD><TITLE>ERROR: The request could not be satisfied</TITLE></HEAD><BODY><H1>403 ERROR</H1></BODY></HTML>`
	wafBody        = `{"error":"request body exceeds WAF 8KB limit","hint":"add path to allowed_oversize_body_paths"}`
)

func countLines(logs, msg string) int {
	n := 0
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, "msg="+msg+" ") || strings.HasSuffix(l, "msg="+msg) {
			n++
		}
	}
	return n
}

// An edge 403 is no refusal from SageOx: the stream keeps every byte and
// retries, says so once per streak at Error without the body, records when it
// began for status output, and says when it clears.
func TestDrainEdgeBlockedRetriesLogsOnceAndClears(t *testing.T) {
	tests := []struct {
		name, contentType, body, shape, marker string
	}{
		{name: "cloudfront html", contentType: "text/html", body: cloudFrontPage, shape: "html", marker: "DOCTYPE"},
		{name: "waf custom json", contentType: "application/json", body: wafBody, shape: "foreign_json", marker: "WAF 8KB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			logs := e.captureLogs()
			e.spool(lines(0, 3))
			e.srv.BlockAtEdge("", 2, tt.contentType, tt.body)

			var since time.Time
			for i := range 2 {
				res, err := e.drain()
				if !errors.Is(err, livesession.ErrEdgeBlocked) || res.Done {
					t.Fatalf("drain %d: res=%+v err=%v, want a retryable ErrEdgeBlocked", i, res, err)
				}
				if strings.Contains(err.Error(), tt.marker) {
					t.Fatalf("drain %d: the edge's body reached the error (%d bytes)", i, len(err.Error()))
				}
				st := e.state()
				if st.Status != livesession.StatusStreaming || st.Acked != 0 || st.EdgeBlockedSince.IsZero() {
					t.Fatalf("drain %d: state=%+v, want streaming, nothing acked, edge-blocked", i, st)
				}
				if i == 0 {
					since = st.EdgeBlockedSince
				} else if !st.EdgeBlockedSince.Equal(since) {
					t.Fatalf("drain %d: blocked since moved within one streak", i)
				}
			}
			if notice := livesession.StreamNotice(e.dir); !strings.Contains(notice, "blocked by the network edge since") {
				t.Fatalf("status notice while blocked = %q", notice)
			}

			out := logs.String()
			if strings.Contains(out, tt.marker) {
				t.Fatal("the edge's body reached the log")
			}
			if n := countLines(out, "live_session_edge_blocked"); n != 1 {
				t.Fatalf("%d edge_blocked lines over one streak, want 1", n)
			}
			line := logs.lineWith("live_session_edge_blocked")
			for _, want := range []string{"level=ERROR", "classification=edge_blocked", "shape=" + tt.shape, "status=403", "body_bytes=", "session_id=ses_", "conversation_id=cnv_", "layer_id=clyr_"} {
				if !strings.Contains(line, want) {
					t.Fatalf("edge_blocked line lacks %q", want)
				}
			}

			if res := e.mustDrain(); !res.Done {
				t.Fatalf("drain after the edge cleared: %+v", res)
			}
			assertSameBytes(t, "stored after the edge cleared", e.stored(e.state()), e.spoolBytes())
			if st := e.state(); !st.EdgeBlockedSince.IsZero() || st.Stalls != 0 {
				t.Fatalf("state after the edge cleared: %+v", st)
			}
			if n := countLines(logs.String(), "live_session_edge_unblocked"); n != 1 {
				t.Fatalf("%d edge_unblocked lines, want 1", n)
			}
			if notice := livesession.StreamNotice(e.dir); notice != "" {
				t.Fatalf("status notice after the edge cleared = %q", notice)
			}
		})
	}
}

// api-go's own 403 still means what it says: a refusal with a reason is
// never mistaken for the edge.
func TestDrainAPIForbiddenIsNotEdgeBlocked(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 3))
	e.srv.RefuseNextAppend(http.StatusForbidden, sessionstream.ReasonForbidden, sessionstream.ActionStop)
	if _, err := e.drain(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if st := e.state(); st.Status != livesession.StatusDisabled || !st.EdgeBlockedSince.IsZero() {
		t.Fatalf("state=%+v, want disabled and not edge-blocked", st)
	}
}

// One batch the link cannot carry was resent whole 55 times in one outage.
// After stallSplitAfter consecutive stalls at one offset the next batch is
// halved, whole lines only, and the cap resets once an append is stored.
func TestDrainHalvesABatchThatKeepsStalling(t *testing.T) {
	if testing.Short() {
		t.Skip("three request timeouts per case: over 500 ms")
	}
	const hangOver = 200 << 10 // the first full batch (~256 KiB) never gets through
	tests := []struct {
		name  string
		stall func(e *env)
		cause string
	}{
		{name: "timeout", cause: "timeout", stall: func(e *env) { e.srv.HangAppendsOver(hangOver) }},
		{name: "edge blocked", cause: "edge_blocked", stall: func(e *env) { e.srv.BlockAtEdge("", 3, "text/html", cloudFrontPage) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			logs := e.captureLogs()
			fast := e.cfg.HTTP
			// a stalled send is cut by the request timeout, not the drain budget
			e.cfg.HTTP = &http.Client{Timeout: 50 * time.Millisecond}
			e.spool(lines(0, 300))
			tt.stall(e)

			for i := range 3 {
				if res, err := e.drain(); err == nil || res.Done || res.Acked != 0 {
					t.Fatalf("stalled drain %d: res=%+v err=%v", i, res, err)
				}
				if st := e.state(); i < 2 && st.BatchLimit != 0 {
					t.Fatalf("batch capped after only %d stalls: %d", i+1, st.BatchLimit)
				}
			}
			st := e.state()
			if st.BatchLimit < 16<<10 || st.BatchLimit > sessionstream.MaxBatchBytes/2 {
				t.Fatalf("after 3 stalls the cap is %d, want about half a full batch", st.BatchLimit)
			}
			split := logs.lineWith("live_session_batch_split")
			if split == "" || !strings.Contains(split, "cause="+tt.cause) || !strings.Contains(split, "session_id=ses_") {
				t.Fatalf("no batch_split line naming the cause and ids")
			}

			e.cfg.HTTP = fast
			if res := e.mustDrain(); !res.Done {
				t.Fatalf("drain at the halved size: %+v", res)
			}
			assertSameBytes(t, "stored after the split", e.stored(e.state()), e.spoolBytes())
			for _, c := range e.srv.Calls() {
				if c.Offset == 0 && c.Status == http.StatusOK && c.BodyLen > st.BatchLimit {
					t.Fatalf("the first stored batch was %d bytes, over the %d cap", c.BodyLen, st.BatchLimit)
				}
			}
			if after := e.state(); after.BatchLimit != 0 || after.Stalls != 0 {
				t.Fatalf("cap or count kept after a stored append: %+v", after)
			}
		})
	}
}

// api-go's own secret scan (F11) refuses a batch the local scan let through:
// 422 secret_refused, action stop. Like a local refusal it never stops the
// stream: the whole batch becomes one secret_withheld gap (R1), earlier gap
// lines stay, and the stream goes on.
func TestDrainServerSecretRefusalWithholdsTheBatchAndContinues(t *testing.T) {
	e := newEnv(t)
	logs := e.captureLogs()
	// an oversized line is spooled as a content-free gap line ahead of the content
	e.spool([]byte(`{"type":"msg","pad":"` + strings.Repeat("x", sessionstream.MaxBatchBytes) + `"}` + "\n"))
	gap := e.spoolBytes()
	content := lines(0, 3)
	e.spool(content)
	e.srv.RefuseNextAppend(http.StatusUnprocessableEntity, "secret_refused", sessionstream.ActionStop)

	if res := e.mustDrain(); !res.Done {
		t.Fatalf("drain after the server's refusal: %+v", res)
	}
	st := e.state()
	if st.Status != livesession.StatusStreaming || !st.SecretWithheld || st.DisabledReason != "" {
		t.Fatalf("state=%+v, want streaming with the batch withheld", st)
	}
	assertSameBytes(t, "spool", e.spoolBytes(), joined(gap, withheldGapFor(content)))
	assertSameBytes(t, "stored", e.stored(st), e.spoolBytes())
	line := logs.lineWith("live_session_secret_withheld")
	if !strings.Contains(line, "by=server") || !strings.Contains(line, "level=WARN") {
		t.Fatal("no secret_withheld Warn line naming the server's scan")
	}

	more := lines(3, 5)
	e.spool(more)
	if res := e.mustDrain(); !res.Done {
		t.Fatalf("drain after the withhold: %+v", res)
	}
	assertSameBytes(t, "stored after more lines", e.stored(e.state()), joined(gap, withheldGapFor(content), more))
}

// A refusal of a batch holding only gap lines cannot be withheld again
// without looping; it keeps today's stop.
func TestDrainServerSecretRefusalOfGapLinesOnlyStops(t *testing.T) {
	e := newEnv(t)
	e.spool([]byte(`{"type":"msg","pad":"` + strings.Repeat("x", sessionstream.MaxBatchBytes) + `"}` + "\n"))
	e.srv.RefuseNextAppend(http.StatusUnprocessableEntity, "secret_refused", sessionstream.ActionStop)
	if _, err := e.drain(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if st := e.state(); st.Status != livesession.StatusDisabled || !bytes.HasPrefix(e.spoolBytes(), []byte(`{"type":"gap","reason":"oversized_line"`)) {
		t.Fatalf("state=%+v, want disabled with the spool untouched", st)
	}
}
