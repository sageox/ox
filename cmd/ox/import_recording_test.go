package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/testguard"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestMediaFile creates a small mp4 file (valid ftyp header + padding)
// and returns its path and content length.
func writeTestMediaFile(t *testing.T) (string, int64) {
	t.Helper()
	content := make([]byte, 1024)
	copy(content, mp4Header)
	path := filepath.Join(t.TempDir(), "standup.mp4")
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path, int64(len(content))
}

// TestImportRecordingFile_KB_MockServer exercises the full 3-step bulk
// recording-file import against a Knowledge Bubble context: POST import →
// presigned PUT → POST complete. Asserts all three legs fire in order and
// that every API leg uses the kb/ context path.
func TestImportRecordingFile_KB_MockServer(t *testing.T) {
	mediaPath, mediaSize := writeTestMediaFile(t)

	var mu sync.Mutex
	var legs []string

	recordLeg := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		legs = append(legs, name)
	}

	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/kb/kb_test123/recordings/import":
			recordLeg("import")

			var req api.ImportRecordingFileRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "standup.mp4", req.Filename)
			assert.Equal(t, "video/mp4", req.ContentType)
			assert.Equal(t, mediaSize, req.Size, "size must come from the file on disk")
			assert.Equal(t, "Standup", req.Title)
			require.NotNil(t, req.RecordedAt)

			// point the presigned PUT back at this same mock server
			resp := map[string]any{
				"recording_id": "rec_imp_001",
				"status":       "pending",
				"upload": map[string]any{
					"upload_url": "http://" + r.Host + "/mock-upload/rec_imp_001",
					"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
				},
				"created_at": time.Now().Format(time.RFC3339),
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)

		case r.Method == "PUT" && r.URL.Path == "/mock-upload/rec_imp_001":
			recordLeg("upload")

			assert.Equal(t, "video/mp4", r.Header.Get("Content-Type"))
			assert.Equal(t, mediaSize, r.ContentLength, "PUT must declare the streamed length")
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			assert.Equal(t, mediaSize, int64(len(body)), "full file content must arrive")
			w.WriteHeader(http.StatusOK)

		case r.Method == "POST" && r.URL.Path == "/api/v1/kb/kb_test123/recordings/rec_imp_001/complete":
			recordLeg("complete")

			var req struct {
				TotalSize int64 `json:"total_size"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, mediaSize, req.TotalSize)

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"recording_id": "rec_imp_001",
				"status":       "processing",
			})

		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
		}
	}))

	client := api.NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")

	recordedAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	result, err := client.ImportRecordingFile(context.Background(), api.ContextTypeKB, "kb_test123", mediaPath, api.ImportRecordingFileRequest{
		Filename:    "standup.mp4",
		ContentType: "video/mp4",
		Title:       "Standup",
		RecordedAt:  &recordedAt,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "rec_imp_001", result.RecordingID)
	assert.Equal(t, "processing", result.Status, "status should come from the complete response")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"import", "upload", "complete"}, legs, "the three legs must fire exactly once, in order")
}

// TestImport_TeamMediaFile_DoesNotUseRecordingImport pins the team routing:
// a media file WITHOUT --kb must fall through to the document-LFS path (the
// backend's team-context-doc-import workflow already transcribes media) and
// must never hit the recordings/import endpoint — that endpoint is KB-only.
// The test isolates cwd and the XDG data dir so the doc path stops at team
// resolution, proving routing reached it instead of the recording import.
func TestImport_TeamMediaFile_DoesNotUseRecordingImport(t *testing.T) {
	mediaPath, _ := writeTestMediaFile(t)
	withImportFlags(t, "", "")

	var recordingImportHits atomic.Int32
	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/recordings/import") {
			recordingImportHits.Add(1)
		}
		http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
	}))

	t.Chdir(t.TempDir())                   // no .sageox/ → no project root
	t.Setenv("OX_PROJECT_ROOT", "")        // neutralize any ambient override
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // no synced teams to auto-discover
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", "env-test-token")

	cmd := &cobra.Command{}
	// a bare command has a nil context until Execute(); these tests call the
	// run funcs directly, so set one explicitly
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("json", false, "")

	err := runImport(cmd, []string{mediaPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "team", "media without --kb must reach the team document path")
	assert.Equal(t, int32(0), recordingImportHits.Load(), "team media import must never call recordings/import")
}

// TestImport_KBMediaFile_UsesRecordingImport pins the KB routing end-to-end
// through runImport: --kb + media file drives the full 3-leg cloud import
// against kb/ paths (incl. the KB-only guard in runImportRecordingFile).
func TestImport_KBMediaFile_UsesRecordingImport(t *testing.T) {
	mediaPath, _ := writeTestMediaFile(t)
	withImportFlags(t, "", "kb_route123")

	var mu sync.Mutex
	var legs []string
	recordLeg := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		legs = append(legs, name)
	}

	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/kb/kb_route123/recordings/import":
			recordLeg("import")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"recording_id": "rec_kb_route_001",
				"status":       "pending",
				"upload": map[string]any{
					"upload_url": "http://" + r.Host + "/mock-upload/rec_kb_route_001",
					"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
				},
			})
		case r.Method == "PUT" && r.URL.Path == "/mock-upload/rec_kb_route_001":
			recordLeg("upload")
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		case r.Method == "POST" && r.URL.Path == "/api/v1/kb/kb_route123/recordings/rec_kb_route_001/complete":
			recordLeg("complete")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"recording_id": "rec_kb_route_001", "status": "processing"})
		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
		}
	}))

	t.Chdir(t.TempDir())
	t.Setenv("OX_PROJECT_ROOT", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", "env-test-token")

	cmd := &cobra.Command{}
	// a bare command has a nil context until Execute(); these tests call the
	// run funcs directly, so set one explicitly
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("json", false, "")
	cmd.SetOut(io.Discard) // success output is asserted via the mock legs, not stdout

	err := runImport(cmd, []string{mediaPath})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"import", "upload", "complete"}, legs, "KB media must drive the 3-leg recording import on kb/ paths")
}

// TestImport_KBDocumentFile_Errors pins the third routing branch: --kb with a
// non-media file is rejected before any network or git work.
func TestImport_KBDocumentFile_Errors(t *testing.T) {
	withImportFlags(t, "", "kb_route123")

	docPath := filepath.Join(t.TempDir(), "notes.md")
	require.NoError(t, os.WriteFile(docPath, []byte("# notes"), 0o644))

	cmd := &cobra.Command{}
	// a bare command has a nil context until Execute(); these tests call the
	// run funcs directly, so set one explicitly
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("json", false, "")

	err := runImport(cmd, []string{docPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "document import into a Knowledge Bubble is not yet available")
}

// TestRunImportRecordingFile_RequiresKB pins the defensive KB-only guard: a
// direct call without --kb must fail before resolving any context, so team
// media can never silently switch storage models (git-LFS → S3 recording).
func TestRunImportRecordingFile_RequiresKB(t *testing.T) {
	mediaPath, _ := writeTestMediaFile(t)
	withImportFlags(t, "", "")

	cmd := &cobra.Command{}
	// a bare command has a nil context until Execute(); these tests call the
	// run funcs directly, so set one explicitly
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("json", false, "")

	err := runImportRecordingFile(cmd, mediaPath, time.Now(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires --kb")
}

// TestImportRecordingFile_404GracefulDegradation mirrors the URL-import
// pattern: a 404 on the import-init POST means the endpoint is not deployed
// yet and must surface as nil, nil — never as an error.
func TestImportRecordingFile_404GracefulDegradation(t *testing.T) {
	mediaPath, _ := writeTestMediaFile(t)

	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	client := api.NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")
	result, err := client.ImportRecordingFile(context.Background(), api.ContextTypeKB, "kb_test123", mediaPath, api.ImportRecordingFileRequest{
		Filename: "standup.mp4",
	})
	assert.NoError(t, err)
	assert.Nil(t, result, "should return nil on 404 (endpoint not deployed)")
}

// TestImportRecordingFile_InvalidContextType asserts the context validation
// fires before any network traffic.
func TestImportRecordingFile_InvalidContextType(t *testing.T) {
	mediaPath, _ := writeTestMediaFile(t)

	client := api.NewRepoClientWithEndpoint("http://localhost:1").WithAuthToken("test-token")
	result, err := client.ImportRecordingFile(context.Background(), "repo", "repo_x", mediaPath, api.ImportRecordingFileRequest{Filename: "standup.mp4"})
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "invalid context type")
}

// --- --kb flag resolution ---

// withImportFlags saves and restores the entire package-global importFlags so
// tests can mutate them safely. It resets to a clean baseline (only team/kb
// set) rather than snapshotting just those two — runImport also reads status,
// list, watch, title, date, text, and force, so a prior test that mutated any
// of those could otherwise leak into routing/assertions here.
func withImportFlags(t *testing.T, team, kb string) {
	t.Helper()
	saved := importFlags
	importFlags = importFlagsT{team: team, kb: kb}
	t.Cleanup(func() {
		importFlags = saved
	})
}

// TestResolveImportContext_KBFlag verifies that --kb with a kb_-prefixed ID
// resolves fully offline (no kb-API round trip) to a kb context.
func TestResolveImportContext_KBFlag(t *testing.T) {
	withImportFlags(t, "", "kb_abc123")
	// env token keeps the resolver off the on-disk auth store
	t.Setenv("SAGEOX_ENDPOINT", "https://kb-import-test.invalid")
	t.Setenv("SAGEOX_TOKEN", "env-test-token")

	contextType, contextID, client, ep, err := resolveImportContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, api.ContextTypeKB, contextType)
	assert.Equal(t, "kb_abc123", contextID)
	require.NotNil(t, client)
	assert.Contains(t, ep, "kb-import-test.invalid")
}

// TestResolveImportContext_BothFlagsError verifies the mutual-exclusion guard
// (defense in depth behind cobra's MarkFlagsMutuallyExclusive).
func TestResolveImportContext_BothFlagsError(t *testing.T) {
	withImportFlags(t, "team_x", "kb_y")

	_, _, _, _, err := resolveImportContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestIsMediaImportFile pins the media-vs-document routing boundary.
func TestIsMediaImportFile(t *testing.T) {
	media := []string{"a.mp4", "b.MOV", "c.webm", "d.m4a", "e.mp3", "f.wav", "dir/g.mkv"}
	for _, p := range media {
		assert.True(t, isMediaImportFile(p), "%s should route to recording import", p)
	}
	docs := []string{"report.pdf", "notes.md", "data.json", "img.png", "noext", "archive.tar.gz"}
	for _, p := range docs {
		assert.False(t, isMediaImportFile(p), "%s should route to document import", p)
	}
}

// TestImportCmd_KBFlagRegistered pins the CLI surface: --kb exists and the
// help text documents the Knowledge Bubble path.
func TestImportCmd_KBFlagRegistered(t *testing.T) {
	flag := importCmd.Flags().Lookup("kb")
	require.NotNil(t, flag, "--kb flag must be registered on ox import")
	assert.True(t, strings.Contains(importCmd.Long, "--kb"), "help text should mention --kb")
}
