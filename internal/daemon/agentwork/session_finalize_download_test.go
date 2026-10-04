package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The finalize scan's decision for a folder in the ledger cache (GH #1107):
// this machine's work is queued as before; a read-only download is not.
// The E2E proof that a download never republishes a session lives in
// cmd/ox/session_download_finalize_test.go.

type cacheFixture struct {
	ledgerPath string
	name       string
	cacheDir   string
}

func (f cacheFixture) write(t *testing.T, filename, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(f.cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.cacheDir, filename), []byte(content), 0o644))
}

// transcript writes the raw.jsonl every case below starts from.
func (f cacheFixture) transcript(t *testing.T) { f.write(t, "raw.jsonl", testRawContent) }

// downloaded is what a download leaves: content files, never summary.json
// (a plain git file) and never meta.json.
func (f cacheFixture) downloaded(t *testing.T) {
	f.transcript(t)
	f.write(t, artifactSummaryMD, "# summary")
	f.write(t, artifactSessionMD, "# session")
}

// allArtifacts is what a stop or a finalize leaves.
func (f cacheFixture) allArtifacts(t *testing.T) {
	f.transcript(t)
	for _, a := range requiredArtifacts {
		f.write(t, a, `{"title":""}`)
	}
}

func (f cacheFixture) ledgerMeta(t *testing.T, content string) {
	t.Helper()
	dir := filepath.Join(f.ledgerPath, "sessions", f.name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(content), 0o644))
}

const (
	outcomeSkipped    = "skipped"
	outcomeFinalize   = "finalize"
	outcomeUploadOnly = "upload-only"
)

func TestDetect_LedgerCacheFolderDecision(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f cacheFixture)
		want  string
	}{
		// --- this machine's work: unchanged ---
		{
			name: "recorded, summary requested at stop, ledger meta carries the start title",
			setup: func(t *testing.T, f cacheFixture) {
				// `ox agent session start "<title>"` puts a title into the
				// ledger meta at stop, before any summary exists.
				f.allArtifacts(t)
				f.write(t, ".needs-summary", "{}")
				f.ledgerMeta(t, `{"version":"1.0","title":"fix the sync loop"}`)
			},
			want: outcomeFinalize,
		},
		{
			name:  "recorded, artifacts missing, no ledger folder yet",
			setup: func(t *testing.T, f cacheFixture) { f.transcript(t) },
			want:  outcomeFinalize,
		},
		{
			name: "recorded, artifacts missing, ledger holds only a draft placeholder",
			setup: func(t *testing.T, f cacheFixture) {
				f.transcript(t)
				f.ledgerMeta(t, `{"version":"1.0","draft":true}`)
			},
			want: outcomeFinalize,
		},
		{
			name: "push failed, own meta.json ok",
			setup: func(t *testing.T, f cacheFixture) {
				f.allArtifacts(t)
				f.write(t, artifactSummJSON, `{"title":"done"}`)
				f.write(t, "meta.json", `{"version":"1.0","title":"done","summary_status":"ok"}`)
				f.ledgerMeta(t, `{"version":"1.0","title":"done","summary_status":"ok"}`)
			},
			want: outcomeUploadOnly,
		},
		{
			name: "re-armed: ledger status blanked, .needs-summary written",
			setup: func(t *testing.T, f cacheFixture) {
				f.transcript(t)
				f.write(t, ".needs-summary", "{}")
				f.ledgerMeta(t, `{"version":"1.0","validation_error":""}`)
			},
			want: outcomeFinalize,
		},
		{
			name: "re-armed over an earlier download",
			setup: func(t *testing.T, f cacheFixture) {
				f.downloaded(t)
				f.write(t, lfs.DownloadedMarkerFile, "")
				f.write(t, ".needs-summary", "{}")
				f.ledgerMeta(t, `{"version":"1.0"}`)
			},
			want: outcomeFinalize,
		},
		{
			name: "own meta.json at failed_validation after one attempt",
			setup: func(t *testing.T, f cacheFixture) {
				f.allArtifacts(t)
				f.write(t, "meta.json", `{"version":"1.0","summary_status":"failed_validation","summary_attempts":1}`)
				f.ledgerMeta(t, `{"version":"1.0","summary_status":"failed_validation","summary_attempts":1}`)
			},
			want: outcomeFinalize,
		},

		// --- read-only downloads: skipped ---
		{
			name: "marked download of a session never summarized",
			setup: func(t *testing.T, f cacheFixture) {
				f.downloaded(t)
				f.write(t, lfs.DownloadedMarkerFile, "")
				f.ledgerMeta(t, `{"version":"1.0"}`)
			},
			want: outcomeSkipped,
		},
		{
			name: "unmarked old download of an ok session",
			setup: func(t *testing.T, f cacheFixture) {
				f.downloaded(t)
				f.ledgerMeta(t, `{"version":"1.0","title":"done","summary_status":"ok"}`)
			},
			want: outcomeSkipped,
		},
		{
			name: "unmarked old download of an unrecoverable session",
			setup: func(t *testing.T, f cacheFixture) {
				f.downloaded(t)
				f.ledgerMeta(t, `{"version":"1.0","summary_status":"unrecoverable","summary_attempts":3}`)
			},
			want: outcomeSkipped,
		},
		{
			name: "unmarked old download of a failed_validation session",
			setup: func(t *testing.T, f cacheFixture) {
				f.downloaded(t)
				f.ledgerMeta(t, `{"version":"1.0","summary_status":"failed_validation","summary_attempts":1}`)
			},
			want: outcomeSkipped,
		},
		{
			name: "unmarked old download of a pre-status session with a title",
			setup: func(t *testing.T, f cacheFixture) {
				f.downloaded(t)
				f.ledgerMeta(t, `{"version":"1.0","title":"done"}`)
			},
			want: outcomeSkipped,
		},
		{
			name: "unreadable ledger meta.json",
			setup: func(t *testing.T, f cacheFixture) {
				f.downloaded(t)
				f.ledgerMeta(t, `{"version":`)
			},
			want: outcomeSkipped,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledgerPath := t.TempDir()
			name := "2026-09-24T23-03-riley-OxRiLy"
			f := cacheFixture{
				ledgerPath: ledgerPath,
				name:       name,
				cacheDir:   filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name),
			}
			tc.setup(t, f)

			h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
			items := detectCacheOnly(t, h, ledgerPath)

			got := outcomeSkipped
			if len(items) == 1 {
				got = outcomeFinalize
				if items[0].Payload.(*SessionFinalizePayload).UploadOnly {
					got = outcomeUploadOnly
				}
			}
			require.LessOrEqual(t, len(items), 1)
			assert.Equal(t, tc.want, got)
		})
	}
}

