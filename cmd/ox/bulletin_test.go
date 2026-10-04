package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/prime"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Full-command runs of `ox bulletin post` against a fake server
// ============================================================================
//
// These drive runBulletinPost through the real API client to a throwaway
// HTTP server that records every request byte-for-byte and answers from a
// script. Each test asserts what a coworker (or their AI coworker, via
// --json) observes: exit status, the JSON code, what crossed the wire, and
// what did not.

// bulletinCapturedRequest is one request exactly as it reached the server.
type bulletinCapturedRequest struct {
	path   string
	header http.Header
	body   []byte
}

// slug decodes the slug field of the captured body.
func (r bulletinCapturedRequest) slug() string {
	var probe struct {
		Slug string `json:"slug"`
	}
	_ = json.Unmarshal(r.body, &probe)
	return probe.Slug
}

type bulletinServerLog struct {
	mu       sync.Mutex
	requests []bulletinCapturedRequest
}

func (l *bulletinServerLog) record(r bulletinCapturedRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, r)
}

func (l *bulletinServerLog) all() []bulletinCapturedRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]bulletinCapturedRequest(nil), l.requests...)
}

func (l *bulletinServerLog) count() int { return len(l.all()) }

// bulletinReply is one scripted server response. body may carry {{sha}},
// {{bytes}}, {{slug}}, {{title}} and {{format}}, filled from the request the
// server actually received, so a 201 receipt can echo the real content hash.
type bulletinReply struct {
	status      int
	body        string
	contentType string // default application/json
	headers     map[string]string
}

// bulletinCreatedBody is a receipt whose hash and size match whatever was
// sent — what a healthy server answers.
const bulletinCreatedBody = `{"team_id":"team_abc","board":"general","path":"bulletin/general/posts/{{slug}}-{{sha}}.md","slug":"{{slug}}","title":"{{title}}","format":"{{format}}","content_sha256":"{{sha}}","content_bytes":{{bytes}},"created_at":"2026-09-21T22:41:07Z","expires_at":"2026-10-05T22:41:07Z","commit_id":"9f1c2a7d3e5b6c8a9f1c2a7d3e5b6c8a9f1c2a7d"}`

type bulletinHarness struct {
	out         *bytes.Buffer
	errOut      *bytes.Buffer
	log         *bulletinServerLog
	teamDir     string
	projectRoot string
	srvURL      string
	waits       []time.Duration
}

// newBulletinCommandHarness wires runBulletinPost to a recording HTTP server,
// an isolated auth store, an initialized project whose local config registers
// a team context {team_abc, Acme, <temp dir>}, captured writers, and a sleeper
// that records waits instead of sleeping.
//
// replies are consulted once per request, in order; the last repeats.
func newBulletinCommandHarness(t *testing.T, replies []bulletinReply) *bulletinHarness {
	t.Helper()
	require.NotEmpty(t, replies, "harness needs at least one scripted reply")

	// Nothing may try to open a prompt: a blocked read here would hang the
	// whole package's test run.
	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })
	t.Setenv("CI", "true")

	h := &bulletinHarness{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, log: &bulletinServerLog{}}

	var calls int
	var callsMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.log.record(bulletinCapturedRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})

		callsMu.Lock()
		idx := calls
		calls++
		callsMu.Unlock()
		if idx >= len(replies) {
			idx = len(replies) - 1
		}
		reply := replies[idx]

		var req struct {
			Slug, Title, Format, Content string
		}
		_ = json.Unmarshal(body, &req)
		sum := sha256.Sum256([]byte(req.Content))
		out := strings.NewReplacer(
			"{{sha}}", hex.EncodeToString(sum[:]),
			"{{bytes}}", strconv.Itoa(len(req.Content)),
			"{{slug}}", req.Slug,
			"{{title}}", req.Title,
			"{{format}}", req.Format,
		).Replace(reply.body)

		ct := reply.contentType
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		for k, v := range reply.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(srv.Close)
	h.srvURL = srv.URL

	t.Setenv("SAGEOX_ENDPOINT", srv.URL)
	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	h.teamDir = t.TempDir()
	h.projectRoot = createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:   "bulletin_test_repo",
		Endpoint: srv.URL,
		TeamID:   "team_abc",
		TeamName: "Acme",
	})
	require.NoError(t, config.SaveLocalConfig(h.projectRoot, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{TeamID: "team_abc", TeamName: "Acme", Path: h.teamDir}},
	}))
	t.Setenv(config.EnvProjectRoot, h.projectRoot)

	require.NoError(t, auth.SaveTokenForEndpoint(srv.URL, &auth.StoredToken{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))

	// Record retry waits instead of sleeping: a 503 script would otherwise
	// cost real seconds per test.
	prevSleep := bulletinSleep
	bulletinSleep = func(_ context.Context, d time.Duration) error {
		h.waits = append(h.waits, d)
		return nil
	}
	t.Cleanup(func() { bulletinSleep = prevSleep })

	resetBulletinCmdState(t, h.out, h.errOut)
	return h
}

