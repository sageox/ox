package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ox import <audio|video> through the recording upload route ---

const (
	fakeRecordingID = "rec_01960000-0000-7000-8000-000000000001"
	fakeUploadID    = "upl_01960000-0000-7000-8000-000000000002"
)

// uploadRequestWire mirrors api-go's POST …/recordings/upload body (phase-2
// contract, Revision 1), including fields ox never sends. Decoded with
// DisallowUnknownFields so a field ox invents fails the test.
type uploadRequestWire struct {
	Filename         string     `json:"filename"`
	ContentType      string     `json:"content_type"`
	Size             int64      `json:"size"`
	Title            string     `json:"title"`
	RecordedAt       *time.Time `json:"recorded_at"`
	ContentHash      string     `json:"content_hash"`
	ChecksumSHA256   string     `json:"checksum_sha256"`
	Multipart        bool       `json:"multipart"`
	RecordingID      string     `json:"recording_id"`
	CaptureStartedAt *time.Time `json:"capture_started_at"`
}

// confirmRequestWire mirrors the confirm body in the phase-2 API contract.
type confirmRequestWire struct {
	UploadID string `json:"upload_id"`
	Parts    []struct {
		PartNumber int    `json:"part_number"`
		ETag       string `json:"etag"`
	} `json:"parts"`
}

// fakeRecordingServer plays api-go's upload/confirm routes and the presigned
// storage behind them. mode picks the upload answer: "single", "multipart",
// "production" (today's server: the route without any phase-2 field),
// "unsupported" (unrouted 404), or an HTTP status for an error envelope.
type fakeRecordingServer struct {
	t        *testing.T
	baseURL  string
	mode     string
	partSize int64
	errCode  string
	errMsg   string

	mu sync.Mutex
	// failPart: part number whose PUT answers failStatus for its first failTimes attempts
	failPart   int
	failStatus int
	failTimes  int
	// URLs carry the upload request count that minted them; storage refuses
	// any minted before minGeneration with refusalStatus/refusalBody, as S3
	// does an expired URL
	minGeneration int
	refusalStatus int
	refusalBody   string
	// driftUploadID: re-presigns answer a different upload_id
	driftUploadID bool
	// confirmStatus/confirmCode: confirm answers with this error envelope
	confirmStatus int
	confirmCode   string

	uploadReqs    []uploadRequestWire
	uploadBodies  [][]byte
	idemKeys      []string
	confirmBodies [][]byte
	stored        map[int][]byte // part number (0 = single PUT) -> bytes
	attempts      map[int]int
	storageHdrs   []http.Header
	apiHdrs       []http.Header
	notifyBodies  [][]byte // POST …/context/import, the document path's notification
}

func newFakeRecordingServer(t *testing.T, f *importRetryFixture, mode string) *fakeRecordingServer {
	t.Helper()
	s := &fakeRecordingServer{t: t, baseURL: f.endpoint, mode: mode, stored: map[int][]byte{}, attempts: map[int]int{}}
	var h http.Handler = s
	f.api.Store(&h)
	return s
}

