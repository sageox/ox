package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mp4Header is a minimal valid ftyp box header for mp4 content type detection.
// http.DetectContentType recognizes this as video/mp4.
var mp4Header = []byte{
	0x00, 0x00, 0x00, 0x20, // box size (32 bytes)
	0x66, 0x74, 0x79, 0x70, // "ftyp"
	0x69, 0x73, 0x6F, 0x6D, // "isom" (major brand)
	0x00, 0x00, 0x02, 0x00, // minor version
	0x69, 0x73, 0x6F, 0x6D, // "isom" (compatible brand)
	0x69, 0x73, 0x6F, 0x32, // "iso2"
	0x61, 0x76, 0x63, 0x31, // "avc1"
	0x6D, 0x70, 0x34, 0x31, // "mp41"
}

func TestDetectContentType_VideoFormats(t *testing.T) {
	// video extensions are not in the explicit extension map, so they fall
	// through to http.DetectContentType which sniffs the content bytes.

	t.Run("mp4 with ftyp header", func(t *testing.T) {
		got := detectContentType("meeting.mp4", mp4Header)
		assert.Equal(t, "video/mp4", got)
	})

	t.Run("mp4 without valid header", func(t *testing.T) {
		// .mp4 extension now has explicit mapping, so it returns video/mp4
		// regardless of content bytes
		got := detectContentType("video.mp4", []byte("not a real video"))
		assert.Equal(t, "video/mp4", got)
	})

	t.Run("mov with ftyp header", func(t *testing.T) {
		// mov files also use ftyp boxes
		movHeader := make([]byte, len(mp4Header))
		copy(movHeader, mp4Header)
		// change major brand to "qt  " (QuickTime)
		movHeader[8] = 0x71  // 'q'
		movHeader[9] = 0x74  // 't'
		movHeader[10] = 0x20 // ' '
		movHeader[11] = 0x20 // ' '
		got := detectContentType("recording.mov", movHeader)
		// http.DetectContentType recognizes ftyp as video/mp4 regardless of brand
		assert.Contains(t, got, "video/")
	})
}

func TestCommitAndPushDocImport_VideoFile(t *testing.T) {
	barePath, clonePath := createBareAndClone(t)
	isolatePushEnv(t, clonePath)

	docID := "team-standup-recording"
	docDir := filepath.Join(clonePath, "data", "docs", "2026", "03", "24", docID)
	require.NoError(t, os.MkdirAll(docDir, 0o755))

	// simulate a video file with a valid ftyp header
	srcContent := make([]byte, 512)
	copy(srcContent, mp4Header)
	srcRef := lfs.NewFileRef(srcContent)

	srcPointerPath := filepath.Join(docDir, "standup.mp4")
	require.NoError(t, os.WriteFile(srcPointerPath, []byte(lfs.FormatPointer(srcRef.OID, srcRef.Size)), 0o644))

	meta := docMeta{
		Version:        "1",
		Title:          "team standup recording",
		SourceFilename: "standup.mp4",
		ContentType:    detectContentType("standup.mp4", srcContent),
		SourceSize:     srcRef.Size,
		SourceOID:      srcRef.OID,
		CreatedAt:      "2026-03-24",
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
		Path:           "data/docs/2026/03/24/" + docID,
		Sidecars:       map[string]sidecar{},
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	metaPath := filepath.Join(docDir, "metadata.json")
	require.NoError(t, os.WriteFile(metaPath, metaData, 0o644))

	textPointerPath := filepath.Join(docDir, "extracted.md")
	err = commitAndPushDocImport(clonePath, "https://test.invalid", docID, metaPath, srcPointerPath, textPointerPath, false)
	require.NoError(t, err)

	// verify artifacts on remote
	verifyClone := cloneBare(t, barePath)

	// metadata.json
	verifyMeta := filepath.Join(verifyClone, "data", "docs", "2026", "03", "24", docID, "metadata.json")
	verifyMetaBytes, err := os.ReadFile(verifyMeta)
	require.NoError(t, err, "metadata.json should exist on remote")

	var parsedMeta docMeta
	require.NoError(t, json.Unmarshal(verifyMetaBytes, &parsedMeta))
	assert.Equal(t, "video/mp4", parsedMeta.ContentType)
	assert.Equal(t, "standup.mp4", parsedMeta.SourceFilename)
	assert.Equal(t, srcRef.OID, parsedMeta.SourceOID)
	assert.Equal(t, srcRef.Size, parsedMeta.SourceSize)
	assert.Equal(t, "team standup recording", parsedMeta.Title)

	// LFS pointer file
	verifySrc := filepath.Join(verifyClone, "data", "docs", "2026", "03", "24", docID, "standup.mp4")
	pointerContent, err := os.ReadFile(verifySrc)
	require.NoError(t, err, "LFS pointer should exist on remote")
	parsedOID, parsedSize, err := lfs.ParsePointer(string(pointerContent))
	require.NoError(t, err, "should be a valid LFS pointer")
	assert.Equal(t, srcRef.OID, parsedOID)
	assert.Equal(t, srcRef.Size, parsedSize)

	// commit message
	msg := runGit(t, verifyClone, "log", "-1", "--format=%s")
	assert.Equal(t, "import: doc "+docID, msg)
}

func TestVideoMetadataSerialization(t *testing.T) {
	srcContent := make([]byte, 1024)
	copy(srcContent, mp4Header)
	srcRef := lfs.NewFileRef(srcContent)

	meta := docMeta{
		Version:        "1",
		Title:          "architecture review",
		SourceFilename: "architecture-review.mp4",
		ContentType:    "video/mp4",
		SourceSize:     srcRef.Size,
		SourceOID:      srcRef.OID,
		CreatedAt:      time.Date(2026, 3, 24, 14, 0, 0, 0, time.UTC).Format(time.RFC3339),
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
		Path:           "data/docs/2026/03/24/architecture-review",
		Sidecars:       map[string]sidecar{},
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)

	// round-trip
	var parsed docMeta
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, "video/mp4", parsed.ContentType)
	assert.Equal(t, "architecture-review.mp4", parsed.SourceFilename)
	assert.Equal(t, int64(1024), parsed.SourceSize)
	assert.NotEmpty(t, parsed.SourceOID)
	assert.Empty(t, parsed.Sidecars, "video import should have no sidecars")
}