// resetBulletinCmdState scrubs the package-level cobra singletons so one
// test's flag values cannot leak into the next, and registers bulletinCmd on
// rootCmd for the test's duration: cmd.Root() must be the real root for the
// persistent --json flag to be found, exactly as it is after
// syncFeatureGatedCommands in production.
func resetBulletinCmdState(t *testing.T, out, errOut *bytes.Buffer) {
	t.Helper()
	wasRegistered := commandRegistered(rootCmd, bulletinCmd)
	setCommandRegistered(rootCmd, bulletinCmd, true)
	reset := func() {
		bulletinPostCmd.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
		_ = rootCmd.PersistentFlags().Set("json", "false")
		bulletinPostCmd.SetOut(nil)
		bulletinPostCmd.SetErr(nil)
		bulletinPostCmd.SetIn(nil)
	}
	reset()
	bulletinPostCmd.SetOut(out)
	bulletinPostCmd.SetErr(errOut)
	bulletinPostCmd.SetContext(context.Background())
	t.Cleanup(func() {
		reset()
		setCommandRegistered(rootCmd, bulletinCmd, wasRegistered)
	})
}

// writeBulletinFile puts content in a scratch directory under the given name
// and returns the path.
func writeBulletinFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

// runBulletin sets flags and runs the command. flags is name -> value;
// jsonOutput sets the root --json flag.
func runBulletin(t *testing.T, file string, jsonOutput bool, flags map[string]string) error {
	t.Helper()
	if _, ok := flags["ttl"]; !ok {
		require.NoError(t, bulletinPostCmd.Flags().Set("ttl", "14d"))
	}
	for k, v := range flags {
		require.NoError(t, bulletinPostCmd.Flags().Set(k, v))
	}
	if jsonOutput {
		require.NoError(t, rootCmd.PersistentFlags().Set("json", "true"))
	}
	return runBulletinPost(bulletinPostCmd, []string{file})
}

// decodeBulletinJSON parses the single JSON document the command wrote.
func decodeBulletinJSON(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &doc), "stdout must be one JSON document, got:\n%s", out.String())
	return doc
}

// requireSilentFailure asserts the exit-1-after-rendering contract.
func requireSilentFailure(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err, "a failure must exit non-zero")
	assert.True(t, cli.IsSilent(err), "the report is already rendered; the error must be silent so main does not print a second error line, got %v", err)
}

// hashTree hashes every file under root so a checkout can be compared before
// and after a run.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(data)
		rel, _ := filepath.Rel(root, path)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	}))
	return out
}

const bulletinPostMarker = "MARKER-POST-BODY-2b7e-MUST-NOT-APPEAR-IN-OUTPUT"

// ----------------------------------------------------------------------------
// (1) Bytes are stored exactly as written
// ----------------------------------------------------------------------------

// TestBulletinPost_BytesArriveExactlyAsWritten is the "bytes stored exactly as
// written" promise: the request carries the file's bytes untouched, exactly
// six keys, an unescaped "<", the default ox User-Agent, and the team id in
// the path.
//
// Failure prevented: a trim, newline fix, or BOM strip changing what the
// server hashes; a seventh key the server rejects; HTML-escaped content
// doubling a tag-dense post past the 2 MiB cap; a rewritten User-Agent
// misreporting the publishing client.
func TestBulletinPost_BytesArriveExactlyAsWritten(t *testing.T) {
	h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
	content := []byte("\ufeff# Release notes for 0.17\r\n\r\n<b>&</b>  \r\n   \n")
	file := writeBulletinFile(t, "Release Notes (0.17).md", content)

	err := runBulletin(t, file, true, nil)
	require.NoError(t, err, "stdout:\n%s\nstderr:\n%s", h.out.String(), h.errOut.String())

	reqs := h.log.all()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, "/api/v1/teams/team_abc/bulletin/posts", r.path)
	assert.Equal(t, "application/json", r.header.Get("Content-Type"))
	assert.Equal(t, "Bearer test-access-token", r.header.Get("Authorization"))
	assert.True(t, strings.HasPrefix(r.header.Get("User-Agent"), "ox/"), "User-Agent %q must keep the ox/ prefix", r.header.Get("User-Agent"))

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(r.body, &keys))
	assert.ElementsMatch(t, []string{"board", "slug", "title", "format", "content", "ttl"}, bulletinWireKeys(keys), "exactly six keys")

	var wire struct {
		Board, Slug, Title, Format, Content, TTL string
	}
	require.NoError(t, json.Unmarshal(r.body, &wire))
	assert.Equal(t, string(content), wire.Content, "content must be the file bytes as-is")
	assert.Equal(t, "general", wire.Board)
	assert.Equal(t, "release-notes-0-17", wire.Slug)
	assert.Equal(t, "Release notes for 0.17", wire.Title)
	assert.Equal(t, "markdown", wire.Format)
	assert.Equal(t, "14d", wire.TTL)
	assert.Contains(t, string(r.body), "<b>&</b>", "< and & must not be HTML-escaped on the wire")
}

func bulletinWireKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ----------------------------------------------------------------------------
// (2) A post is never committed from the laptop
// ----------------------------------------------------------------------------

