package mirrortest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/githubmirror"
)

const (
	// DefaultToken is the bearer token New accepts unless WithToken says otherwise.
	DefaultToken = "test-token"

	// maxRequestBytes bounds the decoded body; a real batch of 50 items is far smaller.
	maxRequestBytes = 32 << 20

	// flaggedReason is the reason a rejected item carries when the scan refuses
	// its title or description.
	flaggedReason = "flagged"
)

// FlagMarker is the text the default scan flags. It is a stand-in for the real
// injection scan, which is far more elaborate and lives server-side.
const FlagMarker = "SYSTEM:"

type routeMode int

const (
	routeNormal     routeMode = iota
	routeUnrouted             // 404, flat error envelope: this server has no mirror route
	routeNotEnabled           // 404, empty body: the mirror is off for this account
)

type config struct {
	token        string
	now          func() time.Time
	scan         func(string) bool
	privateOptIn bool
	repoStatus   string // when set, answers for every repo
	mode         routeMode
}

// Option configures New.
type Option func(*config)

// WithToken sets the bearer token the server accepts (default DefaultToken).
func WithToken(token string) Option { return func(c *config) { c.token = token } }

// WithNow sets the clock behind each post's created_at.
func WithNow(now func() time.Time) Option { return func(c *config) { c.now = now } }

// WithScan replaces the fake injection scan. It reports whether the text is
// flagged. The default flags text containing FlagMarker and nothing else.
func WithScan(scan func(string) bool) Option { return func(c *config) { c.scan = scan } }

// PrivateOptIn makes the team opt in to mirroring private repos. Without it a
// private repo answers not_opted_in and every item is rejected.
func PrivateOptIn() Option { return func(c *config) { c.privateOptIn = true } }

// WithRepoStatus answers the given repo status (RepoNotLinked, RepoNotEligible,
// ...) for every repo, overriding the private-repo policy.
func WithRepoStatus(status string) Option { return func(c *config) { c.repoStatus = status } }

// Unrouted makes the server answer like one without the mirror route: 404 with
// the router's flat error envelope.
func Unrouted() Option { return func(c *config) { c.mode = routeUnrouted } }

// NotEnabled makes the server answer like one where the mirror is off for this
// account: 404 with an empty body.
func NotEnabled() Option { return func(c *config) { c.mode = routeNotEnabled } }

type injectedFailure struct {
	status     int
	body       string
	retryAfter string
}

// Server is an in-process mirror relay endpoint. It answers only
// POST /api/v1/teams/{team}/github-mirror/items and writes the posts it
// accepts under githubmirror.PostsDir(teamContextDir). It holds no state of its
// own beyond those files, so a second Server on the same directory picks up
// where the first left off. A repeat of a live post's change hash answers
// current; if its last_material_change_at is later, the post's expires_at is
// extended in the sidecar first (the post bytes never change). Safe for
// concurrent use.
type Server struct {
	srv *httptest.Server
	dir string
	cfg config

	mu     sync.Mutex
	bodies [][]byte // raw bodies of requests that decoded, in arrival order
	teams  []string // the {team} path value of each, parallel to bodies
	fail   *injectedFailure
}

// New starts a server writing posts into teamContextDir and closes it when the
// test ends.
func New(t testing.TB, teamContextDir string, opts ...Option) *Server {
	t.Helper()

	cfg := config{
		token: DefaultToken,
		now:   time.Now,
		scan:  func(text string) bool { return strings.Contains(text, FlagMarker) },
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	s := &Server{dir: teamContextDir, cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/teams/{team}/github-mirror/items", s.handleItems)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the server's base URL, without a path.
func (s *Server) URL() string { return s.srv.URL }

// FailNext makes the next request fail with the given status and body instead
// of being processed; later requests behave normally. A non-empty retryAfter is
// sent as the Retry-After header.
func (s *Server) FailNext(status int, body string, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = &injectedFailure{status: status, body: body, retryAfter: retryAfter}
}

// Requests returns every request that decoded, oldest first, including ones
// answered 400 or 413. The caller owns the returned values.
func (s *Server) Requests() []githubmirror.RelayRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]githubmirror.RelayRequest, 0, len(s.bodies))
	for _, body := range s.bodies {
		// decoded once already, so decoding again cannot fail; doing it here
		// is what makes each returned request an independent copy
		var req githubmirror.RelayRequest
		if err := json.Unmarshal(body, &req); err != nil {
			panic(fmt.Sprintf("mirrortest: re-decode recorded request: %v", err))
		}
		out = append(out, req)
	}
	return out
}

// Teams returns the {team} path value of each request in Requests, in order.
func (s *Server) Teams() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.teams)
}