// --- URL import mock server tests ---

func TestImportVideoURL_MockServer(t *testing.T) {
	var importCalled atomic.Int32
	var statusCalled atomic.Int32

	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/teams/team_test123/recordings/import/url":
			importCalled.Add(1)

			var req api.ImportVideoURLRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}

			assert.NotEmpty(t, req.URL, "source_url should be set")

			resp := api.ImportVideoURLResponse{
				ImportID:    "rec_test_001",
				RecordingID: "rec_test_001",
				Status:      "pending",
				Title:       req.Title,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case r.Method == "GET" && r.URL.Path == "/api/v1/teams/team_test123/recordings/rec_test_001":
			statusCalled.Add(1)

			dur := 5.0
			resp := api.VideoStatusResponse{
				ID:       "rec_test_001",
				Title:    "Test Video",
				Status:   "ready",
				MimeType: "video/mp4",
				Duration: &dur,
				ProcessingSteps: map[string]map[string]any{
					"upload":     {"status": "completed"},
					"transcribe": {"status": "completed"},
					"summarize":  {"status": "completed"},
					"commit":     {"status": "completed"},
				},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
		}
	}))

	client := api.NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")

	// test ImportVideoURL
	resp, err := client.ImportVideoURL(api.ContextTypeTeam, "team_test123", &api.ImportVideoURLRequest{
		URL:   "https://localhost/test-video.mp4",
		Title: "Test Video",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "rec_test_001", resp.RecordingID)
	assert.Equal(t, "pending", resp.Status)
	assert.Equal(t, int32(1), importCalled.Load())

	// test GetVideoStatus
	status, err := client.GetVideoStatus(api.ContextTypeTeam, "team_test123", "rec_test_001")
	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "ready", status.Status)
	assert.Equal(t, "video/mp4", status.MimeType)
	assert.NotNil(t, status.Duration)
	assert.Equal(t, 5.0, *status.Duration)
	assert.Len(t, status.ProcessingSteps, 4)
	assert.Equal(t, int32(1), statusCalled.Load())

	// verify all processing steps are completed
	for _, step := range []string{"upload", "transcribe", "summarize", "commit"} {
		stepData, ok := status.ProcessingSteps[step]
		require.True(t, ok, "processing step %q should exist", step)
		assert.Equal(t, "completed", stepData["status"], "step %q should be completed", step)
	}
}

func TestImportVideoURL_404GracefulDegradation(t *testing.T) {
	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	client := api.NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")

	// ImportVideoURL returns nil, nil on 404
	resp, err := client.ImportVideoURL(api.ContextTypeTeam, "team_test123", &api.ImportVideoURLRequest{
		URL: "https://localhost/video.mp4",
	})
	assert.NoError(t, err)
	assert.Nil(t, resp, "should return nil on 404 (endpoint not deployed)")

	// GetVideoStatus returns nil, nil on 404
	status, err := client.GetVideoStatus(api.ContextTypeTeam, "team_test123", "rec_nonexistent")
	assert.NoError(t, err)
	assert.Nil(t, status, "should return nil on 404 (recording not found)")
}