// TestBulletinPost_NeverWritesToTheTeamContextCheckout proves the local
// checkout is byte-identical before and after a publish, including an
// existing post on the board.
//
// Failure prevented: the CLI "helpfully" writing the post into the local
// checkout, racing the daemon's sync and leaving a file whose name or
// metadata the server never produced.
func TestBulletinPost_NeverWritesToTheTeamContextCheckout(t *testing.T) {
	h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
	postsDir := filepath.Join(h.teamDir, "bulletin", "general", "posts")
	require.NoError(t, os.MkdirAll(postsDir, 0o755))
	existing := strings.Repeat("b", 64)
	require.NoError(t, os.WriteFile(filepath.Join(postsDir, "older-"+existing+".md"), []byte("# older\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(postsDir, "older-"+existing+".meta.json"), []byte(`{"slug":"older"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(h.teamDir, "docs.md"), []byte("docs\n"), 0o644))
	before := hashTree(t, h.teamDir)

	file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))
	require.NoError(t, runBulletin(t, file, true, nil))

	assert.Equal(t, before, hashTree(t, h.teamDir), "the Team Context checkout must be byte-identical after a publish")
	doc := decodeBulletinJSON(t, h.out)
	assert.Equal(t, postsDir, doc["local_path"], "the receipt points at where the post WILL appear after sync")
}

// ----------------------------------------------------------------------------
// (3) Bad input never leaves the machine
// ----------------------------------------------------------------------------

// TestBulletinPost_BadInputNeverLeavesTheMachine runs each local refusal end
// to end: zero requests, exit 1, JSON code validation_error naming the field.
//
// Failure prevented: an oversized or malformed post being uploaded (1 MiB on
// the wire) only to be refused, or a bad --ttl costing a round-trip.
func TestBulletinPost_BadInputNeverLeavesTheMachine(t *testing.T) {
	tests := []struct {
		name      string
		fileName  string
		content   []byte
		flags     map[string]string
		wantField string
	}{
		{name: "oversized", fileName: "big.md", content: []byte("# t\n" + strings.Repeat("a", bulletinMaxContentBytes-3)), wantField: "content"},
		{name: "NUL byte", fileName: "nul.md", content: []byte("# t\nbody\x00\n"), wantField: "content"},
		{name: "invalid UTF-8", fileName: "bad.md", content: []byte("# t\n\xff\n"), wantField: "content"},
		{name: "whitespace only", fileName: "blank.md", content: []byte("  \n\n"), wantField: "content"},
		{name: "bad ttl 2w", fileName: "n.md", content: []byte("# t\nbody\n"), flags: map[string]string{"ttl": "2w"}, wantField: "ttl"},
		{name: "ttl beyond 90d", fileName: "n.md", content: []byte("# t\nbody\n"), flags: map[string]string{"ttl": "120d"}, wantField: "ttl"},
		{name: "bad slug -x", fileName: "n.md", content: []byte("# t\nbody\n"), flags: map[string]string{"slug": "-x"}, wantField: "slug"},
		{name: "unknown extension", fileName: "n.txt", content: []byte("# t\nbody\n"), wantField: "format"},
		{name: "bad board", fileName: "n.md", content: []byte("# t\nbody\n"), flags: map[string]string{"board": "Ops"}, wantField: "board"},
		{name: "multi-line title", fileName: "n.md", content: []byte("# t\nbody\n"), flags: map[string]string{"title": "a\nb"}, wantField: "title"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
			file := writeBulletinFile(t, tt.fileName, tt.content)

			err := runBulletin(t, file, true, tt.flags)

			requireSilentFailure(t, err)
			assert.Equal(t, 0, h.log.count(), "nothing may leave the machine on a local refusal")
			doc := decodeBulletinJSON(t, h.out)
			assert.Equal(t, "validation_error", doc["status"])
			assert.Equal(t, "validation_error", doc["error"])
			details, _ := doc["details"].(map[string]any)
			assert.Contains(t, details, tt.wantField, "details must name the field: %v", doc)
			assert.Contains(t, doc["message"], tt.wantField)
			assert.Contains(t, doc["guidance"], "Nothing left this machine")
		})
	}
}

