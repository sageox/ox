package lfs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUploadSessionFiles_Basic(t *testing.T) {
	// create a session dir with raw.jsonl and summary.md
	sessionDir := t.TempDir()
	rawContent := []byte(`{"_meta":{"schema_version":"1"}}\n{"type":"user","content":"hello","seq":1}\n`)
	summaryContent := []byte("# Summary\nTest session summary")

	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), summaryContent, 0644); err != nil {
		t.Fatal(err)
	}

	// compute expected OIDs
	rawOID := ComputeOID(rawContent)
	summaryOID := ComputeOID(summaryContent)

	// mock LFS batch server that accepts all uploads and verifies
	var mu sync.Mutex
	uploaded := make(map[string]bool)
	verified := make(map[string]bool)
	var serverURL string
	batchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info/lfs/objects/batch" {
			var req struct {
				Operation string        `json:"operation"`
				Objects   []BatchObject `json:"objects"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}

			resp := BatchResponse{Transfer: "basic"}
			for _, obj := range req.Objects {
				resp.Objects = append(resp.Objects, BatchResponseObject{
					OID:  obj.OID,
					Size: obj.Size,
					Actions: &Actions{
						Upload: &Action{
							Href: serverURL + "/upload/" + obj.OID,
						},
						Verify: &Action{
							Href: serverURL + "/verify",
						},
					},
				})
			}

			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// handle upload PUT requests
		if r.Method == "PUT" {
			oid := filepath.Base(r.URL.Path)
			mu.Lock()
			uploaded[oid] = true
			mu.Unlock()
			w.WriteHeader(200)
			return
		}

		// handle verify POST requests
		if r.Method == "POST" && r.URL.Path == "/verify" {
			var body struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			verified[body.OID] = true
			mu.Unlock()
			w.WriteHeader(200)
			return
		}

		http.Error(w, "not found", 404)
	}))
	defer batchServer.Close()
	serverURL = batchServer.URL

	client := NewClient(batchServer.URL, "testuser", "testtoken")

	fileRefs, err := UploadSessionFiles(client, sessionDir, nil)
	if err != nil {
		t.Fatalf("UploadSessionFiles failed: %v", err)
	}

	// verify file refs returned for both files
	if len(fileRefs) != 2 {
		t.Fatalf("expected 2 file refs, got %d", len(fileRefs))
	}

	rawRef, ok := fileRefs["raw.jsonl"]
	if !ok {
		t.Fatal("missing raw.jsonl in file refs")
	}
	if rawRef.BareOID() != rawOID {
		t.Errorf("raw.jsonl OID mismatch: got %s, want %s", rawRef.BareOID(), rawOID)
	}
	if rawRef.Size != int64(len(rawContent)) {
		t.Errorf("raw.jsonl size mismatch: got %d, want %d", rawRef.Size, len(rawContent))
	}

	summaryRef, ok := fileRefs["summary.md"]
	if !ok {
		t.Fatal("missing summary.md in file refs")
	}
	if summaryRef.BareOID() != summaryOID {
		t.Errorf("summary.md OID mismatch: got %s, want %s", summaryRef.BareOID(), summaryOID)
	}

	// verify blobs were uploaded
	mu.Lock()
	uploadedRaw := uploaded[rawOID]
	uploadedSummary := uploaded[summaryOID]
	verifiedRaw := verified[rawOID]
	verifiedSummary := verified[summaryOID]
	mu.Unlock()

	if !uploadedRaw {
		t.Error("raw.jsonl blob was not uploaded")
	}
	if !uploadedSummary {
		t.Error("summary.md blob was not uploaded")
	}

	// verify blobs were verified via the LFS verify action
	if !verifiedRaw {
		t.Error("raw.jsonl blob was not verified after upload")
	}
	if !verifiedSummary {
		t.Error("summary.md blob was not verified after upload")
	}
}

func TestUploadSessionFiles_SkipsExistingPointers(t *testing.T) {
	sessionDir := t.TempDir()

	// write raw.jsonl as a regular file
	rawContent := []byte(`{"type":"user","content":"test","seq":1}`)
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644); err != nil {
		t.Fatal(err)
	}

	// write summary.md as an LFS pointer file
	pointerOID := "sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abc1"
	pointerContent := FormatPointer(pointerOID, 12345)
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte(pointerContent), 0644); err != nil {
		t.Fatal(err)
	}

	// mock LFS server - only raw.jsonl should be uploaded
	var batchCalled bool
	batchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info/lfs/objects/batch" {
			batchCalled = true
			var req struct {
				Objects []BatchObject `json:"objects"`
			}
			json.NewDecoder(r.Body).Decode(&req)

			// only raw.jsonl should appear in batch request
			if len(req.Objects) != 1 {
				t.Errorf("expected 1 batch object (only raw.jsonl), got %d", len(req.Objects))
			}

			resp := BatchResponse{Transfer: "basic"}
			for _, obj := range req.Objects {
				// already exists on server
				resp.Objects = append(resp.Objects, BatchResponseObject{
					OID:  obj.OID,
					Size: obj.Size,
				})
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer batchServer.Close()

	client := NewClient(batchServer.URL, "testuser", "testtoken")

	fileRefs, err := UploadSessionFiles(client, sessionDir, nil)
	if err != nil {
		t.Fatalf("UploadSessionFiles failed: %v", err)
	}

	if !batchCalled {
		t.Error("batch API was not called")
	}

	// pointer file should have its OID extracted directly
	summaryRef, ok := fileRefs["summary.md"]
	if !ok {
		t.Fatal("missing summary.md in file refs")
	}
	if summaryRef.OID != pointerOID {
		t.Errorf("summary.md OID mismatch: got %s, want %s", summaryRef.OID, pointerOID)
	}
	if summaryRef.Size != 12345 {
		t.Errorf("summary.md size mismatch: got %d, want 12345", summaryRef.Size)
	}
}

func TestUploadSessionFiles_MissingFiles(t *testing.T) {
	sessionDir := t.TempDir()
	// empty session dir — all content files missing

	// mock LFS server — should never be called since no files to upload
	var batchCalled bool
	batchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batchCalled = true
		http.Error(w, "should not be called", 500)
	}))
	defer batchServer.Close()

	client := NewClient(batchServer.URL, "testuser", "testtoken")

	fileRefs, err := UploadSessionFiles(client, sessionDir, nil)
	if err != nil {
		t.Fatalf("UploadSessionFiles failed: %v", err)
	}

	if batchCalled {
		t.Error("batch API should not be called when no files exist")
	}

	if len(fileRefs) != 0 {
		t.Errorf("expected 0 file refs for empty dir, got %d", len(fileRefs))
	}
}

func TestUploadSessionFiles_UploadFailure(t *testing.T) {
	sessionDir := t.TempDir()
	rawContent := []byte(`{"type":"user","content":"hello","seq":1}`)
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644); err != nil {
		t.Fatal(err)
	}

	// mock LFS server that returns upload actions but fails on PUT
	var failServerURL string
	batchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info/lfs/objects/batch" {
			var req struct {
				Objects []BatchObject `json:"objects"`
			}
			json.NewDecoder(r.Body).Decode(&req)

			resp := BatchResponse{Transfer: "basic"}
			for _, obj := range req.Objects {
				resp.Objects = append(resp.Objects, BatchResponseObject{
					OID:  obj.OID,
					Size: obj.Size,
					Actions: &Actions{
						Upload: &Action{
							Href: failServerURL + "/upload/" + obj.OID,
						},
					},
				})
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// fail all uploads
		if r.Method == "PUT" {
			http.Error(w, "internal server error", 500)
			return
		}

		http.Error(w, "not found", 404)
	}))
	defer batchServer.Close()
	failServerURL = batchServer.URL

	client := NewClient(batchServer.URL, "testuser", "testtoken")

	_, err := UploadSessionFiles(client, sessionDir, nil)
	if err == nil {
		t.Fatal("expected error from failed upload, got nil")
	}
}

func TestUploadSessionFiles_PartialFailure(t *testing.T) {
	// two files: raw.jsonl upload succeeds, summary.md upload fails with 500
	sessionDir := t.TempDir()
	rawContent := []byte(`{"_meta":{"schema_version":"1"}}` + "\n" + `{"type":"user","content":"hello","seq":1}` + "\n")
	summaryContent := []byte("# Summary\nTest session summary")

	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.md"), summaryContent, 0644))

	summaryOID := ComputeOID(summaryContent)

	var mu sync.Mutex
	uploaded := make(map[string]bool)

	var serverURL string
	batchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info/lfs/objects/batch" {
			var req struct {
				Objects []BatchObject `json:"objects"`
			}
			json.NewDecoder(r.Body).Decode(&req)

			resp := BatchResponse{Transfer: "basic"}
			for _, obj := range req.Objects {
				resp.Objects = append(resp.Objects, BatchResponseObject{
					OID:  obj.OID,
					Size: obj.Size,
					Actions: &Actions{
						Upload: &Action{
							Href: serverURL + "/upload/" + obj.OID,
						},
					},
				})
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		if r.Method == "PUT" {
			oid := filepath.Base(r.URL.Path)
			// reject summary.md upload, accept raw.jsonl
			if oid == summaryOID {
				http.Error(w, "internal server error", 500)
				return
			}
			mu.Lock()
			uploaded[oid] = true
			mu.Unlock()
			w.WriteHeader(200)
			return
		}

		http.Error(w, "not found", 404)
	}))
	defer batchServer.Close()
	serverURL = batchServer.URL

	client := NewClient(batchServer.URL, "testuser", "testtoken")

	fileRefs, err := UploadSessionFiles(client, sessionDir, nil)

	// must return an error when any file fails
	require.Error(t, err, "expected error from partial upload failure")

	// no refs returned on failure — caller cannot use partial state
	if fileRefs != nil {
		t.Errorf("expected nil fileRefs on failure, got %v", fileRefs)
	}
}

func TestUploadSessionFiles_VerifyFailure(t *testing.T) {
	sessionDir := t.TempDir()
	rawContent := []byte(`{"type":"user","content":"hello","seq":1}`)
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), rawContent, 0644); err != nil {
		t.Fatal(err)
	}

	// mock LFS server: upload succeeds but verify fails (blob not durably stored)
	var verifyServerURL string
	batchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info/lfs/objects/batch" {
			var req struct {
				Objects []BatchObject `json:"objects"`
			}
			json.NewDecoder(r.Body).Decode(&req)

			resp := BatchResponse{Transfer: "basic"}
			for _, obj := range req.Objects {
				resp.Objects = append(resp.Objects, BatchResponseObject{
					OID:  obj.OID,
					Size: obj.Size,
					Actions: &Actions{
						Upload: &Action{
							Href: verifyServerURL + "/upload/" + obj.OID,
						},
						Verify: &Action{
							Href: verifyServerURL + "/verify",
						},
					},
				})
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// upload succeeds
		if r.Method == "PUT" {
			w.WriteHeader(200)
			return
		}

		// verify fails — blob not found on server
		if r.Method == "POST" && r.URL.Path == "/verify" {
			http.Error(w, `{"message":"object not found"}`, 404)
			return
		}

		http.Error(w, "not found", 404)
	}))
	defer batchServer.Close()
	verifyServerURL = batchServer.URL

	client := NewClient(batchServer.URL, "testuser", "testtoken")

	_, err := UploadSessionFiles(client, sessionDir, nil)
	if err == nil {
		t.Fatal("expected error when verify fails after successful upload, got nil")
	}
}

func TestEnsureSessionsGitignore(t *testing.T) {
	dir := t.TempDir()

	// first call creates the file
	if err := EnsureSessionsGitignore(dir); err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}

	if len(content) == 0 {
		t.Error("gitignore should not be empty")
	}

	// second call is idempotent
	if err := EnsureSessionsGitignore(dir); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	content2, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(content) != string(content2) {
		t.Error("gitignore content should not change on second call")
	}
}

func TestVerifyObject_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var body struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.OID != "abc123" {
			t.Errorf("expected OID abc123, got %s", body.OID)
		}
		if body.Size != 42 {
			t.Errorf("expected size 42, got %d", body.Size)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()

	err := VerifyObject(&Action{Href: server.URL}, "abc123", 42)
	if err != nil {
		t.Fatalf("VerifyObject failed: %v", err)
	}
}

func TestVerifyObject_Failure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"not found"}`, 404)
	}))
	defer server.Close()

	err := VerifyObject(&Action{Href: server.URL}, "abc123", 42)
	if err == nil {
		t.Fatal("expected error from failed verify, got nil")
	}
}