// detectCacheOnly runs the scan over the ledger cache alone, so XDG cache
// dirs on the test machine cannot leak items in.
func detectCacheOnly(t *testing.T, h *SessionFinalizeHandler, ledgerPath string) []*WorkItem {
	t.Helper()
	items, _, err := h.detectInDir(filepath.Join(ledgerPath, ".sageox", "cache", "sessions"), ledgerPath)
	require.NoError(t, err)
	return items
}

// TestBuildPrompt_SkipsLLMWhenFolderBecomesDownload: the scan queued the
// folder as local work, then a coworker downloaded the session to read it
// before the worker picked the item up. The LLM must not run, and processing
// must leave the folder untouched.
func TestBuildPrompt_SkipsLLMWhenFolderBecomesDownload(t *testing.T) {
	ledgerPath := t.TempDir()
	name := "2026-09-24T23-03-riley-OxRiLy"
	f := cacheFixture{
		ledgerPath: ledgerPath,
		name:       name,
		cacheDir:   filepath.Join(ledgerPath, ".sageox", "cache", "sessions", name),
	}
	f.transcript(t)

	h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
	items := detectCacheOnly(t, h, ledgerPath)
	require.Len(t, items, 1, "precondition: an unclaimed transcript with no ledger entry is local work")

	f.write(t, lfs.DownloadedMarkerFile, "")

	req, err := h.BuildPrompt(items[0])
	require.NoError(t, err)
	assert.True(t, req.SkipLLM, "the LLM must not run once the folder is a download")
	require.NoError(t, h.ProcessResult(items[0], &RunResult{}))
	assert.NoFileExists(t, filepath.Join(f.cacheDir, artifactSummJSON), "processing must not write a summary into a download")
	assert.FileExists(t, filepath.Join(f.cacheDir, "raw.jsonl"), "the downloaded copy must survive")
}