// TestBulletinPost_UnreadableFileIsALocalRefusal pins that a missing path or
// a directory is a validation_error naming the file, with zero requests.
//
// Failure prevented: a typo'd path being reported as a generic error whose
// guidance says "rerun the same command" — advice that can never work.
func TestBulletinPost_UnreadableFileIsALocalRefusal(t *testing.T) {
	tests := []struct {
		name   string
		source func(t *testing.T) string
	}{
		{name: "missing file", source: func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope.md") }},
		{name: "directory", source: func(t *testing.T) string { return t.TempDir() + ".md" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
			src := tt.source(t)
			if tt.name == "directory" {
				require.NoError(t, os.MkdirAll(src, 0o755))
			}

			err := runBulletin(t, src, true, nil)

			requireSilentFailure(t, err)
			assert.Equal(t, 0, h.log.count())
			doc := decodeBulletinJSON(t, h.out)
			assert.Equal(t, "validation_error", doc["status"])
			details, _ := doc["details"].(map[string]any)
			assert.Contains(t, details["content"], "cannot read")
			assert.NotContains(t, doc["guidance"], "Rerun the same command;")
		})
	}
}

// TestBulletinPost_NoFileArgumentIsAnError pins the bare-command error.
//
// Failure prevented: `ox bulletin post --ttl 14d` panicking on args[0] or
// silently reading stdin.
func TestBulletinPost_NoFileArgumentIsAnError(t *testing.T) {
	h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
	require.NoError(t, bulletinPostCmd.Flags().Set("ttl", "14d"))

	err := runBulletinPost(bulletinPostCmd, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ox bulletin post <file>")
	assert.Equal(t, 0, h.log.count())
}

// ----------------------------------------------------------------------------
// (4) Each server outcome maps to the right code and exit status
// ----------------------------------------------------------------------------

// TestBulletinPost_PublishedReceipt is the happy path end to end in --json.
//
// Failure prevented: a successful publish exiting 1, a receipt missing the
// path or local_path an AI coworker needs, or guidance drifting from the
// shared copy prime and the guide use.
func TestBulletinPost_PublishedReceipt(t *testing.T) {
	h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
	content := []byte("# Release notes\n\nbody\n")
	file := writeBulletinFile(t, "release-notes.md", content)

	err := runBulletin(t, file, true, nil)
	require.NoError(t, err, "stdout:\n%s", h.out.String())

	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	doc := decodeBulletinJSON(t, h.out)
	assert.Equal(t, "published", doc["status"])
	team, _ := doc["team"].(map[string]any)
	assert.Equal(t, "team_abc", team["team_id"])
	assert.Equal(t, "Acme", team["name"])
	assert.Equal(t, "general", doc["board"])
	assert.Equal(t, "bulletin/general/posts/release-notes-"+sha+".md", doc["path"])
	assert.Equal(t, "release-notes", doc["slug"])
	assert.Equal(t, "Release notes", doc["title"])
	assert.Equal(t, "markdown", doc["format"])
	assert.Equal(t, sha, doc["content_sha256"])
	assert.Equal(t, float64(len(content)), doc["content_bytes"])
	assert.Equal(t, "2026-09-21T22:41:07Z", doc["created_at"])
	assert.Equal(t, "2026-10-05T22:41:07Z", doc["expires_at"])
	assert.Equal(t, "9f1c2a7d3e5b6c8a9f1c2a7d3e5b6c8a9f1c2a7d", doc["commit_id"])
	assert.Equal(t, filepath.Join(h.teamDir, "bulletin", "general", "posts"), doc["local_path"])
	assert.True(t, strings.HasSuffix(doc["local_path"].(string), filepath.Join("bulletin", "general", "posts")))
	assert.Equal(t, prime.BulletinReceiptGuidance, doc["guidance"])
	assert.NotContains(t, h.out.String(), "body\n", "the receipt never echoes the post")
	assert.Empty(t, h.errOut.String(), "no retry noise on a clean publish")
}

// TestBulletinPost_ServerOutcomes maps every non-201 answer to its exit
// status and JSON code, end to end.
//
// Failure prevented: the three 404s collapsing into one message (telling an
// enrolled member "not in the pilot", or a non-member "server unsupported"),
// a duplicate losing the existing post's path, a 400 losing the field the
// server named, or any failure exiting 0.
func TestBulletinPost_ServerOutcomes(t *testing.T) {
	tests := []struct {
		name         string
		reply        bulletinReply
		wantCode     string
		wantGuidance string
		check        func(t *testing.T, doc map[string]any)
	}{
		{
			name:         "409 duplicate",
			reply:        bulletinReply{status: http.StatusConflict, body: `{"error":{"code":"duplicate_post","message":"identical content already posted","details":{"path":"bulletin/general/posts/release-notes-abc.md","state":"active","created_at":"2026-09-20T00:00:00Z","expires_at":"2026-10-04T00:00:00Z"}}}`},
			wantCode:     "duplicate_post",
			wantGuidance: "already published",
			check: func(t *testing.T, doc map[string]any) {
				dup, _ := doc["duplicate"].(map[string]any)
				assert.Equal(t, "bulletin/general/posts/release-notes-abc.md", dup["path"])
				assert.Equal(t, "active", dup["state"])
				assert.Equal(t, "2026-09-20T00:00:00Z", dup["created_at"])
				assert.Equal(t, "2026-10-04T00:00:00Z", dup["expires_at"])
				assert.Contains(t, doc["guidance"], "bulletin/general/posts/release-notes-abc.md")
			},
		},
		{
			name:         "400 validation with details",
			reply:        bulletinReply{status: http.StatusBadRequest, body: `{"error":{"code":"validation_error","message":"invalid post","details":{"ttl":"ttl \"14d\" is longer than the maximum 7d"}}}`},
			wantCode:     "validation_error",
			wantGuidance: "server rejected",
			check: func(t *testing.T, doc map[string]any) {
				details, _ := doc["details"].(map[string]any)
				assert.Equal(t, `ttl "14d" is longer than the maximum 7d`, details["ttl"])
				assert.Contains(t, doc["message"], "ttl")
			},
		},
		{
			name:         "400 validation without details",
			reply:        bulletinReply{status: http.StatusBadRequest, body: `{"error":{"code":"validation_error","message":"unknown field"}}`},
			wantCode:     "validation_error",
			wantGuidance: "server rejected",
			check: func(t *testing.T, doc map[string]any) {
				_, has := doc["details"]
				assert.False(t, has, "no details when the server sent none")
			},
		},
		{
			name:         "404 empty body: outside the pilot",
			reply:        bulletinReply{status: http.StatusNotFound, body: ""},
			wantCode:     "not_enabled",
			wantGuidance: "Do not retry",
		},
		{
			// Body captured verbatim from the live server on 2026-09-21 for a
			// team the caller cannot reach.
			name:         "404 nested JSON error: not a member",
			reply:        bulletinReply{status: http.StatusNotFound, body: "{\"error\":{\"code\":\"NOT_FOUND\",\"message\":\"team not found\"}}\n"},
			wantCode:     "not_a_member",
			wantGuidance: "team you belong to",
			check: func(t *testing.T, doc map[string]any) {
				assert.NotContains(t, strings.ToLower(doc["message"].(string)), "does not exist")
				assert.NotContains(t, strings.ToLower(doc["guidance"].(string)), "does not exist")
			},
		},
		{
			// Body captured verbatim from the live server on 2026-09-21 for an
			// unrouted path: the router answers with a FLAT JSON envelope and a
			// JSON content type, not plain text. This is what a deployment
			// without the bulletin route says, and it must not be read as a
			// membership problem.
			name:         "404 flat JSON from the router: no bulletin route",
			reply:        bulletinReply{status: http.StatusNotFound, body: "{\"error\":\"route not registered\"}\n"},
			wantCode:     "unsupported_server",
			wantGuidance: "Do not retry against this endpoint",
		},
		{
			name:         "404 plain text: no bulletin route",
			reply:        bulletinReply{status: http.StatusNotFound, body: "404 page not found\n", contentType: "text/plain"},
			wantCode:     "unsupported_server",
			wantGuidance: "Do not retry against this endpoint",
		},
		{
			name:         "401",
			reply:        bulletinReply{status: http.StatusUnauthorized, body: `{"error":{"code":"unauthorized","message":"not authenticated"}}`},
			wantCode:     "unauthenticated",
			wantGuidance: "ox login",
		},
		{
			name:         "413",
			reply:        bulletinReply{status: http.StatusRequestEntityTooLarge, body: `{"error":{"code":"content_too_large","message":"content exceeds 1 MiB"}}`},
			wantCode:     "content_too_large",
			wantGuidance: "too large",
		},
		{
			name:         "500",
			reply:        bulletinReply{status: http.StatusInternalServerError, body: `{"error":{"code":"internal_error","message":"boom"}}`},
			wantCode:     "error",
			wantGuidance: "identical retry is safe",
			check: func(t *testing.T, doc map[string]any) {
				assert.Contains(t, doc["message"], "boom")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBulletinCommandHarness(t, []bulletinReply{tt.reply})
			file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

			err := runBulletin(t, file, true, nil)

			requireSilentFailure(t, err)
			assert.Equal(t, 1, h.log.count(), "exactly one request; none of these outcomes is retried")
			doc := decodeBulletinJSON(t, h.out)
			assert.Equal(t, tt.wantCode, doc["status"])
			assert.Equal(t, tt.wantCode, doc["error"])
			assert.NotEmpty(t, doc["message"])
			assert.Contains(t, doc["guidance"], tt.wantGuidance)
			if tt.check != nil {
				tt.check(t, doc)
			}
		})
	}
}

// TestBulletinPost_UnavailableRetriesTheIdenticalRequest pins the 503 policy:
// up to three retries of the SAME request (same slug), waiting what the
// server asked (floored at 1s), never past the 30s budget, one progress line
// per retry on stderr in both modes, then team_context_unavailable.
//
// Failure prevented: a retry with a fresh slug defeating the server's
// duplicate check and double-posting; retries ignoring Retry-After and
// hammering a recovering store; an unbounded retry loop; or progress lines
// landing on stdout and corrupting the --json document.
func TestBulletinPost_UnavailableRetriesTheIdenticalRequest(t *testing.T) {
	unavailable := func(retryAfter string) bulletinReply {
		r := bulletinReply{status: http.StatusServiceUnavailable, body: `{"error":{"code":"team_context_unavailable","message":"team context store unavailable"}}`}
		if retryAfter != "" {
			r.headers = map[string]string{"Retry-After": retryAfter}
		}
		return r
	}

	t.Run("four 503s with Retry-After 5", func(t *testing.T) {
		h := newBulletinCommandHarness(t, []bulletinReply{unavailable("5")})
		file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

		err := runBulletin(t, file, true, nil)

		requireSilentFailure(t, err)
		reqs := h.log.all()
		require.Len(t, reqs, 4, "one attempt plus three retries")
		for _, r := range reqs {
			assert.Equal(t, "release-notes", r.slug(), "every retry must carry the same slug")
			assert.Equal(t, reqs[0].body, r.body, "every retry must be the identical request")
		}
		assert.Equal(t, []time.Duration{5 * time.Second, 5 * time.Second, 5 * time.Second}, h.waits)
		progress := strings.Count(h.errOut.String(), "retrying in 5s")
		assert.Equal(t, 3, progress, "one progress line per retry on stderr:\n%s", h.errOut.String())
		doc := decodeBulletinJSON(t, h.out)
		assert.Equal(t, "team_context_unavailable", doc["status"])
		assert.Contains(t, doc["guidance"], "4 attempts")
		assert.Contains(t, doc["guidance"], "identical retry is safe")
	})

	t.Run("Retry-After 20 stops before overrunning the 30s budget", func(t *testing.T) {
		h := newBulletinCommandHarness(t, []bulletinReply{unavailable("20")})
		file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

		err := runBulletin(t, file, true, nil)

		requireSilentFailure(t, err)
		assert.Equal(t, 2, h.log.count(), "a second 20s wait would pass 30s, so no third attempt")
		assert.Equal(t, []time.Duration{20 * time.Second}, h.waits)
		doc := decodeBulletinJSON(t, h.out)
		assert.Equal(t, "team_context_unavailable", doc["status"])
		assert.Contains(t, doc["guidance"], "2 attempts")
	})

	t.Run("no Retry-After waits the 1s floor", func(t *testing.T) {
		h := newBulletinCommandHarness(t, []bulletinReply{unavailable("")})
		file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

		err := runBulletin(t, file, true, nil)

		requireSilentFailure(t, err)
		assert.Equal(t, 4, h.log.count())
		assert.Equal(t, []time.Duration{time.Second, time.Second, time.Second}, h.waits)
	})

	t.Run("503 then 201 publishes after two requests", func(t *testing.T) {
		h := newBulletinCommandHarness(t, []bulletinReply{unavailable("5"), {status: http.StatusCreated, body: bulletinCreatedBody}})
		file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

		err := runBulletin(t, file, true, nil)

		require.NoError(t, err, "stdout:\n%s", h.out.String())
		assert.Equal(t, 2, h.log.count())
		assert.Equal(t, []time.Duration{5 * time.Second}, h.waits)
		assert.Equal(t, 1, strings.Count(h.errOut.String(), "retrying in 5s"))
		doc := decodeBulletinJSON(t, h.out)
		assert.Equal(t, "published", doc["status"])
	})

	t.Run("human mode also reports on stderr and exits 1", func(t *testing.T) {
		h := newBulletinCommandHarness(t, []bulletinReply{unavailable("5")})
		file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

		err := runBulletin(t, file, false, nil)

		requireSilentFailure(t, err)
		assert.Equal(t, 4, h.log.count())
		assert.Equal(t, 3, strings.Count(h.errOut.String(), "retrying in 5s"))
		out := stripANSI(h.out.String())
		assert.Contains(t, out, "✗")
		assert.Contains(t, out, "temporarily unavailable")
		assert.Contains(t, out, "safe to rerun")
	})
}

// ----------------------------------------------------------------------------
// (5) The receipt's hash is real
// ----------------------------------------------------------------------------

// TestBulletinPost_WrongHashFromTheServerIsAHardError proves a receipt whose
// hash or size does not match the bytes sent is refused in both modes.
//
// Failure prevented: the CLI printing "published" for a receipt that
// describes some other content — a person then trusts a path that holds
// something they never sent.
func TestBulletinPost_WrongHashFromTheServerIsAHardError(t *testing.T) {
	wrongHash := strings.ReplaceAll(bulletinCreatedBody, `"content_sha256":"{{sha}}"`, `"content_sha256":"`+strings.Repeat("0", 64)+`"`)
	wrongBytes := strings.ReplaceAll(bulletinCreatedBody, `"content_bytes":{{bytes}}`, `"content_bytes":1`)

	tests := []struct {
		name       string
		body       string
		jsonOutput bool
	}{
		{name: "wrong hash, json", body: wrongHash, jsonOutput: true},
		{name: "wrong hash, human", body: wrongHash, jsonOutput: false},
		{name: "wrong byte count, json", body: wrongBytes, jsonOutput: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: tt.body}})
			file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

			err := runBulletin(t, file, tt.jsonOutput, nil)

			requireSilentFailure(t, err)
			assert.Equal(t, 1, h.log.count())
			if tt.jsonOutput {
				doc := decodeBulletinJSON(t, h.out)
				assert.Equal(t, "hash_mismatch", doc["status"])
				assert.Equal(t, "hash_mismatch", doc["error"])
				assert.Contains(t, doc["guidance"], "Do not trust this receipt")
				assert.Contains(t, doc["guidance"], "duplicate_post")
				return
			}
			out := stripANSI(h.out.String())
			assert.Contains(t, out, "✗")
			assert.Contains(t, out, "does not match")
			assert.Contains(t, out, "Do not trust this receipt")
			assert.NotContains(t, out, "Published")
		})
	}
}

