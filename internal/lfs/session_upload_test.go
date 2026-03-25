package lfs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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
			uploaded[oid] = true
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
			verified[body.OID] = true
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
	if !uploaded[rawOID] {
		t.Error("raw.jsonl blob was not uploaded")
	}
	if !uploaded[summaryOID] {
		t.Error("summary.md blob was not uploaded")
	}

	// verify blobs were verified via the LFS verify action
	if !verified[rawOID] {
		t.Error("raw.jsonl blob was not verified after upload")
	}
	if !verified[summaryOID] {
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