func TestVerifyObject_NilAction(t *testing.T) {
	// nil verify action = no verification required, should succeed silently
	err := VerifyObject(nil, "abc123", 42)
	if err != nil {
		t.Fatalf("expected nil error for nil action, got: %v", err)
	}
}

// --- FindPointerStubsWithMissingBlobs ---

// TestFindPointerStubsWithMissingBlobs_AllExist verifies that no files are reported
// missing when the remote LFS store has all the referenced blobs.
// Failure prevented: false positives blocking sessions from being uploaded when
// blobs are actually present.
func TestFindPointerStubsWithMissingBlobs_AllExist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Objects []BatchObject `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := BatchResponse{Transfer: "basic"}
		for _, obj := range req.Objects {
			// all objects exist — return a download action
			resp.Objects = append(resp.Objects, BatchResponseObject{
				OID:  obj.OID,
				Size: obj.Size,
				Actions: &Actions{
					Download: &Action{Href: "https://storage.example.com/" + obj.OID},
				},
			})
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	sessionDir := t.TempDir()
	content := []byte("real session content for testing")
	writePointerForContent(t, sessionDir, "raw.jsonl", content)

	client := &Client{batchURL: server.URL + "/info/lfs/objects/batch", httpClient: &http.Client{}}
	missing := FindPointerStubsWithMissingBlobs(client, sessionDir, nil)
	if len(missing) != 0 {
		t.Errorf("expected no missing blobs, got: %v", missing)
	}
}

// TestFindPointerStubsWithMissingBlobs_SomeMissing verifies that files whose blobs
// are absent from the remote LFS store are returned as missing.
// Failure prevented: committing pointer stubs whose blobs don't exist in GitLab
// causes pre-receive hook to reject the entire push, blocking ALL sessions.
func TestFindPointerStubsWithMissingBlobs_SomeMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Objects []BatchObject `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := BatchResponse{Transfer: "basic"}
		for i, obj := range req.Objects {
			if i == 0 {
				// first object missing
				resp.Objects = append(resp.Objects, BatchResponseObject{
					OID:   obj.OID,
					Size:  obj.Size,
					Error: &ObjectError{Code: 404, Message: "Object does not exist"},
				})
			} else {
				resp.Objects = append(resp.Objects, BatchResponseObject{
					OID:  obj.OID,
					Size: obj.Size,
					Actions: &Actions{
						Download: &Action{Href: "https://storage.example.com/" + obj.OID},
					},
				})
			}
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	sessionDir := t.TempDir()
	writePointerForContent(t, sessionDir, "raw.jsonl", []byte("raw content"))
	writePointerForContent(t, sessionDir, "summary.md", []byte("# Summary"))

	client := &Client{batchURL: server.URL + "/info/lfs/objects/batch", httpClient: &http.Client{}}
	missing := FindPointerStubsWithMissingBlobs(client, sessionDir, nil)
	if len(missing) != 1 {
		t.Errorf("expected 1 missing blob, got %d: %v", len(missing), missing)
	}
}