// Posts lists the current post files (absolute paths, sorted).
func (s *Server) Posts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	matches, err := filepath.Glob(filepath.Join(githubmirror.PostsDir(s.dir), "*.md"))
	if err != nil {
		return nil // the pattern is constant; Glob only fails on a bad pattern
	}
	slices.Sort(matches)
	return matches
}

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.fail != nil {
		fail := *s.fail
		s.fail = nil
		writeRaw(w, fail.status, fail.body, fail.retryAfter)
		return
	}
	if s.cfg.mode == routeUnrouted {
		writeRaw(w, http.StatusNotFound, `{"error":"route not registered"}`, "")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.cfg.token {
		writeError(w, http.StatusUnauthorized, "unauthorized", "sign in required", nil)
		return
	}
	if s.cfg.mode == routeNotEnabled {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error(), nil)
		return
	}
	req, err := decodeRequest(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	s.bodies = append(s.bodies, raw)
	s.teams = append(s.teams, r.PathValue("team"))

	if len(req.Items) > githubmirror.MaxBatchItems {
		writeError(w, http.StatusRequestEntityTooLarge, "batch_too_large",
			fmt.Sprintf("at most %d items per request", githubmirror.MaxBatchItems), nil)
		return
	}
	if details := validate(req); len(details) > 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "invalid relay request", details)
		return
	}

	resp, err := s.process(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error(), nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// a failed write means the client went away; there is nobody left to tell
	_ = json.NewEncoder(w).Encode(resp)
}

// decodeRequest refuses unknown fields: a client that sends something the
// contract does not define should fail in a test, not in production.
func decodeRequest(raw []byte) (githubmirror.RelayRequest, error) {
	var req githubmirror.RelayRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("decode request: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return req, errors.New("decode request: unexpected data after the JSON document")
	}
	return req, nil
}

// validate returns per-field messages keyed by field path, empty when valid.
func validate(req githubmirror.RelayRequest) map[string]string {
	details := map[string]string{}
	if req.Repo.Owner == "" {
		details["repo.owner"] = "required"
	}
	if req.Repo.Name == "" {
		details["repo.name"] = "required"
	}
	for i, it := range req.Items {
		field := func(name string) string { return fmt.Sprintf("items[%d].%s", i, name) }
		if it.Kind != githubmirror.KindPullRequest && it.Kind != githubmirror.KindIssue {
			details[field("kind")] = "must be pull_request or issue"
		}
		if it.Number <= 0 {
			details[field("number")] = "must be positive"
		}
		if digest, ok := strings.CutPrefix(it.ChangeHash, "sha256:"); !ok || digest == "" {
			details[field("change_hash")] = "must be sha256:<hex>"
		}
	}
	return details
}

func (s *Server) repoStatus(repo githubmirror.Repo) string {
	switch {
	case s.cfg.repoStatus != "":
		return s.cfg.repoStatus
	case repo.Private && !s.cfg.privateOptIn:
		return githubmirror.RepoNotOptedIn
	default:
		return githubmirror.RepoEnabled
	}
}

func (s *Server) process(req githubmirror.RelayRequest) (*githubmirror.RelayResponse, error) {
	status := s.repoStatus(req.Repo)
	resp := &githubmirror.RelayResponse{
		RepoStatus: status,
		Results:    make([]githubmirror.ItemResult, 0, len(req.Items)),
	}

	for _, it := range req.Items {
		key := githubmirror.SourceKey(req.Repo.Owner, req.Repo.Name, it.Kind, it.Number)
		result := githubmirror.ItemResult{SourceKey: key}

		if status != githubmirror.RepoEnabled {
			result.Status = githubmirror.ResultRejected
			result.Reason = status
			resp.Results = append(resp.Results, result)
			continue
		}

		outcome, reason, err := s.publish(req.Repo, it, key)
		if err != nil {
			return nil, fmt.Errorf("publish %s: %w", key, err)
		}
		result.Status, result.Reason = outcome, reason
		resp.Results = append(resp.Results, result)
	}
	return resp, nil
}

// postMetaFile is the .meta.json the server writes: the fields the client
// reads (githubmirror.PostMeta) plus the ones only the server needs.
type postMetaFile struct {
	githubmirror.PostMeta
	ChangeHash string `json:"change_hash"`
	Supersedes string `json:"supersedes,omitempty"`
}

// storedPost is a post already on disk for a source key.
type storedPost struct {
	mdPath   string
	metaPath string
	meta     postMetaFile
}

