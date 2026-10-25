package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRestoreUnpushedSessionPointers_OwnUploadFailuresStayReported covers an upload that cannot happen
// (no credentials, server down) or a working copy edited since the commit. The artifact must stay raw
// and be reported with the reason, never pointerized without proof the blob is stored.
func TestRestoreUnpushedSessionPointers_OwnUploadFailuresStayReported(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusInternalServerError) }))
	t.Cleanup(down.Close)
	tests := []struct {
		name       string
		uploader   *ownArtifactUploader
		mutate     func(t *testing.T, ledger string)
		wantReason string
	}{
		{"no credentials", &ownArtifactUploader{username: "ryan", client: func() (*lfs.Client, error) { return nil, os.ErrPermission }},
			func(*testing.T, string) {}, "cannot upload now"},
		{"server rejects the upload", &ownArtifactUploader{username: "ryan", client: func() (*lfs.Client, error) { return lfs.NewClient(down.URL+"/ledger.git", "u", "t"), nil }},
			func(*testing.T, string) {}, "upload failed"},
		{"local edit since the commit", newLFSStub(t).uploader("ryan"),
			func(t *testing.T, ledger string) { writeLedgerFile(t, ledger, myNeverUploadedPath, "edited\n") }, "uncommitted local edit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := newWedgedLedger(t, false)
			addNeverUploadedSessions(t, ledger)
			tt.mutate(t, ledger)

			report, err := restoreUnpushedSessionPointers(context.Background(), ledger, true, tt.uploader)

			require.NoError(t, err)
			var reason string
			for _, failure := range report.Unrepairable {
				if failure.Path == myNeverUploadedPath {
					reason = failure.Reason
				}
			}
			assert.Contains(t, reason, tt.wantReason)
			assert.Equal(t, myNeverUploaded, ledgerFile(t, ledger, "HEAD:"+myNeverUploadedPath))
		})
	}
}
