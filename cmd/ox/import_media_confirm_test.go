package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ox import <media>: confirm answered 202 (received, still checking) ---

const waitingLine = "the server is still checking"

const statusHint = "ox import --status " + fakeRecordingID + " --watch --team " + importRetryTeamID

// confirmWaits stands in for the wait between confirms: it records each wait
// instead of sleeping, and runs onWait (if set) with the import's context.
// The sleeper runs only on the import's goroutine and is read after the
// import returns, so it needs no lock.
type confirmWaits struct {
	waits  []time.Duration
	onWait func(ctx context.Context) error
}

func (cw *confirmWaits) total() time.Duration {
	var sum time.Duration
	for _, w := range cw.waits {
		sum += w
	}
	return sum
}

// pendingImport sets up a media import whose confirm answers follow script,
// with the wait between confirms recorded instead of slept.
func pendingImport(t *testing.T, script ...confirmAnswer) (*mediaFixture, *fakeRecordingServer, *confirmWaits) {
	t.Helper()
	f := newMediaFixture(t, "standup.m4a", 4096)
	srv := newFakeRecordingServer(t, f.importRetryFixture, "single")
	srv.confirmScript = script

	cw := &confirmWaits{}
	prior := mediaConfirmSleep
	mediaConfirmSleep = func(ctx context.Context, d time.Duration) error {
		cw.waits = append(cw.waits, d)
		if cw.onWait != nil {
			return cw.onWait(ctx)
		}
		return ctx.Err()
	}
	t.Cleanup(func() { mediaConfirmSleep = prior })
	return f, srv, cw
}

// assertNotImported checks the three marks of an import ox did not finish:
// an error, no "Imported:" line, and no local record that would turn a retry
// into "Already imported".
func (f *mediaFixture) assertNotImported(t *testing.T, stdout string, err error) {
	t.Helper()
	require.Error(t, err)
	assert.NotContains(t, stdout, "Imported:")
	_, found := findMediaImportByOID(f.tcPath(), f.sha256Hex())
	assert.False(t, found, "an upload the server has not accepted is not recorded as imported")
}

var (
	pending   = confirmAnswer{status: http.StatusAccepted}
	accepted  = confirmAnswer{status: http.StatusOK}
	unanswer  = confirmAnswer{hang: true}
	pendingIn = func(retryAfter string) confirmAnswer {
		return confirmAnswer{status: http.StatusAccepted, retryAfter: retryAfter}
	}
)

// TestImportMedia_PendingConfirmThenAccepted: a 202 is followed by another
// confirm, and only the accepting answer makes the import "Imported".
// Failure prevented: an import that treats a 202 as done.
func TestImportMedia_PendingConfirmThenAccepted(t *testing.T) {
	f, srv, cw := pendingImport(t, pendingIn("3"), accepted)

	stdout, stderr, err := f.importMedia(false, false)
	require.NoError(t, err, "stderr: %s", stderr)

	assert.Contains(t, stdout, "Imported: standup")
	assert.Equal(t, 1, strings.Count(stderr, waitingLine), "one waiting line on stderr: %q", stderr)
	assert.NotContains(t, stdout, waitingLine, "the waiting line never reaches stdout")
	confirms := srv.snapshot().confirms
	require.Len(t, confirms, 2, "ox asks again after a 202")
	assert.Equal(t, confirms[0], confirms[1], "the repeat is the same confirm")
	assert.Equal(t, []time.Duration{3 * time.Second}, cw.waits)
	_, found := findMediaImportByOID(f.tcPath(), f.sha256Hex())
	assert.True(t, found, "an accepted upload is recorded")
}

// TestImportMedia_PendingConfirmThenRefused: when the server refuses after a
// 202, the person sees the server's own code and message and nothing is
// recorded.
// Failure prevented: a file the server later rejects reported as imported,
// and the record blocking a retry with "Already imported".
func TestImportMedia_PendingConfirmThenRefused(t *testing.T) {
	f, srv, _ := pendingImport(t, pendingIn("1"), confirmAnswer{
		status: http.StatusUnprocessableEntity, code: "upload_checksum_mismatch", msg: "stored object does not match the declared checksum",
	})

	stdout, _, err := f.importMedia(false, false)
	f.assertNotImported(t, stdout, err)
	assert.Contains(t, err.Error(), "upload_checksum_mismatch")
	assert.Contains(t, err.Error(), "stored object does not match the declared checksum")
	assert.Len(t, srv.snapshot().confirms, 2)
}

// TestImportMedia_PendingConfirmGivesUpAfterBudget: a server that keeps
// answering 202 is asked until the budget runs out, never past it; then the
// person is told the upload was received and how to follow it. With --json
// the error and hint stay off stdout.
// Failure prevented: an import that waits forever, one that gives up without
// saying the upload arrived and where to look next, or a non-JSON stdout.
func TestImportMedia_PendingConfirmGivesUpAfterBudget(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[jsonOut], func(t *testing.T) {
			f, srv, cw := pendingImport(t, pendingIn("30"))

			stdout, stderr, err := f.importMedia(false, jsonOut)
			f.assertNotImported(t, stdout, err)
			assert.Contains(t, err.Error(), "received but not yet accepted")
			assert.Contains(t, stderr, statusHint)
			if jsonOut {
				assert.Empty(t, stdout)
			}
			assert.Equal(t, mediaConfirmBudget, cw.total(), "every 30s wait that fits in the budget is taken, and none past it")
			assert.Len(t, srv.snapshot().confirms, 1+int(mediaConfirmBudget/(30*time.Second)))
			assert.Equal(t, 1, strings.Count(stderr, waitingLine), "the waiting line is printed once, not per confirm")
		})
	}
}