// TestFindPointerStubsWithMissingBlobs_NoPointers verifies that sessions with
// real content files (not pointer stubs) return nil without making any API call.
func TestFindPointerStubsWithMissingBlobs_NoPointers(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		http.Error(w, "should not be called", 500)
	}))
	defer server.Close()

	sessionDir := t.TempDir()
	// write real content (large enough to not be a pointer stub)
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionDir, "raw.jsonl"),
		[]byte(`{"metadata":{}}`+"\n"+`{"type":"user","content":"hello","seq":1}`+"\n"),
		0644,
	))

	client := &Client{batchURL: server.URL + "/info/lfs/objects/batch", httpClient: &http.Client{}}
	missing := FindPointerStubsWithMissingBlobs(client, sessionDir, nil)
	if len(missing) != 0 {
		t.Errorf("expected no missing blobs for real content, got: %v", missing)
	}
	if callCount != 0 {
		t.Errorf("expected no API calls for non-pointer files, got %d", callCount)
	}
}

// writePointerForContent writes an LFS pointer stub to sessionDir/filename,
// deriving the OID and size from the provided content.
func writePointerForContent(t *testing.T, sessionDir, filename string, content []byte) {
	t.Helper()
	refs := map[string]FileRef{filename: NewFileRef(content)}
	// write a temporary content file so WritePointerFiles can replace it
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, filename), content, 0644))
	_, err := WritePointerFiles(sessionDir, AssertUploadedManifest(refs))
	require.NoError(t, err)
}