// ----------------------------------------------------------------------------
// (6) Confirmation, identity, and team selection
// ----------------------------------------------------------------------------

// TestBulletinPost_YesAndJSONNeverPrompt proves --yes and --json each publish
// without a prompt in a non-interactive run.
//
// Failure prevented: an AI coworker's --json run blocking on a confirmation
// nobody can answer, or --yes still asking.
func TestBulletinPost_YesAndJSONNeverPrompt(t *testing.T) {
	tests := []struct {
		name       string
		flags      map[string]string
		jsonOutput bool
	}{
		{name: "--yes", flags: map[string]string{"yes": "true"}},
		{name: "--json", jsonOutput: true},
		{name: "--yes --json", flags: map[string]string{"yes": "true"}, jsonOutput: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
			file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

			err := runBulletin(t, file, tt.jsonOutput, tt.flags)

			require.NoError(t, err, "stdout:\n%s", h.out.String())
			assert.Equal(t, 1, h.log.count())
			assert.NotContains(t, h.out.String(), "Publish?")
		})
	}
}

// TestBulletinPost_TeamServiceTokenIsRefusedBeforeAnyRequest proves a team
// token (oxt_ prefix) never reaches the server.
//
// Failure prevented: a CI job running with the team token getting an opaque
// 401 after uploading the post, instead of being told publishing is a
// person's act.
func TestBulletinPost_TeamServiceTokenIsRefusedBeforeAnyRequest(t *testing.T) {
	for _, jsonOutput := range []bool{true, false} {
		t.Run(map[bool]string{true: "json", false: "human"}[jsonOutput], func(t *testing.T) {
			h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
			require.NoError(t, auth.SaveTokenForEndpoint(h.srvURL, &auth.StoredToken{
				AccessToken: auth.TeamTokenPrefix + "teamtoken_0000",
				TokenType:   "Bearer",
				ExpiresAt:   time.Now().Add(time.Hour),
			}))
			file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

			err := runBulletin(t, file, jsonOutput, nil)

			requireSilentFailure(t, err)
			assert.Equal(t, 0, h.log.count(), "a team token must be refused before any network call")
			if jsonOutput {
				doc := decodeBulletinJSON(t, h.out)
				assert.Equal(t, "team_token_cannot_publish", doc["status"])
				assert.Contains(t, doc["guidance"], "person's act")
				return
			}
			out := stripANSI(h.out.String())
			assert.Contains(t, out, "team service token cannot publish")
			assert.Contains(t, out, "ox login")
		})
	}
}