func TestImportVideoURL_StatusProgression(t *testing.T) {
	// simulates a recording progressing through states: pending -> processing -> ready
	var callCount atomic.Int32

	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")

		var status string
		var steps map[string]map[string]any

		switch {
		case count <= 1:
			status = "pending"
			steps = map[string]map[string]any{
				"upload": {"status": "in_progress"},
			}
		case count <= 2:
			status = "processing"
			steps = map[string]map[string]any{
				"upload":     {"status": "completed"},
				"transcribe": {"status": "in_progress"},
			}
		default:
			status = "ready"
			steps = map[string]map[string]any{
				"upload":     {"status": "completed"},
				"transcribe": {"status": "completed"},
				"summarize":  {"status": "completed"},
				"commit":     {"status": "completed"},
			}
		}

		dur := 60.0
		resp := api.VideoStatusResponse{
			ID:              "rec_progress_001",
			Title:           "Progressing Video",
			Status:          status,
			Duration:        &dur,
			ProcessingSteps: steps,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
		json.NewEncoder(w).Encode(resp)
	}))

	client := api.NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")

	// first call: pending
	s1, err := client.GetVideoStatus(api.ContextTypeTeam, "team_test123", "rec_progress_001")
	require.NoError(t, err)
	assert.Equal(t, "pending", s1.Status)

	// second call: processing
	s2, err := client.GetVideoStatus(api.ContextTypeTeam, "team_test123", "rec_progress_001")
	require.NoError(t, err)
	assert.Equal(t, "processing", s2.Status)

	// third call: ready
	s3, err := client.GetVideoStatus(api.ContextTypeTeam, "team_test123", "rec_progress_001")
	require.NoError(t, err)
	assert.Equal(t, "ready", s3.Status)
	assert.Len(t, s3.ProcessingSteps, 4)
}

func TestListVideos_MockServer(t *testing.T) {
	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := api.ListVideosResponse{
			Recordings: []api.RecordingListItem{
				{
					ID:        "rec_001",
					Title:     "Architecture Review",
					Status:    "ready",
					MimeType:  "video/mp4",
					CreatedAt: time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC),
				},
				{
					ID:        "rec_002",
					Title:     "Sprint Retro",
					Status:    "processing",
					MimeType:  "video/mp4",
					CreatedAt: time.Date(2026, 3, 23, 15, 0, 0, 0, time.UTC),
				},
			},
			Pagination: api.PaginationResponse{
				Total:   2,
				Limit:   50,
				Offset:  0,
				HasMore: false,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))

	client := api.NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")
	resp, err := client.ListVideos(api.ContextTypeTeam, "team_test123", 50, 0)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Recordings, 2)
	assert.Equal(t, "rec_001", resp.Recordings[0].ID)
	assert.Equal(t, "ready", resp.Recordings[0].Status)
	assert.Equal(t, "video/mp4", resp.Recordings[0].MimeType)
	assert.Equal(t, "rec_002", resp.Recordings[1].ID)
	assert.Equal(t, "processing", resp.Recordings[1].Status)
	assert.False(t, resp.Pagination.HasMore)
}

func TestListVideos_404GracefulDegradation(t *testing.T) {
	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	client := api.NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")
	resp, err := client.ListVideos(api.ContextTypeTeam, "team_test123", 50, 0)
	assert.NoError(t, err)
	assert.Nil(t, resp, "should return nil on 404")
}

// --- notifyImport tests ---

func TestNotifyImport_VideoContentType(t *testing.T) {
	var notifyCalled atomic.Int32
	var receivedMeta docMeta

	server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/teams/team_vid_001/context/import" && r.Method == "POST" {
			notifyCalled.Add(1)

			var body struct {
				Metadata json.RawMessage `json:"metadata"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			json.Unmarshal(body.Metadata, &receivedMeta)

			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	// set up auth so notifyImport finds a token
	t.Setenv("SAGEOX_ENDPOINT", server.URL)

	// notifyImport reads from auth store which requires disk — test the API client directly
	client := api.NewRepoClientWithEndpoint(server.URL).WithAuthToken("test-token")

	meta := docMeta{
		Version:        "1",
		Title:          "sprint planning recording",
		SourceFilename: "sprint-planning.mp4",
		ContentType:    "video/mp4",
		SourceSize:     1048576,
		SourceOID:      "sha256:abcdef1234567890",
		CreatedAt:      "2026-03-24",
		ImportedAt:     time.Now().UTC().Format(time.RFC3339),
		Path:           "data/docs/2026/03/24/sprint-planning",
		Sidecars:       map[string]sidecar{},
	}

	_, err := client.NotifyImport("team_vid_001", &meta)
	require.NoError(t, err)
	assert.Equal(t, int32(1), notifyCalled.Load())
	assert.Equal(t, "video/mp4", receivedMeta.ContentType)
	assert.Equal(t, "sha256:abcdef1234567890", receivedMeta.SourceOID)
	assert.Equal(t, int64(1048576), receivedMeta.SourceSize)
}
