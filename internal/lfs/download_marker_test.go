package lfs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- GH #1107: a read-only download marks its cache folder; work never does ---

// TestMarkCacheDownload_Decision: a download claims an empty folder, and
// never one already holding this machine's own work. Claiming a recording's
// folder would make the daemon drop the owner's finalization.
func TestMarkCacheDownload_Decision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing string // file already in the folder; "" for none
		missing  bool   // folder does not exist yet
		want     bool
	}{
		{name: "folder does not exist yet", missing: true, want: true},
		{name: "empty folder", want: true},
		{name: "earlier partial download", existing: "summary.md", want: true},
		{name: "recording transcript", existing: "raw.jsonl", want: false},
		{name: "unpushed finalize", existing: "meta.json", want: false},
		{name: "summary requested", existing: ".needs-summary", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cacheDir := filepath.Join(t.TempDir(), "sessions", "2026-09-24T23-03-riley-OxRiLy")
			if !tc.missing {
				require.NoError(t, os.MkdirAll(cacheDir, 0o755))
			}
			if tc.existing != "" {
				require.NoError(t, os.WriteFile(filepath.Join(cacheDir, tc.existing), []byte("x"), 0o644))
			}

			require.NoError(t, MarkCacheDownload(cacheDir))

			assert.Equal(t, tc.want, HasDownloadedMarker(cacheDir))
		})
	}
}

// TestMarkCacheDownload_ReportsWriteFailure: when the marker cannot be
// written the caller must hear about it, or it downloads a transcript the
// daemon will then re-summarize and republish.
func TestMarkCacheDownload_ReportsWriteFailure(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "sessions")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o644))

	err := MarkCacheDownload(filepath.Join(blocker, "2026-09-24T23-03-riley-OxRiLy"))

	require.Error(t, err)
}

// TestHydrateRawToCacheErr_DoesNotMarkDownload: the re-arm and the doctor's
// session-content fix download a transcript precisely so it WILL be
// summarized. Marking their folder read-only would silently cancel the retry.
func TestHydrateRawToCacheErr_DoesNotMarkDownload(t *testing.T) {
	content := []byte("{\"type\":\"user\",\"content\":\"hello\"}\n")
	ref := NewFileRef(content)

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/objects/batch"):
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(w).Encode(BatchResponse{Transfer: "basic", Objects: []BatchResponseObject{{
				OID: ref.BareOID(), Size: ref.Size,
				Actions: &Actions{Download: &Action{Href: serverURL + "/download"}},
			}}})
		case r.URL.Path == "/download":
			_, _ = w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL

	ledgerPath := t.TempDir()
	sessionName := "2026-09-24T23-03-riley-OxRiLy"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	meta := NewSessionMeta(sessionName, "riley", "OxRiLy", "claude-code", time.Now().UTC()).Build()
	meta.Files = map[string]FileRef{"raw.jsonl": ref}
	require.NoError(t, WriteSessionMetaOnly(sessionDir, meta))
	require.NoError(t, WritePointerFile(filepath.Join(sessionDir, "raw.jsonl"), AssertUploaded(ref)))

	cachePath, err := HydrateRawToCacheErr(NewClient(server.URL+"/ledger.git", "u", "t"), sessionDir, ledgerPath)

	require.NoError(t, err)
	require.FileExists(t, cachePath, "precondition: the transcript must actually be downloaded")
	assert.False(t, HasDownloadedMarker(filepath.Dir(cachePath)),
		"a download made in order to summarize must not be marked read-only")
}
