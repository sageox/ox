package livesession_test

import (
	"errors"
	"testing"

	"github.com/sageox/ox/internal/livesession"
)

// Failure prevented (PR 1971 review): a superseded stream's close request was
// written for whatever stream the folder held by then, so a recording that
// reused the folder in between was closed in its place.
func TestRequestCloseOfActsOnlyOnTheRecordingItNames(t *testing.T) {
	_, _, dir, p := prepared(t)

	err := livesession.RequestCloseOf(t.Context(), dir, "ses_someone_else", testBudget)
	if !errors.Is(err, livesession.ErrNotStreaming) {
		t.Fatalf("close request for another recording: %v, want ErrNotStreaming", err)
	}
	if st, _ := livesession.ReadState(dir); st.CloseRequested {
		t.Fatal("the folder's current stream was closed for another recording")
	}

	if err := livesession.RequestCloseOf(t.Context(), dir, p.SessionID, testBudget); err != nil {
		t.Fatalf("close request for the folder's own recording: %v", err)
	}
	if st, _ := livesession.ReadState(dir); !st.CloseRequested {
		t.Fatal("the named recording's stream was not asked to close")
	}
}
