package livesessiontest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/sessionstream"
)

// mediaState is the fake's attachment lane: declare, a storage PUT that stands
// in for the presigned S3 URL, and complete. Declared objects are kept per
// conversation and digest, as the real handler's content-addressed keys are.
type mediaState struct {
	mu          sync.Mutex
	stored      map[string][]byte // cnv/sha -> bytes
	mime        map[string]string // cnv/sha -> content type the PUT carried
	failUploads int               // next n storage PUTs answer 503
	refuseMIME  string            // declare answers 413 with this reason when set (too large)
	puts        int
	failStatus  int // every storage PUT answers this status when set
	redirectTo  string
}

func (m *mediaState) key(cnv, sha string) string { return cnv + "/" + sha }

// FailMediaUploads makes the next n storage PUTs answer 503.
func (s *Server) FailMediaUploads(n int) {
	s.media.mu.Lock()
	defer s.media.mu.Unlock()
	s.media.failUploads = n
}

// RefuseMediaDeclares makes every declare answer 413 (the server's size cap).
func (s *Server) RefuseMediaDeclares() { s.set(func() { s.media.refuseMIME = "too_large" }) }

// MediaPuts is how many storage PUTs the fake received.
func (s *Server) MediaPuts() int {
	s.media.mu.Lock()
	defer s.media.mu.Unlock()
	return s.media.puts
}

// StoredMedia returns the bytes stored for a digest and the content type they
// arrived with; ok is false when none were stored.
func (s *Server) StoredMedia(t testing.TB, cnv, sha string) (data []byte, mime string, ok bool) {
	t.Helper()
	s.media.mu.Lock()
	defer s.media.mu.Unlock()
	data, ok = s.media.stored[s.media.key(cnv, sha)]
	return data, s.media.mime[s.media.key(cnv, sha)], ok
}

func (s *Server) registerMedia(mux *http.ServeMux) {
	s.media = &mediaState{stored: map[string][]byte{}, mime: map[string]string{}}
	mux.HandleFunc("POST "+sessionstream.PathSessions+"/{cnv}/media", s.route(s.declareMedia))
	mux.HandleFunc("POST "+sessionstream.PathSessions+"/{cnv}/media/{sha}/complete", s.route(s.completeMedia))
	mux.HandleFunc("PUT /storage/{cnv}/{sha}", s.putMedia)
}

func (s *Server) declareMedia(r *http.Request, body []byte, _ *Call) reply {
	cnv := r.PathValue("cnv")
	var req sessionstream.MediaRequest
	if json.Unmarshal(body, &req) != nil || !sessionstream.ValidSHA256Hex(req.SHA256) || req.Size <= 0 || req.MIME == "" {
		return refusal(http.StatusBadRequest, sessionstream.ReasonInvalidRequest, sessionstream.ActionFix, nil)
	}
	if req.Size > sessionstream.MaxMediaBytes || s.media.refuseMIME != "" {
		return refusal(http.StatusRequestEntityTooLarge, sessionstream.ReasonTooLarge, sessionstream.ActionFix, nil)
	}
	c := s.convs[cnv]
	if c == nil {
		return refusal(http.StatusNotFound, sessionstream.ReasonNotFound, sessionstream.ActionStop, nil)
	}
	if rep := s.liveGuard(c); rep != nil {
		return *rep
	}
	s.media.mu.Lock()
	_, have := s.media.stored[s.media.key(cnv, req.SHA256)]
	s.media.mu.Unlock()
	if have {
		return reply{status: http.StatusOK, body: sessionstream.MediaResponse{SHA256: req.SHA256, AlreadyStored: true}}
	}
	exp := time.Now().Add(sessionstream.MediaUploadURLTTL)
	return reply{status: http.StatusOK, body: sessionstream.MediaResponse{
		SHA256: req.SHA256, UploadURL: s.URL + "/storage/" + cnv + "/" + req.SHA256,
		UploadHeaders: map[string]string{"x-test-checksum-sha256": req.SHA256}, ExpiresAt: &exp,
	}}
}

func (s *Server) completeMedia(r *http.Request, _ []byte, _ *Call) reply {
	cnv, sha := r.PathValue("cnv"), r.PathValue("sha")
	s.media.mu.Lock()
	data, have := s.media.stored[s.media.key(cnv, sha)]
	s.media.mu.Unlock()
	if !have || len(data) == 0 {
		return refusal(http.StatusConflict, sessionstream.ReasonNotFound, sessionstream.ActionRetry, nil)
	}
	return reply{status: http.StatusOK, body: map[string]bool{"stored": true}}
}

// putMedia is the storage side: no login, no signature, as a presigned URL.
func (s *Server) putMedia(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	cnv, sha := r.PathValue("cnv"), r.PathValue("sha")
	status, location := s.storePut(r, cnv, sha, data)
	// recorded after media.mu is released, with the status actually sent
	s.record(Call{Method: r.Method, Path: r.URL.Path, Offset: -1, Status: status, BodyLen: len(data)})
	if location != "" {
		w.Header().Set("Location", location)
	}
	w.WriteHeader(status)
}

// storePut decides the storage answer and, for an accepted upload, keeps it.
func (s *Server) storePut(r *http.Request, cnv, sha string, data []byte) (status int, location string) {
	s.media.mu.Lock()
	defer s.media.mu.Unlock()
	s.media.puts++
	switch {
	case s.media.redirectTo != "":
		return http.StatusTemporaryRedirect, s.media.redirectTo
	case s.media.failStatus != 0:
		return s.media.failStatus, ""
	case s.media.failUploads > 0:
		s.media.failUploads--
		return http.StatusServiceUnavailable, ""
	}
	sum := sha256.Sum256(data)
	if r.Header.Get("x-test-checksum-sha256") != sha || hex.EncodeToString(sum[:]) != sha || r.Header.Get("Authorization") != "" {
		return http.StatusForbidden, ""
	}
	s.media.stored[s.media.key(cnv, sha)] = data
	s.media.mime[s.media.key(cnv, sha)] = r.Header.Get("Content-Type")
	return http.StatusOK, ""
}

// RedirectMediaUploads makes every storage PUT answer 307 to location.
func (s *Server) RedirectMediaUploads(location string) {
	s.media.mu.Lock()
	defer s.media.mu.Unlock()
	s.media.redirectTo = location
}

// FailMediaUploadsWith makes every storage PUT answer status (a persistent
// refusal, e.g. 403), until reset with 0.
func (s *Server) FailMediaUploadsWith(status int) {
	s.media.mu.Lock()
	defer s.media.mu.Unlock()
	s.media.failStatus = status
}