// TestBulletinPost_NoTokenIsUnauthenticated proves a missing login is a
// clean refusal before any request.
//
// Failure prevented: an empty Bearer header being sent and the person
// reading a server 401 instead of "run ox login".
func TestBulletinPost_NoTokenIsUnauthenticated(t *testing.T) {
	h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // a fresh, empty auth store
	file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

	err := runBulletin(t, file, true, nil)

	requireSilentFailure(t, err)
	assert.Equal(t, 0, h.log.count())
	doc := decodeBulletinJSON(t, h.out)
	assert.Equal(t, "unauthenticated", doc["status"])
	assert.Contains(t, doc["guidance"], "ox login")
}

// TestBulletinPost_NoTeamIsRefusedBeforeAnyRequest proves a repo without a
// team and no --team is refused locally.
//
// Failure prevented: a request to /teams//bulletin/posts, which 404s and
// would be misreported as "server does not support bulletin posts".
func TestBulletinPost_NoTeamIsRefusedBeforeAnyRequest(t *testing.T) {
	h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
	require.NoError(t, config.SaveProjectConfig(h.projectRoot, &config.ProjectConfig{RepoID: "bulletin_test_repo", Endpoint: h.srvURL}))
	require.NoError(t, config.SaveLocalConfig(h.projectRoot, &config.LocalConfig{}))
	file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

	err := runBulletin(t, file, true, nil)

	requireSilentFailure(t, err)
	assert.Equal(t, 0, h.log.count())
	doc := decodeBulletinJSON(t, h.out)
	assert.Equal(t, "no_team", doc["status"])
	assert.Contains(t, doc["guidance"], "--team")
}