// set changes the fake's behavior under its lock.
func (s *fakeRecordingServer) set(fn func(*fakeRecordingServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (s *fakeRecordingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	base := "/api/v1/teams/" + importRetryTeamID
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base+"/recordings/upload":
		s.serveUpload(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/recordings/"+fakeRecordingID+"/confirm":
		s.serveConfirm(w, r)
	case r.Method == http.MethodPost && r.URL.Path == base+"/context/import":
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.notifyBodies = append(s.notifyBodies, body)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"recording_id":"rec_legacy_path"}`))
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/storage/"):
		s.serveStorage(w, r)
	default:
		// unrouted, as on a real server: the document path's LFS client asks
		// GET /api/v1/cli/repos first and falls back on 404
		http.NotFound(w, r)
	}
}

func (s *fakeRecordingServer) serveUpload(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req uploadRequestWire
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.t.Errorf("upload request body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.uploadReqs = append(s.uploadReqs, req)
	s.uploadBodies = append(s.uploadBodies, raw)
	s.idemKeys = append(s.idemKeys, r.Header.Get("Idempotency-Key"))
	s.apiHdrs = append(s.apiHdrs, r.Header.Clone())
	generation := len(s.uploadReqs)
	uploadID := fakeUploadID
	if s.driftUploadID && generation > 1 {
		uploadID = "upl_drifted"
	}
	s.mu.Unlock()

	storageURL := func(path string) string {
		return fmt.Sprintf("%s/storage/%s?gen=%d&X-Amz-Signature=secret", s.baseURL, path, generation)
	}
	switch s.mode {
	case "unsupported":
		// what chi answers for a route that is not mounted
		http.Error(w, "404 page not found", http.StatusNotFound)
	case "production":
		// ignores multipart, checksum_sha256 and Idempotency-Key, as a server
		// that predates them does (its JSON decoder drops unknown fields)
		writeJSON(w, http.StatusCreated, map[string]any{
			"recording_id": fakeRecordingID,
			"upload_url":   storageURL("single"),
		})
	case "single":
		writeJSON(w, http.StatusCreated, map[string]any{
			"recording_id": fakeRecordingID,
			"upload_url":   storageURL("single"),
			"upload":       map[string]string{"upload_url": storageURL("single")},
			"upload_id":    uploadID,
			"upload_headers": map[string]string{
				"Content-Type":          req.ContentType,
				"Content-Length":        strconv.FormatInt(req.Size, 10),
				"x-amz-checksum-sha256": req.ChecksumSHA256,
			},
		})
	case "multipart":
		n := int((req.Size + s.partSize - 1) / s.partSize)
		parts := make([]map[string]any, 0, n)
		// listed in reverse so the client cannot rely on server order
		for i := n; i >= 1; i-- {
			length := s.partSize
			if i == n {
				length = req.Size - int64(n-1)*s.partSize
			}
			parts = append(parts, map[string]any{
				"part_number":    i,
				"upload_url":     storageURL("part/" + strconv.Itoa(i)),
				"upload_headers": map[string]string{"Content-Length": strconv.FormatInt(length, 10)},
			})
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"recording_id": fakeRecordingID,
			"upload_id":    uploadID,
			"multipart": map[string]any{
				"part_size":  s.partSize,
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"parts":      parts,
			},
		})
	default:
		status, err := strconv.Atoi(s.mode)
		if err != nil {
			s.t.Errorf("unknown fake mode %q", s.mode)
			http.Error(w, "bad fake mode", http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		code, msg := s.errCode, s.errMsg
		s.mu.Unlock()
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
	}
}

func (s *fakeRecordingServer) serveStorage(w http.ResponseWriter, r *http.Request) {
	part := 0
	if n, ok := strings.CutPrefix(r.URL.Path, "/storage/part/"); ok {
		part, _ = strconv.Atoi(n)
	}
	generation, _ := strconv.Atoi(r.URL.Query().Get("gen"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if int64(len(body)) != r.ContentLength {
		s.t.Errorf("part %d: Content-Length %d but %d bytes arrived", part, r.ContentLength, len(body))
	}

	s.mu.Lock()
	s.attempts[part]++
	refused := generation < s.minGeneration
	refusalStatus, refusalBody := s.refusalStatus, s.refusalBody
	fail := part == s.failPart && s.attempts[part] <= s.failTimes
	failStatus := s.failStatus
	s.storageHdrs = append(s.storageHdrs, r.Header.Clone())
	s.mu.Unlock()

	switch {
	case refused:
		http.Error(w, refusalBody, refusalStatus)
		return
	case fail:
		http.Error(w, "SlowDown", failStatus)
		return
	}
	// storage checks a signed checksum against the bytes, as S3 does
	if want := r.Header.Get("x-amz-checksum-sha256"); want != "" {
		sum := sha256.Sum256(body)
		if base64.StdEncoding.EncodeToString(sum[:]) != want {
			http.Error(w, "<Code>BadDigest</Code>", http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	s.stored[part] = body
	s.mu.Unlock()
	w.Header().Set("ETag", fmt.Sprintf("%q", fmt.Sprintf("etag-%d", part)))
	w.WriteHeader(http.StatusOK)
}

func (s *fakeRecordingServer) serveConfirm(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.confirmBodies = append(s.confirmBodies, body)
	s.apiHdrs = append(s.apiHdrs, r.Header.Clone())
	status, code, msg := s.confirmStatus, s.confirmCode, s.errMsg
	s.mu.Unlock()
	if status != 0 {
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": fakeRecordingID, "status": "processing"})
}

// fakeSnapshot is a copy of what the server saw, taken under its lock: a part
// that fails for good cancels its siblings, whose handlers may still be
// running when the import returns.
type fakeSnapshot struct {
	uploads      []uploadRequestWire
	uploadBodies [][]byte
	idemKeys     []string
	attempts     map[int]int
	storage      []http.Header
	api          []http.Header
	confirms     [][]byte
	notifies     [][]byte
}

func (s *fakeRecordingServer) snapshot() fakeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := make(map[int]int, len(s.attempts))
	for k, v := range s.attempts {
		attempts[k] = v
	}
	return fakeSnapshot{
		uploads:      append([]uploadRequestWire(nil), s.uploadReqs...),
		uploadBodies: append([][]byte(nil), s.uploadBodies...),
		idemKeys:     append([]string(nil), s.idemKeys...),
		attempts:     attempts,
		storage:      append([]http.Header(nil), s.storageHdrs...),
		api:          append([]http.Header(nil), s.apiHdrs...),
		confirms:     append([][]byte(nil), s.confirmBodies...),
		notifies:     append([][]byte(nil), s.notifyBodies...),
	}
}

// storedLen is the byte count stored for one part.
func (s *fakeRecordingServer) storedLen(part int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stored[part])
}

// reassembled returns the stored bytes in part order.
func (s *fakeRecordingServer) reassembled() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.stored[0]; ok {
		return b
	}
	var out []byte
	for i := 1; i <= len(s.stored); i++ {
		out = append(out, s.stored[i]...)
	}
	return out
}

// strictNotify decodes the single document-path notification the server
// received — what api-go routes to its doc, media, or transcript workflow.
func (s *fakeRecordingServer) strictNotify(t *testing.T) docMeta {
	t.Helper()
	notifies := s.snapshot().notifies
	require.Len(t, notifies, 1, "exactly one /context/import notification")
	var n api.ImportNotification
	dec := json.NewDecoder(bytes.NewReader(notifies[0]))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&n))
	assert.Equal(t, importRetryTeamID, n.TeamID)
	var meta docMeta
	dec = json.NewDecoder(bytes.NewReader(n.Metadata))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&meta))
	return meta
}

// strictConfirm decodes the single confirm body the server received.
func (s *fakeRecordingServer) strictConfirm(t *testing.T) confirmRequestWire {
	t.Helper()
	confirms := s.snapshot().confirms
	require.Len(t, confirms, 1, "exactly one confirm")
	var c confirmRequestWire
	dec := json.NewDecoder(bytes.NewReader(confirms[0]))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&c))
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// mediaFixture is the import fixture plus a logged-in user and a media file.
type mediaFixture struct {
	*importRetryFixture
	media   string
	content []byte
}

// newMediaFixture writes a media file of size bytes next to the fixture's document.
func newMediaFixture(t *testing.T, name string, size int) *mediaFixture {
	t.Helper()
	f := newImportRetryFixture(t)
	require.NoError(t, auth.SaveTokenForEndpoint(f.endpoint, &auth.StoredToken{
		AccessToken: "test-access-token", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour), TokenType: "Bearer",
	}))
	// a login mints git credentials bound to its token and well clear of
	// gitserver.NearExpiryThreshold; otherwise the document path's LFS client
	// re-mints them from the API
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(f.endpoint, gitserver.GitCredentials{
		Username: "oauth2", Token: "test-token", ServerURL: f.endpoint, ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("test-access-token"),
	}))
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i*7 + i/251) // not periodic in the part size, so a misplaced part changes the bytes
	}
	media := filepath.Join(filepath.Dir(f.src), name)
	require.NoError(t, os.WriteFile(media, content, 0o644))

	prior := mediaUploadRetryBackoff
	mediaUploadRetryBackoff = time.Millisecond
	t.Cleanup(func() { mediaUploadRetryBackoff = prior })
	return &mediaFixture{importRetryFixture: f, media: media, content: content}
}

// importMedia runs `ox import <media> --team <team> --date 2026-09-19 [--force] [--json]`.
func (f *mediaFixture) importMedia(force, jsonOut bool) (stdout, stderr string, err error) {
	importFlags = importFlagsT{team: importRetryTeamID, date: "2026-09-19", text: f.text, force: force}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", jsonOut, "")
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err = runImport(cmd, []string{f.media})
	return out.String(), errOut.String(), err
}

// tcPath is the fixture's team context clone.
func (f *mediaFixture) tcPath() string { return filepath.Dir(filepath.Dir(f.docsDir)) }

func (f *mediaFixture) sha256Hex() string {
	sum := sha256.Sum256(f.content)
	return hex.EncodeToString(sum[:])
}

func (f *mediaFixture) sha256Base64() string {
	sum := sha256.Sum256(f.content)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// assertNoDocumentPath checks nothing reached LFS or the team context remote.
func (f *mediaFixture) assertNoDocumentPath(t *testing.T) {
	t.Helper()
	assert.Zero(t, f.uploads.Load(), "media must not upload through LFS")
	assert.NotContains(t, runGit(t, f.bare, "log", "--format=%s"), "import: doc", "media must not commit to the team context")
}

// TestImportMedia_SinglePutStreamsFileAndConfirms covers the whole small-file
// route: the upload request carries the contract fields, the bytes arrive
// intact under every signed header, and confirm names the upload.
// Failure prevented: an audio import that still goes through LFS + git (the
// 5-minute cap), a PUT storage refuses for a missing signed header, or a
// confirm the server cannot match to its upload.
func TestImportMedia_SinglePutStreamsFileAndConfirms(t *testing.T) {
	f := newMediaFixture(t, "standup.m4a", 4096)
	srv := newFakeRecordingServer(t, f.importRetryFixture, "single")

	stdout, stderr, err := f.importMedia(false, false)
	require.NoError(t, err)

	snap := srv.snapshot()
	require.Len(t, snap.uploads, 1)
	recordedAt := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, uploadRequestWire{
		Filename:       "standup.m4a",
		ContentType:    "audio/mp4",
		Size:           4096,
		Title:          "standup",
		RecordedAt:     &recordedAt,
		ContentHash:    f.sha256Hex(),
		ChecksumSHA256: f.sha256Base64(),
		Multipart:      true,
	}, snap.uploads[0])
	key, err := uuid.Parse(snap.idemKeys[0])
	require.NoError(t, err, "Idempotency-Key must be a UUID")
	assert.Equal(t, uuid.Version(7), key.Version())

	assert.Equal(t, f.content, srv.reassembled(), "the stored object must be the file")
	require.Len(t, snap.storage, 1)
	put := snap.storage[0]
	assert.Equal(t, "audio/mp4", put.Get("Content-Type"))
	assert.Equal(t, f.sha256Base64(), put.Get("x-amz-checksum-sha256"), "every upload_headers entry is sent")
	assert.Empty(t, put.Get("Authorization"), "the SageOx token must never reach storage")
	assert.Empty(t, put.Get("X-Client-Type"), "storage is not a SageOx API")
	assert.Empty(t, put.Get("Idempotency-Key"))

	c := srv.strictConfirm(t)
	assert.Equal(t, fakeUploadID, c.UploadID)
	assert.Empty(t, c.Parts, "a single PUT confirms without parts")

	require.Len(t, snap.api, 2, "upload and confirm")
	for _, h := range snap.api {
		assert.Equal(t, "cli", h.Get("X-Client-Type"))
		assert.Equal(t, "Bearer test-access-token", h.Get("Authorization"))
		assert.True(t, strings.HasPrefix(h.Get("User-Agent"), "ox/"))
	}

	assert.Contains(t, stdout, "Imported: standup\nRecording: "+fakeRecordingID+"\n")
	assert.Contains(t, stdout, "Track progress: ox import --status "+fakeRecordingID+" --watch --team "+importRetryTeamID)
	assert.Empty(t, stderr, "a 4 KB upload prints no progress")
	f.assertNoDocumentPath(t)
}

// TestImportMedia_ProductionServerWithoutPhase2Fields runs a new CLI against
// today's production server: /recordings/upload exists but knows none of the
// phase-2 fields, answers only {recording_id, upload_url}, and confirms with
// no body.
// Failure prevented: a CLI release that breaks media import on every server
// deployed before the phase-2 contract.
func TestImportMedia_ProductionServerWithoutPhase2Fields(t *testing.T) {
	f := newMediaFixture(t, "standup.m4a", 4096)
	srv := newFakeRecordingServer(t, f.importRetryFixture, "production")

	stdout, stderr, err := f.importMedia(false, false)
	require.NoError(t, err)

	snap := srv.snapshot()
	require.Len(t, snap.uploads, 1, "one upload request: no re-presign")
	require.Len(t, snap.storage, 1, "one single PUT")
	assert.Equal(t, "audio/mp4", snap.storage[0].Get("Content-Type"), "Content-Type is what that server signed")
	assert.Empty(t, snap.storage[0].Get("x-amz-checksum-sha256"), "nothing unsigned is invented")
	assert.Equal(t, f.content, srv.reassembled())
	require.Len(t, snap.confirms, 1)
	assert.Empty(t, snap.confirms[0], "no upload_id, so confirm has no body")
	assert.Contains(t, stdout, "Recording: "+fakeRecordingID)
	assert.Empty(t, stderr)
	f.assertNoDocumentPath(t)
}

// TestImportMedia_ProductionServerCannotRepresign: with no upload_id, a
// refused URL fails the import instead of re-sending the upload request.
// Failure prevented: a "re-presign" against a server that ignores
// Idempotency-Key, which would create a second recording.
func TestImportMedia_ProductionServerCannotRepresign(t *testing.T) {
	f := newMediaFixture(t, "standup.m4a", 4096)
	srv := newFakeRecordingServer(t, f.importRetryFixture, "production")
	srv.set(func(s *fakeRecordingServer) {
		s.minGeneration, s.refusalStatus, s.refusalBody = 100, http.StatusForbidden, "<Code>AccessDenied</Code>"
	})

	_, _, err := f.importMedia(false, false)

	require.ErrorContains(t, err, "storage answered HTTP 403")
	snap := srv.snapshot()
	assert.Len(t, snap.uploads, 1, "no second upload request")
	assert.Empty(t, snap.confirms)
}

// TestImportMedia_MultipartRetriesFailedPartAndConfirmsEveryETag uploads in
// parts, one of which fails once with a 503.
// Failure prevented: one transient storage error discarding a large upload,
// or a confirm whose parts are missing, misordered, or carry the wrong ETags.
func TestImportMedia_MultipartRetriesFailedPartAndConfirmsEveryETag(t *testing.T) {
	f := newMediaFixture(t, "all-hands.mp4", 3000)
	srv := newFakeRecordingServer(t, f.importRetryFixture, "multipart")
	srv.set(func(s *fakeRecordingServer) {
		s.partSize = 1024
		s.failPart, s.failStatus, s.failTimes = 2, http.StatusServiceUnavailable, 1
	})

	_, _, err := f.importMedia(false, false)
	require.NoError(t, err)

	snap := srv.snapshot()
	assert.Equal(t, map[int]int{1: 1, 2: 2, 3: 1}, snap.attempts, "only the failed part is retried, once")
	assert.Len(t, snap.uploads, 1, "a transient failure is retried, not re-presigned")
	assert.Equal(t, 3000-2*1024, srv.storedLen(3), "the last part carries the remainder")
	assert.Equal(t, f.content, srv.reassembled(), "parts must cover the file exactly, in order")
	for _, h := range snap.storage {
		assert.Empty(t, h.Get("Content-Type"), "part URLs are signed without a Content-Type")
		assert.Empty(t, h.Get("Authorization"))
	}

	c := srv.strictConfirm(t)
	assert.Equal(t, fakeUploadID, c.UploadID)
	require.Len(t, c.Parts, 3)
	for i, p := range c.Parts {
		assert.Equal(t, i+1, p.PartNumber)
		assert.Equal(t, fmt.Sprintf("%q", fmt.Sprintf("etag-%d", i+1)), p.ETag, "ETags are passed through unchanged, quotes included")
	}
	f.assertNoDocumentPath(t)
}

// TestImportMedia_PartFailures checks which part failures end the import.
// Failure prevented: retrying forever, retrying a refusal another attempt
// cannot change, or confirming an upload that never completed.
func TestImportMedia_PartFailures(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		wantAttempts int
		wantErr      string
	}{
		{"server error retried until the budget runs out", http.StatusInternalServerError, mediaUploadRetries + 1, "after 4 attempts"},
		{"bad request is not retried", http.StatusBadRequest, 1, "storage answered HTTP 400"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newMediaFixture(t, "all-hands.mp4", 3000)
			srv := newFakeRecordingServer(t, f.importRetryFixture, "multipart")
			srv.set(func(s *fakeRecordingServer) {
				s.partSize = 1024
				s.failPart, s.failStatus, s.failTimes = 2, tt.status, 100
			})

			_, _, err := f.importMedia(false, false)

			require.ErrorContains(t, err, "part 2")
			require.ErrorContains(t, err, tt.wantErr)
			snap := srv.snapshot()
			assert.Equal(t, tt.wantAttempts, snap.attempts[2])
			assert.Len(t, snap.uploads, 1, "neither failure is a refused URL")
			assert.Empty(t, snap.confirms, "an incomplete upload must not be confirmed")
			_, found := findMediaImportByOID(f.tcPath(), f.sha256Hex())
			assert.False(t, found, "a failed import leaves no record")
			f.assertNoDocumentPath(t)

			// the failed import must not block a retry
			srv.set(func(s *fakeRecordingServer) { s.failPart = 0 })
			_, _, err = f.importMedia(false, false)
			require.NoError(t, err)
			_, found = findMediaImportByOID(f.tcPath(), f.sha256Hex())
			assert.True(t, found)
		})
	}
}

// TestImportMedia_ExpiredURLsAreRepresigned: storage refusing a URL as expired
// renews the URLs through the same upload request and the same key.
// Failure prevented: a slow multi-gigabyte upload thrown away at the URL
// expiry, a renewal that creates a second recording, or every part asking
// for its own renewal at once.
func TestImportMedia_ExpiredURLsAreRepresigned(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		status int
		body   string
	}{
		{"single PUT, expired signature", "single", http.StatusForbidden, "<Code>AccessDenied</Code><Message>Request has expired</Message>"},
		{"single PUT, expired signing credentials", "single", http.StatusBadRequest, "<Code>ExpiredToken</Code>"},
		{"every part expired at once", "multipart", http.StatusForbidden, "<Code>AccessDenied</Code><Message>Request has expired</Message>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newMediaFixture(t, "all-hands.mp4", 3000)
			srv := newFakeRecordingServer(t, f.importRetryFixture, tt.mode)
			srv.set(func(s *fakeRecordingServer) {
				s.partSize = 1024
				s.minGeneration, s.refusalStatus, s.refusalBody = 2, tt.status, tt.body // first URLs are stale
			})

			_, _, err := f.importMedia(false, false)
			require.NoError(t, err)

			snap := srv.snapshot()
			require.Len(t, snap.uploads, 2, "one re-presign renews every URL")
			assert.Equal(t, snap.idemKeys[0], snap.idemKeys[1], "a re-presign reuses the import's Idempotency-Key")
			assert.NotEmpty(t, snap.idemKeys[0])
			assert.Equal(t, snap.uploadBodies[0], snap.uploadBodies[1], "a changed body under the same key is a 422")
			assert.Equal(t, f.content, srv.reassembled())
			assert.Equal(t, fakeUploadID, srv.strictConfirm(t).UploadID)
		})
	}
}

// TestImportMedia_RepresignLimits: renewal is bounded, and a renewal that
// names a different upload is refused.
// Failure prevented: an import looping on a 403 that is not about expiry, or
// uploading parts of one upload under another's URLs.
func TestImportMedia_RepresignLimits(t *testing.T) {
	t.Run("storage keeps refusing", func(t *testing.T) {
		f := newMediaFixture(t, "standup.m4a", 4096)
		srv := newFakeRecordingServer(t, f.importRetryFixture, "single")
		srv.set(func(s *fakeRecordingServer) {
			s.minGeneration, s.refusalStatus, s.refusalBody = 100, http.StatusForbidden, "<Code>SignatureDoesNotMatch</Code>"
		})

		_, _, err := f.importMedia(false, false)

		require.ErrorContains(t, err, "after 3 re-presigns")
		require.ErrorContains(t, err, "SignatureDoesNotMatch", "storage's own refusal is kept")
		snap := srv.snapshot()
		assert.Len(t, snap.uploads, 1+mediaUploadMaxRepresigns)
		assert.Empty(t, snap.confirms)
	})

	t.Run("server names a different upload", func(t *testing.T) {
		f := newMediaFixture(t, "standup.m4a", 4096)
		srv := newFakeRecordingServer(t, f.importRetryFixture, "single")
		srv.set(func(s *fakeRecordingServer) {
			s.minGeneration, s.refusalStatus, s.refusalBody = 2, http.StatusForbidden, "<Code>AccessDenied</Code>"
			s.driftUploadID = true
		})

		_, _, err := f.importMedia(false, false)

		require.ErrorContains(t, err, `re-presign returned upload "upl_drifted"`)
		snap := srv.snapshot()
		assert.Len(t, snap.storage, 1, "nothing is PUT under the other upload's URLs")
		assert.Empty(t, snap.confirms)
	})
}

// TestImportMedia_ConfirmRefusalShowsServerCode: confirm's 409s reach the
// person with the server's code and message, and no local record is written.
// Failure prevented: a checksum or size mismatch reported as a generic error,
// or recorded locally as imported so the retry is refused as a duplicate.
func TestImportMedia_ConfirmRefusalShowsServerCode(t *testing.T) {
	for _, code := range []string{"upload_size_mismatch", "upload_checksum_mismatch", "upload_object_missing"} {
		t.Run(code, func(t *testing.T) {
			f := newMediaFixture(t, "standup.m4a", 4096)
			srv := newFakeRecordingServer(t, f.importRetryFixture, "single")
			srv.set(func(s *fakeRecordingServer) {
				s.confirmStatus, s.confirmCode, s.errMsg = http.StatusConflict, code, "stored object does not match the declared upload"
			})

			_, _, err := f.importMedia(false, false)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "HTTP 409 "+code+": stored object does not match the declared upload")
			_, found := findMediaImportByOID(f.tcPath(), f.sha256Hex())
			assert.False(t, found, "an unconfirmed upload is not recorded as imported")
		})
	}
}

// TestImportMedia_UnsupportedServerFallsBackToDocumentPath: a server without
// the upload route answers 404, and audio and video still land through LFS +
// /context/import, the path that server already transcribes from.
// Failure prevented: a new CLI failing every media import against an older
// server.
func TestImportMedia_UnsupportedServerFallsBackToDocumentPath(t *testing.T) {
	tests := []struct {
		file        string
		contentType string
	}{
		{"standup.m4a", "audio/mp4"},
		{"all-hands.mp4", "video/mp4"},
		{"design-review.mov", "video/quicktime"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			f := newMediaFixture(t, tt.file, 4096)
			srv := newFakeRecordingServer(t, f.importRetryFixture, "unsupported")

			stdout, stderr, err := f.importMedia(false, false)
			require.NoError(t, err)

			snap := srv.snapshot()
			require.Len(t, snap.uploads, 1, "the upload route is tried first")
			assert.Empty(t, snap.storage, "nothing is PUT to storage")
			assert.Contains(t, stderr, "does not accept recording uploads")
			assert.Positive(t, f.uploads.Load(), "the file goes through LFS")
			slug := strings.TrimSuffix(tt.file, filepath.Ext(tt.file))
			assert.Equal(t, "import: doc "+slug, runGit(t, f.bare, "log", "-1", "--format=%s"))
			assert.FileExists(t, filepath.Join(f.docsDir, "2026", "09", "19", slug, "metadata.json"))

			meta := srv.strictNotify(t)
			assert.Equal(t, tt.file, meta.SourceFilename)
			assert.Equal(t, tt.contentType, meta.ContentType, "the content type the server routes media on")
			assert.Equal(t, "sha256:"+f.sha256Hex(), meta.SourceOID)
			assert.Contains(t, stdout, "Track progress: ox import --status rec_legacy_path")
		})
	}
}

// TestImport_TranscriptsKeepDocumentPath: .vtt and .srt files are not media.
// They take LFS + /context/import exactly as before, which the server routes
// to its import-transcript workflow, even when the recording upload route
// is available.
// Failure prevented: a transcript sent to the media upload route, which only
// accepts audio and video, or transcript imports silently changing storage.
func TestImport_TranscriptsKeepDocumentPath(t *testing.T) {
	tests := []struct {
		file    string
		content string
	}{
		{"standup.vtt", "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nPerson A: good morning\n"},
		{"retro.srt", "1\n00:00:00,000 --> 00:00:02,000\nPerson B: let's start\n"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			f := newMediaFixture(t, tt.file, 1)
			require.NoError(t, os.WriteFile(f.media, []byte(tt.content), 0o644))
			f.content = []byte(tt.content)
			srv := newFakeRecordingServer(t, f.importRetryFixture, "single")

			stdout, stderr, err := f.importMedia(false, false)
			require.NoError(t, err)

			snap := srv.snapshot()
			assert.Empty(t, snap.uploads, "the recording upload route must not be called")
			assert.Empty(t, snap.storage)
			assert.Empty(t, stderr, "no fallback warning: this is the transcript's own path")
			assert.Positive(t, f.uploads.Load(), "the transcript goes through LFS")
			slug := strings.TrimSuffix(tt.file, filepath.Ext(tt.file))
			assert.Equal(t, "import: doc "+slug, runGit(t, f.bare, "log", "-1", "--format=%s"))
			assert.FileExists(t, filepath.Join(f.docsDir, "2026", "09", "19", slug, tt.file), "the pointer file is committed")

			meta := srv.strictNotify(t)
			assert.Equal(t, tt.file, meta.SourceFilename, "the server routes transcripts by this file")
			assert.Equal(t, "sha256:"+f.sha256Hex(), meta.SourceOID)
			assert.Equal(t, int64(len(tt.content)), meta.SourceSize)
			assert.Contains(t, stdout, "Imported: "+slug)
		})
	}
}

// TestImportMedia_RefusalShowsServerMessage: a 403, 409, or deliberate 404
// (api-go's error envelope) on the upload request reaches the person with
// the server's own words, and nothing falls back or uploads.
// Failure prevented: a generic error hiding why the server said no, or a
// refused upload — "team not found" included — quietly retried through the
// document path.
func TestImportMedia_RefusalShowsServerMessage(t *testing.T) {
	tests := []struct {
		status int
		code   string
		msg    string
	}{
		{http.StatusForbidden, "forbidden", "you are not a member of this team"},
		{http.StatusConflict, "conflict", "recording is not accepting uploads: status=ready"},
		{http.StatusNotFound, "not_found", "team not found"},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.status), func(t *testing.T) {
			f := newMediaFixture(t, "standup.m4a", 4096)
			srv := newFakeRecordingServer(t, f.importRetryFixture, strconv.Itoa(tt.status))
			srv.set(func(s *fakeRecordingServer) { s.errCode, s.errMsg = tt.code, tt.msg })

			_, stderr, err := f.importMedia(false, false)

			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("HTTP %d %s: %s", tt.status, tt.code, tt.msg), "the server's code and message, verbatim")
			assert.NotContains(t, stderr, "does not accept recording uploads", "a deliberate answer is not a missing route")
			snap := srv.snapshot()
			assert.Len(t, snap.uploads, 1)
			assert.Empty(t, snap.storage)
			assert.Empty(t, snap.notifies)
			f.assertNoDocumentPath(t)
		})
	}
}

// TestImportMedia_SecondImportIsRecognized keeps the source-OID dedup the
// document path has: the same bytes import once unless --force.
// Failure prevented: re-running an import (an AI coworker retrying a command)
// creating a duplicate recording and a second transcription bill.
func TestImportMedia_SecondImportIsRecognized(t *testing.T) {
	f := newMediaFixture(t, "standup.m4a", 4096)
	srv := newFakeRecordingServer(t, f.importRetryFixture, "single")

	_, _, err := f.importMedia(false, false)
	require.NoError(t, err)

	stdout, _, err := f.importMedia(false, false)
	require.NoError(t, err)
	assert.Equal(t, "Already imported (id: "+fakeRecordingID+"). Use --force to reimport.\n", stdout)

	stdout, _, err = f.importMedia(false, true)
	require.NoError(t, err)
	var res importResult
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&res))
	assert.Equal(t, importResult{Status: "already_imported", ID: fakeRecordingID, RecordingID: fakeRecordingID}, res)
	assert.Len(t, srv.snapshot().uploads, 1, "a recognized import makes no upload request")

	_, _, err = f.importMedia(true, false)
	require.NoError(t, err)
	snap := srv.snapshot()
	require.Len(t, snap.uploads, 2, "--force imports again")
	assert.NotEqual(t, snap.idemKeys[0], snap.idemKeys[1], "a new import is a new key, never a replay of the old upload")
}

// TestImportMedia_JSONOutput: --json reports the recording like a document import.
func TestImportMedia_JSONOutput(t *testing.T) {
	f := newMediaFixture(t, "standup.m4a", 4096)
	newFakeRecordingServer(t, f.importRetryFixture, "single")

	stdout, _, err := f.importMedia(false, true)
	require.NoError(t, err)

	var res importResult
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&res))
	assert.Equal(t, importResult{
		Status:      "imported",
		Title:       "standup",
		TeamID:      importRetryTeamID,
		SourceOID:   "sha256:" + f.sha256Hex(),
		ContentType: "audio/mp4",
		RecordingID: fakeRecordingID,
	}, res)
}

// TestImport_DocumentPathUnchanged: documents, and media imported with --text,
// never touch the recording upload route.
// Failure prevented: a PDF sent to the transcription pipeline, or --text
// silently dropped from a media import.
func TestImport_DocumentPathUnchanged(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		withTxt bool
	}{
		{"markdown document", "q3-plan.md", false},
		{"media with --text", "standup.m4a", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newMediaFixture(t, tt.file, 2048)
			srv := newFakeRecordingServer(t, f.importRetryFixture, "single")
			if tt.withTxt {
				f.text = filepath.Join(filepath.Dir(f.media), "extracted.md")
				require.NoError(t, os.WriteFile(f.text, []byte("transcript\n"), 0o644))
			}

			_, _, err := f.importMedia(false, false)
			require.NoError(t, err)

			assert.Empty(t, srv.snapshot().uploads, "the recording upload route must not be called")
			assert.Positive(t, f.uploads.Load())
			slug := strings.TrimSuffix(tt.file, filepath.Ext(tt.file))
			assert.Equal(t, "import: doc "+slug, runGit(t, f.bare, "log", "-1", "--format=%s"))
		})
	}
}

// TestPlanRecordingUpload rejects plans that do not cover the file exactly or
// that sign a length ox will not send.
// Failure prevented: uploading gigabytes against a plan the server can only
// reject at confirm, or a part quietly sent over plain http.
func TestPlanRecordingUpload(t *testing.T) {
	part := func(n int, length string) api.MultipartPart {
		p := api.MultipartPart{PartNumber: n, UploadURL: fmt.Sprintf("https://bucket.example.test/p%d?sig=x", n)}
		if length != "" {
			p.UploadHeaders = map[string]string{"Content-Length": length}
		}
		return p
	}
	single := "https://bucket.example.test/o?sig=x"
	tests := []struct {
		name        string
		resp        api.RecordingUploadResponse
		wantErr     string
		wantLengths []int64
		wantHeaders map[string]string // first span's
	}{
		{"older server single PUT signs Content-Type", api.RecordingUploadResponse{UploadURL: single}, "", []int64{2500}, map[string]string{"Content-Type": "audio/mp4"}},
		{"upload_headers sent as given", api.RecordingUploadResponse{UploadURL: single, UploadHeaders: map[string]string{"Content-Length": "2500", "x-amz-checksum-sha256": "abc="}}, "", []int64{2500}, map[string]string{"Content-Length": "2500", "x-amz-checksum-sha256": "abc="}},
		{"signed length differs from file", api.RecordingUploadResponse{UploadURL: single, UploadHeaders: map[string]string{"content-length": "2499"}}, "signed Content-Length", nil, nil},
		{"parts cover file", api.RecordingUploadResponse{UploadID: "upl_1", Multipart: &api.MultipartUpload{PartSize: 1000, Parts: []api.MultipartPart{part(3, "500"), part(1, "1000"), part(2, "1000")}}}, "", []int64{1000, 1000, 500}, map[string]string{"Content-Length": "1000"}},
		{"older server parts send nothing extra", api.RecordingUploadResponse{UploadID: "upl_1", Multipart: &api.MultipartUpload{PartSize: 1000, Parts: []api.MultipartPart{part(1, ""), part(2, ""), part(3, "")}}}, "", []int64{1000, 1000, 500}, nil},
		{"last part signed at full size", api.RecordingUploadResponse{UploadID: "upl_1", Multipart: &api.MultipartUpload{PartSize: 1000, Parts: []api.MultipartPart{part(1, "1000"), part(2, "1000"), part(3, "1000")}}}, "part 3: server signed Content-Length", nil, nil},
		{"too few parts", api.RecordingUploadResponse{UploadID: "upl_1", Multipart: &api.MultipartUpload{PartSize: 1000, Parts: []api.MultipartPart{part(1, ""), part(2, "")}}}, "needs 3", nil, nil},
		{"gap in numbering", api.RecordingUploadResponse{UploadID: "upl_1", Multipart: &api.MultipartUpload{PartSize: 1000, Parts: []api.MultipartPart{part(1, ""), part(2, ""), part(4, "")}}}, "not numbered", nil, nil},
		{"no upload_id", api.RecordingUploadResponse{Multipart: &api.MultipartUpload{PartSize: 1000, Parts: []api.MultipartPart{part(1, ""), part(2, ""), part(3, "")}}}, "no upload_id", nil, nil},
		{"zero part size", api.RecordingUploadResponse{UploadID: "upl_1", Multipart: &api.MultipartUpload{}}, "invalid part_size", nil, nil},
		{"plain http off loopback", api.RecordingUploadResponse{UploadURL: "http://bucket.example.test/o"}, "must use https", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spans, err := planRecordingUpload(2500, "audio/mp4", &tt.resp)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			var lengths []int64
			var next int64
			for _, s := range spans {
				assert.Equal(t, next, s.offset, "spans must be contiguous")
				next += s.length
				lengths = append(lengths, s.length)
			}
			assert.Equal(t, tt.wantLengths, lengths)
			assert.Equal(t, tt.wantHeaders, spans[0].headers)
		})
	}
}

// TestUploadProgress prints quarters for a large upload off a terminal, and
// nothing for a small one.
// Failure prevented: an AI coworker's transcript flooded with progress lines,
// or a retried part counted twice and reported past 100%.
func TestUploadProgress(t *testing.T) {
	var buf bytes.Buffer
	assert.Nil(t, newUploadProgress(&buf, "small.m4a", progressThresholdBytes), "at the threshold, quiet")

	total := int64(progressThresholdBytes * 4)
	p := newUploadProgress(&buf, "big.mp4", total)
	require.NotNil(t, p)
	p.add(total / 2)  // 0 -> 50%: two quarters at once, one line
	p.add(-total / 4) // a failed attempt is un-counted: back to 25%
	p.add(total / 4)  // 50% again, already printed
	p.add(total / 4)  // 75%
	p.add(total)      // clamped at 100%
	p.finish()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3, "a repeated quarter never prints twice: %q", buf.String())
	assert.Equal(t, "Uploading big.mp4: 100.0 MB / 200.0 MB (50%)", lines[0])
	assert.Equal(t, "Uploading big.mp4: 150.0 MB / 200.0 MB (75%)", lines[1])
	assert.Equal(t, "Uploading big.mp4: 200.0 MB / 200.0 MB (100%)", lines[2])
}

// TestUploadProgress_Terminal redraws one line in place on a terminal: at most
// five times a second, always at 100%, and blank-padded so a shorter line
// fully covers a longer one.
// Failure prevented: a terminal flooded with redraws, a final "(100%)" line
// left with stale characters from the line under it, or the next output
// starting on the progress line.
func TestUploadProgress_Terminal(t *testing.T) {
	var buf bytes.Buffer
	total := int64(1 << 30)
	p := &uploadProgress{w: &buf, name: "big.mp4", total: total, tty: true}

	p.add(total / 2) // first draw
	// pin the clock inside the throttle window so the next draws are skipped
	p.lastDraw = time.Now().Add(time.Hour)
	p.add(1)         // throttled
	p.add(-total)    // a failed attempt un-counted past zero clamps at 0; throttled
	p.add(total * 2) // clamped at 100%, which always draws
	p.finish()

	assert.Equal(t,
		"\rUploading big.mp4: 512.0 MB / 1.0 GB (50%)"+
			"\rUploading big.mp4: 1.0 GB / 1.0 GB (100%) "+
			"\n",
		buf.String())
}

// TestMediaContentTypesMatchServerPairing pins the MIME ox declares for each
// media extension to one api-go's mediaformats pairs with that extension.
// Failure prevented: a 400 "content_type does not match file extension" on
// every import of a format.
func TestMediaContentTypesMatchServerPairing(t *testing.T) {
	// from api-go packages/mediaformats directUploadFormats
	server := map[string][]string{
		".m4a": {"audio/x-m4a", "audio/m4a", "audio/mp4"}, ".mp3": {"audio/mpeg", "audio/mp3"},
		".mp4": {"video/mp4", "audio/mp4"}, ".mov": {"video/quicktime"}, ".m4v": {"video/x-m4v"},
		".wav": {"audio/wav", "audio/wave", "audio/x-wav"}, ".webm": {"audio/webm", "video/webm"},
		".ogg": {"audio/ogg", "application/ogg"}, ".opus": {"audio/opus", "audio/ogg"},
		".flac": {"audio/flac", "audio/x-flac"}, ".aac": {"audio/aac", "audio/x-aac"}, ".wma": {"audio/x-ms-wma"},
	}
	require.Len(t, mediaImportExtensions, len(server), "routing and the server's accepted formats must list the same extensions")
	for ext := range mediaImportExtensions {
		assert.Contains(t, server[ext], detectContentType("file"+ext, nil), ext)
		assert.True(t, isMediaImportFile("Meeting"+strings.ToUpper(ext)), "extension match is case-insensitive: %s", ext)
	}
	assert.False(t, isMediaImportFile("clip.mkv"), "mkv is not accepted by the upload route")
	assert.False(t, isMediaImportFile("notes.md"))
}