// TestImportMedia_PendingConfirmWaitIsBounded: the wait follows Retry-After
// within 1s..30s, and is 2s when the server gives none.
// Failure prevented: a Retry-After of 0 hammering the server with confirms,
// or a huge one leaving the person waiting minutes between confirms.
func TestImportMedia_PendingConfirmWaitIsBounded(t *testing.T) {
	tests := []struct {
		retryAfter string
		want       time.Duration
	}{
		{"0", time.Second},
		{"3", 3 * time.Second},
		{"999", 30 * time.Second},
		{"", 2 * time.Second},
		{"soon", 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run("Retry-After="+tt.retryAfter, func(t *testing.T) {
			f, _, cw := pendingImport(t, pendingIn(tt.retryAfter), accepted)

			_, stderr, err := f.importMedia(false, false)
			require.NoError(t, err, "stderr: %s", stderr)
			assert.Equal(t, []time.Duration{tt.want}, cw.waits)
		})
	}
}

// TestImportMedia_CtrlCWhileWaitingForConfirm: Ctrl-C during the wait stops
// the import at once with the cancellation error, records nothing, and points
// at --status because the upload itself was received.
// Failure prevented: Ctrl-C ignored until the wait ends, or a cancelled
// import reported as imported.
func TestImportMedia_CtrlCWhileWaitingForConfirm(t *testing.T) {
	f, srv, cw := pendingImport(t, pendingIn("5"), accepted)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cw.onWait = func(ctx context.Context) error {
		cancel() // the person presses Ctrl-C while ox waits
		return ctx.Err()
	}

	stdout, stderr, err := f.importMediaCtx(ctx, false, false)
	f.assertNotImported(t, stdout, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, stderr, statusHint)
	assert.Len(t, srv.snapshot().confirms, 1, "no confirm is sent after Ctrl-C")
}

// TestImportMedia_CtrlCDuringRepeatedConfirm: Ctrl-C while a repeated confirm
// is waiting on the server ends it at once, the same way as during the wait.
// Failure prevented: Ctrl-C reported as a network error with no hint, or held
// until the call's own 2-minute limit.
func TestImportMedia_CtrlCDuringRepeatedConfirm(t *testing.T) {
	f, _, cw := pendingImport(t, pendingIn("1"), unanswer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cw.onWait = func(context.Context) error {
		// the wait ends normally; Ctrl-C lands while the next confirm hangs
		time.AfterFunc(200*time.Millisecond, cancel)
		return nil
	}

	start := time.Now()
	stdout, stderr, err := f.importMediaCtx(ctx, false, false)
	f.assertNotImported(t, stdout, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, stderr, statusHint)
	assert.Less(t, time.Since(start), 30*time.Second)
}

// TestMediaConfirmSleep_EndsOnCancel: the real wait between confirms returns
// as soon as the context is cancelled.
// Failure prevented: Ctrl-C held for up to 30 seconds by a plain time.Sleep.
func TestMediaConfirmSleep_EndsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := mediaConfirmSleep(ctx, time.Hour)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), time.Second)
}

// TestImportMedia_PendingConfirmJSONOutput: with --json, stdout is only the
// result object; the waiting line goes to stderr.
// Failure prevented: a progress line mixed into stdout, breaking every
// consumer that parses ox import --json.
func TestImportMedia_PendingConfirmJSONOutput(t *testing.T) {
	f, _, _ := pendingImport(t, pending, accepted)

	stdout, stderr, err := f.importMedia(false, true)
	require.NoError(t, err, "stderr: %s", stderr)

	dec := json.NewDecoder(strings.NewReader(stdout))
	var res importResult
	require.NoError(t, dec.Decode(&res))
	assert.Equal(t, "imported", res.Status)
	assert.Equal(t, fakeRecordingID, res.RecordingID)
	_, err = dec.Token()
	assert.ErrorIs(t, err, io.EOF, "stdout holds one JSON object and nothing else")
	assert.Contains(t, stderr, waitingLine)
}

// TestImportMedia_HangingConfirmIsCutAtBudget: a repeated confirm that does
// not answer is cut off when the budget runs out, not after the normal
// per-call limit.
// Failure prevented: a 5-minute wait that becomes 7 because the last confirm
// started at 4:59 with a 2-minute limit of its own.
func TestImportMedia_HangingConfirmIsCutAtBudget(t *testing.T) {
	f, srv, _ := pendingImport(t, pendingIn("1"), unanswer)
	prior := mediaConfirmBudget
	// the 1s wait fits; the recorded sleep takes no time, so the hanging
	// confirm is cut about 1.5s after the first 202
	mediaConfirmBudget = 1500 * time.Millisecond
	t.Cleanup(func() { mediaConfirmBudget = prior })

	start := time.Now()
	stdout, stderr, err := f.importMedia(false, false)
	elapsed := time.Since(start)

	f.assertNotImported(t, stdout, err)
	assert.Contains(t, err.Error(), "received but not yet accepted")
	assert.False(t, errors.Is(err, context.Canceled))
	assert.Contains(t, stderr, statusHint)
	assert.Less(t, elapsed, 30*time.Second, "the hanging confirm is cut at the budget, not the 2-minute call limit")
	assert.Len(t, srv.snapshot().confirms, 2)
}