// TestBulletinPost_TeamFlagReachesThePath proves --team is passed through to
// the URL (the server resolves slugs) and the receipt's local_path degrades
// to the relative board directory when no local checkout is known for that
// team, or resolves to the checkout when one is.
//
// Failure prevented: --team being ignored in favor of the repo's team, so a
// post lands on the wrong board.
func TestBulletinPost_TeamFlagReachesThePath(t *testing.T) {
	t.Run("unknown slug passes through for the server to resolve", func(t *testing.T) {
		h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
		file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

		err := runBulletin(t, file, true, map[string]string{"team": "globex"})

		require.NoError(t, err, "stdout:\n%s", h.out.String())
		reqs := h.log.all()
		require.Len(t, reqs, 1)
		assert.Equal(t, "/api/v1/teams/globex/bulletin/posts", reqs[0].path)
		doc := decodeBulletinJSON(t, h.out)
		assert.Equal(t, filepath.FromSlash("bulletin/general/posts"), doc["local_path"], "no local checkout is known for globex")
	})

	t.Run("locally known team name resolves to its id", func(t *testing.T) {
		h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
		file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

		err := runBulletin(t, file, true, map[string]string{"team": "acme"})

		require.NoError(t, err, "stdout:\n%s", h.out.String())
		reqs := h.log.all()
		require.Len(t, reqs, 1)
		assert.Equal(t, "/api/v1/teams/team_abc/bulletin/posts", reqs[0].path, "the local team named Acme resolves to its id")
	})

	t.Run("locally known team id resolves its checkout", func(t *testing.T) {
		h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
		file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

		err := runBulletin(t, file, true, map[string]string{"team": "team_abc"})

		require.NoError(t, err, "stdout:\n%s", h.out.String())
		reqs := h.log.all()
		require.Len(t, reqs, 1)
		assert.Equal(t, "/api/v1/teams/team_abc/bulletin/posts", reqs[0].path)
		doc := decodeBulletinJSON(t, h.out)
		assert.Equal(t, filepath.Join(h.teamDir, "bulletin", "general", "posts"), doc["local_path"])
	})
}