// publish stores one item, or says why it did not.
func (s *Server) publish(repo githubmirror.Repo, it githubmirror.Item, key string) (status, reason string, err error) {
	previous, err := s.storedFor(key)
	if err != nil {
		return "", "", err
	}
	for _, p := range previous {
		if p.meta.ChangeHash == it.ChangeHash {
			if _, statErr := os.Stat(p.mdPath); statErr == nil {
				// The hash covers who approved, not when, so a re-approval or a
				// close and reopen can move the expiry clock without it. The
				// post bytes stay; only its life is extended, never shortened.
				if err := extendExpiry(p, it); err != nil {
					return "", "", err
				}
				return githubmirror.ResultCurrent, "", nil
			}
		}
	}

	// the scan runs on every segment before anything is written, and a refused
	// title or description stops the item entirely
	if s.cfg.scan(it.Title) || s.cfg.scan(it.Body) {
		return githubmirror.ResultRejected, flaggedReason, nil
	}
	withheld := map[int64]bool{}
	for _, c := range it.Comments {
		if s.cfg.scan(c.Body) {
			withheld[c.ID] = true
		}
	}

	content := RenderPost(repo, it, withheld)
	sum := sha256.Sum256(content)
	slug := githubmirror.Slug(repo.Owner, repo.Name, it.Kind, it.Number)
	name := slug + "-" + hex.EncodeToString(sum[:])[:8]

	postsDir := githubmirror.PostsDir(s.dir)
	if err := os.MkdirAll(postsDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create posts dir: %w", err)
	}
	mdPath := filepath.Join(postsDir, name+".md")
	metaPath := filepath.Join(postsDir, name+".meta.json")

	meta := postMetaFile{
		PostMeta: githubmirror.PostMeta{
			Board:     githubmirror.Board,
			Slug:      slug,
			Path:      path.Join("bulletin", githubmirror.Board, "posts", name+".md"),
			CreatedAt: s.cfg.now().UTC(),
			ExpiresAt: it.LastMaterialChangeAt.UTC().Add(githubmirror.Window),
			SourceKey: key,
		},
		ChangeHash: it.ChangeHash,
	}
	for _, p := range previous {
		if p.mdPath != mdPath {
			meta.Supersedes = p.meta.Path
			break
		}
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode post meta: %w", err)
	}

	if err := os.WriteFile(mdPath, content, 0o644); err != nil {
		return "", "", fmt.Errorf("write post: %w", err)
	}
	if err := os.WriteFile(metaPath, append(metaJSON, '\n'), 0o644); err != nil {
		return "", "", fmt.Errorf("write post meta: %w", err)
	}

	// the new files are in place before the old ones go, so a reader never
	// sees the item missing
	for _, p := range previous {
		if p.mdPath == mdPath {
			continue
		}
		for _, stale := range []string{p.mdPath, p.metaPath} {
			if rmErr := os.Remove(stale); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				return "", "", fmt.Errorf("remove superseded post: %w", rmErr)
			}
		}
	}
	return githubmirror.ResultAccepted, "", nil
}

// extendExpiry moves a live post's expires_at to the item's last material
// change + the window when that is later. Only the sidecar changes.
func extendExpiry(p storedPost, it githubmirror.Item) error {
	expiresAt := it.LastMaterialChangeAt.UTC().Add(githubmirror.Window)
	if !expiresAt.After(p.meta.ExpiresAt) {
		return nil
	}
	meta := p.meta
	meta.ExpiresAt = expiresAt
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("encode post meta: %w", err)
	}
	if err := fileutil.AtomicWriteBytes(p.metaPath, append(metaJSON, '\n'), 0o644); err != nil {
		return fmt.Errorf("extend post expiry: %w", err)
	}
	return nil
}

// storedFor finds the posts on disk for a source key by reading their meta
// files, so the server needs no memory of its own.
func (s *Server) storedFor(key string) ([]storedPost, error) {
	metaPaths, err := filepath.Glob(filepath.Join(githubmirror.PostsDir(s.dir), "*.meta.json"))
	if err != nil {
		return nil, fmt.Errorf("list post metas: %w", err)
	}
	slices.Sort(metaPaths)

	var found []storedPost
	for _, metaPath := range metaPaths {
		data, err := os.ReadFile(metaPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", metaPath, err)
		}
		var meta postMetaFile
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("decode %s: %w", metaPath, err)
		}
		if meta.SourceKey != key {
			continue
		}
		found = append(found, storedPost{
			mdPath:   strings.TrimSuffix(metaPath, ".meta.json") + ".md",
			metaPath: metaPath,
			meta:     meta,
		})
	}
	return found, nil
}

// writeRaw answers with a fixed status and body, as an injected failure does.
func writeRaw(w http.ResponseWriter, status int, body, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	if body != "" {
		if json.Valid([]byte(body)) {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// writeError answers with the nested error envelope the real API uses.
func writeError(w http.ResponseWriter, status int, code, message string, details map[string]string) {
	envelope := struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Details map[string]string `json:"details,omitempty"`
		} `json:"error"`
	}{}
	envelope.Error.Code, envelope.Error.Message, envelope.Error.Details = code, message, details

	body, err := json.Marshal(envelope)
	if err != nil {
		body = []byte(`{"error":{"code":"internal","message":"encode error"}}`)
	}
	writeRaw(w, status, string(body), "")
}