// ----------------------------------------------------------------------------
// (7) Human receipt
// ----------------------------------------------------------------------------

// TestBulletinPost_HumanReceipt pins what a person sees after a publish: the
// stored path, where it will appear locally, the expiry, and the shared trust
// note — and never the post itself.
//
// Failure prevented: a receipt that omits where the post went, or that
// echoes the content (which may be large, and is not the receipt's to
// repeat); the trust note drifting from prime and the guide.
func TestBulletinPost_HumanReceipt(t *testing.T) {
	h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
	content := []byte("# Release notes\n\n" + bulletinPostMarker + "\n")
	file := writeBulletinFile(t, "release-notes.md", content)

	err := runBulletin(t, file, false, nil)
	require.NoError(t, err, "stdout:\n%s", h.out.String())

	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	out := stripANSI(h.out.String())
	assert.Contains(t, out, "✓ Published release-notes to general on team Acme")
	assert.Contains(t, out, "bulletin/general/posts/release-notes-"+sha+".md")
	assert.Contains(t, out, sha[:8]+"…"+sha[len(sha)-4:])
	assert.Contains(t, out, strconv.Itoa(len(content))+" bytes · markdown")
	assert.Regexp(t, `expires\s+2026-10-05T22:41:07Z \(14d\)`, out)
	assert.Regexp(t, `commit\s+9f1c2a7\n`, out, "commit is shortened to 7 characters")
	assert.Contains(t, out, "Appears in "+filepath.Join(h.teamDir, "bulletin", "general", "posts")+" after the next Team Context sync.")
	assert.Contains(t, out, prime.BulletinTrustNote)
	assert.NotContains(t, out, bulletinPostMarker, "the receipt must never echo the post")
	assert.NotContains(t, out, "{")
	assert.NotContains(t, out, "agent", "user-facing copy says AI coworker, never agent")
}

// TestBulletinPost_HumanFailureShowsHints pins the human rendering of a
// server refusal: the ✗ line, a dim explanation, and an actionable hint.
//
// Failure prevented: a bare error string with no next step, or a JSON
// envelope leaking into a human terminal.
func TestBulletinPost_HumanFailureShowsHints(t *testing.T) {
	h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusNotFound, body: `{"error":{"code":"not_found","message":"team not found"}}`}})
	file := writeBulletinFile(t, "release-notes.md", []byte("# Release notes\n\nbody\n"))

	err := runBulletin(t, file, false, nil)

	requireSilentFailure(t, err)
	out := stripANSI(h.out.String())
	assert.Contains(t, out, "✗ You can't post to team_abc.")
	assert.Contains(t, out, "Choose a team you belong to.")
	assert.Contains(t, out, "ox teams")
	assert.NotContains(t, out, `"status"`)
}

// ----------------------------------------------------------------------------
// (8) stdin
// ----------------------------------------------------------------------------

// TestBulletinPost_Stdin proves "-" publishes the piped bytes when --format,
// --title and --slug are given, and refuses locally naming the missing flag
// when they are not.
//
// Failure prevented: piped input silently deriving a slug or title from "-",
// or a missing --format being discovered only by a server 400.
func TestBulletinPost_Stdin(t *testing.T) {
	piped := []byte("piped body <i>&</i>\r\n")

	t.Run("with format, title, and slug", func(t *testing.T) {
		h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
		bulletinPostCmd.SetIn(bytes.NewReader(piped))

		err := runBulletin(t, "-", true, map[string]string{"format": "html", "title": "Piped", "slug": "piped"})

		require.NoError(t, err, "stdout:\n%s", h.out.String())
		reqs := h.log.all()
		require.Len(t, reqs, 1)
		var wire struct {
			Slug, Title, Format, Content string
		}
		require.NoError(t, json.Unmarshal(reqs[0].body, &wire))
		assert.Equal(t, string(piped), wire.Content)
		assert.Equal(t, "piped", wire.Slug)
		assert.Equal(t, "Piped", wire.Title)
		assert.Equal(t, "html", wire.Format)
		doc := decodeBulletinJSON(t, h.out)
		assert.Equal(t, "published", doc["status"])
	})

	t.Run("without the flags refuses locally", func(t *testing.T) {
		tests := []struct {
			name      string
			flags     map[string]string
			wantField string
		}{
			{name: "no format", flags: map[string]string{}, wantField: "format"},
			{name: "no title", flags: map[string]string{"format": "markdown"}, wantField: "title"},
			{name: "no slug", flags: map[string]string{"format": "markdown", "title": "T"}, wantField: "slug"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h := newBulletinCommandHarness(t, []bulletinReply{{status: http.StatusCreated, body: bulletinCreatedBody}})
				bulletinPostCmd.SetIn(bytes.NewReader(piped))

				err := runBulletin(t, "-", true, tt.flags)

				requireSilentFailure(t, err)
				assert.Equal(t, 0, h.log.count())
				doc := decodeBulletinJSON(t, h.out)
				assert.Equal(t, "validation_error", doc["status"])
				details, _ := doc["details"].(map[string]any)
				assert.Contains(t, details, tt.wantField)
				assert.Contains(t, doc["message"], "--"+tt.wantField)
			})
		}
	})
}
