package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// End-to-end proofs for `ox session import`, one per Rule in
// tests/acceptance/features/session-recording/import-past-sessions.feature.
//
// Real: native Claude Code and Codex transcripts on disk, discovery and
// classification, the raw writer's redaction, summary validation, staging, the
// scoped commit, and pushLedger to a real bare remote. Fake: the vendor CLI
// that summarizes, the adapter binaries that convert a transcript, and the LFS
// store, which keeps every uploaded object so a test can read what left the
// machine.

const (
	e2eRepoID   = "repo_draft_test" // the project newDraftLedgerFixture writes
	e2eTeam     = "Acme Engineering"
	e2eClaudeA  = "5b1d7e3a-2c4f-4e8a-9b6d-0f1e2d3c4b5a"
	e2eClaudeB  = "8c2e4f6a-1b3d-4c5e-8f7a-9b0c1d2e3f4a"
	e2eClaudeC  = "a7b8c9d0-e1f2-4a3b-9c4d-5e6f7a8b9c0d"
	e2eCodexA   = "01a0ee54-4491-70e1-b771-3c5d2e8f9a10"
	e2eCodexB   = "01a0ee60-1234-7abc-8def-0123456789ab"
	e2eCodexA2  = "01a0ee54-4491-7aaa-8bbb-000000000001" // started in the same minute as e2eCodexA
	loginPrompt = "The login page loops back to itself after the OAuth callback on Safari only, find out why and fix it"
	pushPrompt  = "Every Ledger push from my laptop fails with a missing object error since yesterday, explain what broke"
	tokenPrompt = "Check which GitHub account the CLI is using on this machine before I rotate the deploy credentials"
)

// fakeLFSStore is an LFS server that keeps every uploaded object.
type fakeLFSStore struct {
	mu        sync.Mutex
	objects   map[string][]byte // bare sha256 hex -> bytes
	url       string
	failBatch bool // answer every batch request with a server error
}

func newFakeLFSStore(t *testing.T) *fakeLFSStore {
	t.Helper()
	s := &fakeLFSStore{objects: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/info/lfs/objects/batch"):
			s.mu.Lock()
			fail := s.failBatch
			s.mu.Unlock()
			if fail {
				http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
				return
			}
			var request struct {
				Objects []lfs.BatchObject `json:"objects"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			response := lfs.BatchResponse{Transfer: "basic"}
			for _, obj := range request.Objects {
				response.Objects = append(response.Objects, lfs.BatchResponseObject{
					OID: obj.OID, Size: obj.Size,
					Actions: &lfs.Actions{
						Upload: &lfs.Action{Href: s.url + "/upload/" + obj.OID},
						Verify: &lfs.Action{Href: s.url + "/verify"},
					},
				})
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(w).Encode(response)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/upload/"):
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.objects[strings.TrimPrefix(r.URL.Path, "/upload/")] = body
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/verify":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	s.url = server.URL
	return s
}

func (s *fakeLFSStore) client() *lfs.Client {
	return lfs.NewClient(s.url+"/ledger.git", "oauth2", "test-token")
}

func (s *fakeLFSStore) object(oid string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[strings.TrimPrefix(oid, "sha256:")]
}

// holding counts uploaded objects whose bytes contain text.
func (s *fakeLFSStore) holding(text string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, data := range s.objects {
		if bytes.Contains(data, []byte(text)) {
			n++
		}
	}
	return n
}

func (s *fakeLFSStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

// fakeSummarizer stands in for the claude or codex CLI.
type fakeSummarizer struct {
	unavailable bool   // the CLI is not installed
	exitCode    int    // a non-zero exit fails every call
	exitOutput  string // what the CLI printed when it failed

	mu       sync.Mutex
	prompts  []string
	isolated []bool
	before   func(prompt string)        // runs first on every call
	fail     func(prompt string) error  // a non-nil error fails that call
	reply    func(prompt string) string // a non-empty reply replaces the default summary
}

func (s *fakeSummarizer) Available() bool { return !s.unavailable }

func (s *fakeSummarizer) Run(ctx context.Context, req agentwork.RunRequest) (*agentwork.RunResult, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, req.Prompt)
	s.isolated = append(s.isolated, req.Isolated)
	before, fail, reply := s.before, s.fail, s.reply
	s.mu.Unlock()
	if before != nil {
		before(req.Prompt)
	}
	if err := ctx.Err(); err != nil {
		return nil, err // the real runner's CLI is killed with its context
	}
	if fail != nil {
		if err := fail(req.Prompt); err != nil {
			return nil, err
		}
	}
	if s.exitCode != 0 {
		return &agentwork.RunResult{ExitCode: s.exitCode, Output: s.exitOutput}, nil
	}
	if reply != nil {
		if out := reply(req.Prompt); out != "" {
			return &agentwork.RunResult{Output: out}, nil
		}
	}
	return &agentwork.RunResult{Output: e2eSummaryFor(req.Prompt)}, nil
}

func (s *fakeSummarizer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

// e2eTitleFor is the title the fake summarizer writes for each conversation.
func e2eTitleFor(prompt string) string {
	switch {
	case strings.Contains(prompt, loginPrompt):
		return "Fix the Safari OAuth login redirect loop"
	case strings.Contains(prompt, pushPrompt):
		return "Explain the missing object error on Ledger push"
	default:
		return "Check the GitHub account before rotating deploy credentials"
	}
}

func e2eSummaryFor(prompt string) string {
	out, _ := json.Marshal(map[string]any{
		"title":       e2eTitleFor(prompt),
		"summary":     e2eTitleFor(prompt) + ": traced the problem to its cause, made the fix, and confirmed it with a test run.",
		"key_actions": []string{"Reproduced the problem", "Found the cause", "Verified the fix"},
		"outcome":     "success",
	})
	return string(out)
}

// importFixture is a coworker's machine: native Claude Code and Codex stores,
// a project whose Ledger is a real clone of a real bare remote, a fake LFS
// store and a fake summarizer.
type importFixture struct {
	*draftLedgerFixture
	claudeProjects string
	codexHome      string
	visibility     string
	summarizer     *fakeSummarizer
	store          *fakeLFSStore
	push           func(ctx context.Context, ledgerPath string) error // nil: the real pushLedger
	pushBatch      int
	readErr        error // every adapter read fails with it
	unusable       bool  // no summarizer CLI is installed and logged in
	interactive    bool  // a coworker at a terminal can answer
	confirm        func(prompt string) (bool, error)
	ctx            context.Context // the run's context; nil means context.Background()
	progress       bytes.Buffer    // what a JSON run reports while it works

	mu       sync.Mutex
	native   map[string][]adapters.RawEntry // adapter output by native file
	reads    []string
	notified []string
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	base := newDraftLedgerFixture(t)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("SAGEOX_DAEMON", "false")
	claudeDir, codexHome := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexHome)
	priorDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	t.Cleanup(func() { gitserver.TestSetConfigDirOverride(priorDir) })
	priorStorage := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() { gitserver.TestSetForceFileStorage(priorStorage) })
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })

	return &importFixture{
		draftLedgerFixture: base,
		claudeProjects:     filepath.Join(claudeDir, "projects"),
		codexHome:          codexHome,
		visibility:         "private",
		summarizer:         &fakeSummarizer{},
		store:              newFakeLFSStore(t),
		native:             map[string][]adapters.RawEntry{},
	}
}

// pastSession is one native session written to its tool's store.
type pastSession struct {
	agent    nativeimport.Agent
	id       string
	start    time.Time
	prompt   string
	reply    string              // empty: a transcript with no reply
	inFlight bool                // Codex: a turn started and never completed
	hook     string              // Claude: SessionStart hook output
	entries  []adapters.RawEntry // adapter output; derived from prompt and reply when nil
	cwd      string              // where it ran; the project root when empty
	alsoIn   string              // a second working directory it also used
	folder   string              // Claude: the projects folder; derived from cwd when empty
	source   json.RawMessage     // Codex: session_meta.source; "cli" when nil
}

func jsonLine(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}

// add writes s to its native store, quiet for two hours, and registers what
// the adapter returns for it. It returns the native file's path.
func (f *importFixture) add(t *testing.T, s pastSession) string {
	t.Helper()
	cwd := f.projectRoot
	if s.cwd != "" {
		cwd = s.cwd
	}
	stamp := func(d time.Duration) string { return s.start.Add(d).UTC().Format(time.RFC3339Nano) }
	var path string
	var lines []string
	if s.agent == nativeimport.AgentClaude {
		folder := s.folder
		if folder == "" {
			folder = "-work-" + filepath.Base(cwd)
		}
		path = filepath.Join(f.claudeProjects, folder, s.id+".jsonl")
		record := func(typ string, d time.Duration, fields map[string]any) string {
			rec := map[string]any{"type": typ, "sessionId": s.id, "timestamp": stamp(d), "cwd": cwd, "gitBranch": "main"}
			for k, v := range fields {
				rec[k] = v
			}
			return jsonLine(t, rec)
		}
		if s.hook != "" {
			lines = append(lines, record("attachment", 0, map[string]any{
				"attachment": map[string]any{"type": "hook_success", "hookEvent": "SessionStart", "content": s.hook},
			}))
		}
		lines = append(lines, record("user", time.Second, map[string]any{"message": map[string]any{"role": "user", "content": s.prompt}}))
		if s.alsoIn != "" {
			lines = append(lines, jsonLine(t, map[string]any{"type": "user", "sessionId": s.id, "timestamp": stamp(2 * time.Second),
				"cwd": s.alsoIn, "message": map[string]any{"role": "user", "content": "and over here"}}))
		}
		if s.reply != "" {
			lines = append(lines, record("assistant", time.Minute, map[string]any{
				"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": s.reply}}},
			}))
		}
	} else {
		day := s.start.UTC()
		path = filepath.Join(f.codexHome, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"),
			"rollout-"+day.Format("2006-01-02T15-04-05")+"-"+s.id+".jsonl")
		record := func(typ string, d time.Duration, payload map[string]any) string {
			return jsonLine(t, map[string]any{"type": typ, "timestamp": stamp(d), "payload": payload})
		}
		source := s.source
		if source == nil {
			source = json.RawMessage(`"cli"`)
		}
		lines = append(lines,
			record("session_meta", 0, map[string]any{"id": s.id, "cwd": cwd, "source": source, "git": map[string]any{"branch": "main"}}),
			record("response_item", time.Second, map[string]any{
				"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": s.prompt}},
			}),
			record("event_msg", 2*time.Second, map[string]any{"type": "task_started"}),
		)
		if s.alsoIn != "" {
			lines = append(lines, record("turn_context", 3*time.Second, map[string]any{"cwd": s.alsoIn}))
		}
		if s.reply != "" {
			lines = append(lines, record("response_item", time.Minute, map[string]any{
				"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": s.reply}},
			}))
		}
		if !s.inFlight {
			lines = append(lines, record("event_msg", time.Minute+time.Second, map[string]any{"type": "task_complete"}))
		}
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	quiet := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(path, quiet, quiet))

	entries := s.entries
	if entries == nil {
		entries = []adapters.RawEntry{{Timestamp: s.start.Add(time.Second), Role: "user", Content: s.prompt}}
		if s.reply != "" {
			entries = append(entries, adapters.RawEntry{Timestamp: s.start.Add(time.Minute), Role: "assistant", Content: s.reply})
		}
	}
	f.mu.Lock()
	f.native[path] = entries
	f.mu.Unlock()
	return path
}

// readNative is the adapter: it answers only for the exact file it is given.
func (f *importFixture) readNative(_ nativeimport.Agent, path string) ([]adapters.RawEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, path)
	if f.readErr != nil {
		return nil, f.readErr
	}
	entries, ok := f.native[path]
	if !ok {
		return nil, errors.New("adapter: no such session file")
	}
	return append([]adapters.RawEntry(nil), entries...), nil
}

func (f *importFixture) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reads)
}

type importRun struct {
	out    string
	report importJSONOutput
	err    error
}

func (f *importFixture) run(t *testing.T, opts importOptions) importRun {
	t.Helper()
	return f.runOn(t, f.ledgerPath, opts)
}

// runOn runs everything after preflight against the given Ledger clone, with
// production dependencies except the seams the fixture fakes.
func (f *importFixture) runOn(t *testing.T, ledgerPath string, opts importOptions) importRun {
	t.Helper()
	env, dest := f.envFor(ledgerPath, opts)
	ctx := f.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var buf bytes.Buffer
	r := importRun{err: runSessionImportFlow(ctx, &buf, opts, env, dest)}
	r.out = buf.String()
	if opts.jsonOut {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &r.report), r.out)
	}
	return r
}

func (f *importFixture) envFor(ledgerPath string, opts importOptions) (*importEnv, importDestination) {
	env := &importEnv{
		projectRoot: f.projectRoot,
		ledgerPath:  ledgerPath,
		repoID:      e2eRepoID,
		username:    "Devon",
		stagingRoot: importStagingRoot(ledgerPath),
		summarizer:  opts.summarizer,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env.deps = productionImportDeps(context.Background(), env)
	env.deps.readNative = f.readNative
	env.deps.runner = func(nativeimport.Agent) agentwork.Runner { return f.summarizer }
	env.deps.lfsClient = func() (*lfs.Client, error) { return f.store.client(), nil }
	env.deps.notify = func(_ *lfs.SessionMeta, name string) {
		f.mu.Lock()
		f.notified = append(f.notified, name)
		f.mu.Unlock()
	}
	env.deps.usable = func(nativeimport.Agent) bool { return !f.unusable }
	env.deps.syncLedger = func() {}
	env.deps.interactive = func() bool { return f.interactive }
	if f.confirm != nil {
		env.deps.confirm = f.confirm
	}
	if f.push != nil {
		env.deps.push = f.push
	}
	env.pushBatch = f.pushBatch
	env.progress = &f.progress
	return env, importDestination{Team: e2eTeam, RepoID: e2eRepoID, Visibility: f.visibility, Ledger: ledgerPath, verified: true}
}

func (r importRun) session(t *testing.T, nativeID string) importJSONSession {
	t.Helper()
	for _, s := range r.report.Sessions {
		if s.NativeID == nativeID {
			return s
		}
	}
	require.Failf(t, "session missing from the report", "%s not in %s", nativeID, r.out)
	return importJSONSession{}
}

// remoteSessionDirs lists the session directories on the bare remote's HEAD.
func remoteSessionDirs(t *testing.T, barePath string) []string {
	t.Helper()
	seen := map[string]bool{}
	var dirs []string
	for _, p := range remoteTree(t, barePath) {
		parts := strings.Split(p, "/")
		if len(parts) >= 3 && parts[0] == "sessions" && !seen[parts[1]] {
			seen[parts[1]] = true
			dirs = append(dirs, parts[1])
		}
	}
	return dirs
}

func remoteMeta(t *testing.T, barePath, name string) lfs.SessionMeta {
	t.Helper()
	var meta lfs.SessionMeta
	require.NoError(t, json.Unmarshal([]byte(runGit(t, barePath, "show", "HEAD:sessions/"+name+"/meta.json")), &meta))
	return meta
}

// uploadedContent follows the remote's pointer for a session file to the
// bytes the LFS store received.
func (f *importFixture) uploadedContent(t *testing.T, name, file string) string {
	t.Helper()
	oid, size, err := lfs.ParsePointer(runGit(t, f.barePath, "show", "HEAD:sessions/"+name+"/"+file) + "\n")
	require.NoError(t, err, "%s on the remote must be an LFS pointer", file)
	data := f.store.object(oid)
	require.NotNil(t, data, "%s points at an object the LFS store never received", file)
	require.EqualValues(t, size, len(data))
	return string(data)
}

// optionsFromCommand runs a printed command the way an AI coworker does: as
// written, with JSON output, in an agent context.
func optionsFromCommand(t *testing.T, command string) importOptions {
	t.Helper()
	fields := strings.Fields(command)
	require.GreaterOrEqual(t, len(fields), 3, command)
	require.Equal(t, []string{"ox", "session", "import"}, fields[:3], command)
	opts := importOptions{jsonOut: true, agentCtx: true}
	for i := 3; i < len(fields); i++ {
		switch fields[i] {
		case "--yes":
			opts.yes = true
		case "--session":
			i++
			opts.sessions = append(opts.sessions, strings.Split(fields[i], ",")...)
		case "--summarizer":
			i++
			agent, ok := parseImportAgent(fields[i])
			require.True(t, ok, command)
			opts.summarizer = agent
		default:
			t.Fatalf("unexpected argument %q in %q", fields[i], command)
		}
	}
	return opts
}

// appendClaudePrompt continues a Claude Code transcript, as a resumed session
// does, and lets it go quiet again.
func appendClaudePrompt(t *testing.T, path, id, cwd, prompt string, at time.Time) {
	t.Helper()
	line := jsonLine(t, map[string]any{"type": "user", "sessionId": id, "timestamp": at.UTC().Format(time.RFC3339Nano), "cwd": cwd,
		"message": map[string]any{"role": "user", "content": prompt}})
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(line + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	quiet := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(path, quiet, quiet))
}

func assertNothingMoved(t *testing.T, f *importFixture, remoteHead string) {
	t.Helper()
	assert.Zero(t, f.summarizer.calls(), "nothing may be summarized")
	assert.Zero(t, f.store.count(), "nothing may be uploaded to LFS")
	assert.Equal(t, remoteHead, runGit(t, f.barePath, "rev-parse", "HEAD"), "nothing may be pushed")
	assert.Equal(t, remoteHead, runGit(t, f.ledgerPath, "rev-parse", "HEAD"), "nothing may be committed")
}

// --- Rule: Every chosen past session lands in the Ledger once, with a summary

// Devon imports past Claude Code and Codex sessions: each lands on the remote
// in exactly one commit, with its summary in that same commit, a pointer to
// the uploaded transcript, and its own link.
//
// Failure prevented: a session that lands without a summary (leaving it to a
// repair path that rewrites meta.json, the #1105 wedge), lands twice, or is
// summarized only after its transcript already left the machine.
func TestImportE2E_EachChosenSessionLandsOnceWithSummary(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 12, 14, 3, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt,
		reply: "The callback handler drops the state cookie on Safari; setting SameSite=Lax fixes the loop."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(48 * time.Hour), prompt: pushPrompt,
		reply: "A stale pointer references an object that was never uploaded; re-uploading it lets the push through."})
	var uploadedBeforeSummary []string
	f.summarizer.before = func(prompt string) {
		for _, phrase := range []string{loginPrompt, pushPrompt} {
			if strings.Contains(prompt, phrase) && f.store.holding(phrase) > 0 {
				uploadedBeforeSummary = append(uploadedBeforeSummary, phrase)
			}
		}
	}

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	require.NoError(t, r.err, r.out)

	assert.Equal(t, "done", r.report.Status)
	assert.Empty(t, uploadedBeforeSummary, "a transcript left the machine before its summary was written")
	assert.Len(t, remoteSessionDirs(t, f.barePath), 2, "exactly the two chosen sessions reach the Ledger")
	links := map[string]bool{}
	for _, c := range []struct{ id, prompt, agentType string }{
		{e2eClaudeA, loginPrompt, "claude-code"},
		{e2eCodexA, pushPrompt, "codex"},
	} {
		s := r.session(t, c.id)
		assert.Equal(t, "uploaded", s.Outcome, s.Detail)
		name := s.SessionName
		assert.Contains(t, name, "-import-")
		assert.Equal(t, []string{"session import: " + name}, commitsTouching(t, f.barePath, "sessions/"+name),
			"one commit carries the whole session")

		meta := remoteMeta(t, f.barePath, name)
		assert.Equal(t, s.SessionID, meta.SessionID)
		assert.Equal(t, c.agentType, meta.AgentType)
		assert.Equal(t, e2eTitleFor(c.prompt), meta.Title, "the summary written on this machine is in the first commit")
		assert.NotEmpty(t, meta.Summary)
		require.Len(t, meta.NativeSessions, 1)
		assert.Equal(t, c.id, meta.NativeSessions[0].ID)
		assert.Equal(t, nativeimport.NativeSourceImport, meta.NativeSessions[0].Source)
		assert.False(t, meta.IsDraft())

		tree := strings.Join(remoteTree(t, f.barePath), "\n")
		for _, file := range []string{"meta.json", "summary.json", "raw.jsonl", "summary.md", "session.md"} {
			assert.Contains(t, tree, "sessions/"+name+"/"+file)
		}
		raw := f.uploadedContent(t, name, "raw.jsonl")
		assert.Contains(t, raw, c.prompt, "the uploaded transcript is the conversation")
		assert.Contains(t, raw, s.SessionID, "the transcript header carries the session's identity")
		assert.Contains(t, f.uploadedContent(t, name, "summary.md"), meta.Summary, "the uploaded summary is this session's")
		rawRef, ok := meta.Files["raw.jsonl"]
		require.True(t, ok, "meta.json lists the uploaded transcript")
		assert.Equal(t, int64(len(raw)), rawRef.Size)
		landed := filepath.Join(f.ledgerPath, "sessions", name)
		assert.True(t, lfs.RecoverEmptyTitleMeta(landed, true).Skipped, "the daemon's empty-title repair never rewrites an import (#1105)")
		// The re-arm reads the transcript before the meta; give it the hydrated
		// copy a reader leaves in the cache, so only the meta decides.
		cache := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
		require.NoError(t, os.MkdirAll(cache, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(cache, "raw.jsonl"), []byte(raw), 0o600))
		assert.False(t, lfs.ResetInlineSummaryEligible(landed, true, nil, f.ledgerPath), "nor does its summary re-arm")

		assert.True(t, strings.HasSuffix(s.URL, "/c/"+s.SessionID), "each session has its own link: %s", s.URL)
		links[s.URL] = true
	}
	assert.Len(t, links, 2)
	assert.ElementsMatch(t, []string{r.session(t, e2eClaudeA).SessionName, r.session(t, e2eCodexA).SessionName}, f.notified)
	for _, isolated := range f.summarizer.isolated {
		assert.True(t, isolated, "the summarizer runs in its isolated mode")
	}
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--untracked-files=no"), "no half-staged state left in the clone")
	staging, _ := os.ReadDir(importStagingRoot(f.ledgerPath))
	assert.Empty(t, staging, "staging is cleaned up")
	gitFsckClean(t, f.barePath)
}

// --- Rule: Running the import again uploads nothing twice

// Avery re-runs the import, and Sam runs it on a second laptop whose clone
// only knows the Ledger: both see the sessions as already imported, and a
// second confirmed run summarizes, uploads and commits nothing.
//
// Failure prevented: every re-run (or every machine) duplicating a coworker's
// whole history in the team's Ledger.
func TestImportE2E_RerunUploadsNothingTwice(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	claudePath := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded the object."})
	first := f.run(t, importOptions{yes: true, jsonOut: true})
	require.NoError(t, first.err, first.out)
	require.Len(t, remoteSessionDirs(t, f.barePath), 2)
	head := runGit(t, f.barePath, "rev-parse", "HEAD")
	calls, objects, reads := f.summarizer.calls(), f.store.count(), f.readCount()

	preview := f.run(t, importOptions{dryRun: true, jsonOut: true})
	require.NoError(t, preview.err, preview.out)
	for _, id := range []string{e2eClaudeA, e2eCodexA} {
		s := preview.session(t, id)
		assert.Equal(t, string(stateAlreadyImported), s.State)
		assert.Equal(t, first.session(t, id).SessionName, s.CoveredBy)
		assert.False(t, s.Selected)
	}
	assert.Empty(t, preview.report.NextCommand, "nothing is left to upload")

	again := f.run(t, importOptions{yes: true, jsonOut: true})
	require.NoError(t, again.err, again.out)
	assert.Equal(t, "done", again.report.Status, "a confirmed run with nothing to do still reports")
	assert.Zero(t, again.report.Counts["outcome_uploaded"])
	assert.Equal(t, calls, f.summarizer.calls(), "nothing is summarized again")
	assert.Equal(t, objects, f.store.count(), "nothing is uploaded again")
	assert.Equal(t, reads, f.readCount(), "no transcript is even converted again")
	assert.Equal(t, head, runGit(t, f.barePath, "rev-parse", "HEAD"), "nothing is committed or pushed again")

	t.Run("Sam's second laptop", func(t *testing.T) {
		secondClone := cloneBare(t, f.barePath)
		laptop := f.runOn(t, secondClone, importOptions{dryRun: true, jsonOut: true})
		require.NoError(t, laptop.err, laptop.out)
		for _, id := range []string{e2eClaudeA, e2eCodexA} {
			assert.Equal(t, string(stateAlreadyImported), laptop.session(t, id).State)
		}
	})

	t.Run("a session continued after its import is reported, not uploaded again", func(t *testing.T) {
		appendClaudePrompt(t, claudePath, e2eClaudeA, f.projectRoot, "one more thing about the cookie", start.Add(3*time.Hour))
		later := f.run(t, importOptions{yes: true, jsonOut: true})
		require.NoError(t, later.err, later.out)
		s := later.session(t, e2eClaudeA)
		assert.Equal(t, string(stateAlreadyImported), s.State)
		assert.Equal(t, "continued after it was imported; the later part is not in the Ledger", s.Reason)
		assert.Equal(t, calls, f.summarizer.calls())
		assert.Equal(t, head, runGit(t, f.barePath, "rev-parse", "HEAD"))
	})
}

// --- Rule: Sessions ox already recorded are never uploaded again

// Riley recorded a session with an ox that did not keep native session IDs.
// The only link between that recording and the native transcript is the
// session-context marker ox printed into it; the import must find it and
// leave the session alone, while a session ox never saw is still imported.
//
// Failure prevented: a second, differently named copy of every session
// recorded before native IDs were kept.
func TestImportE2E_OlderRecordingIsNotUploadedAgain(t *testing.T) {
	f := newImportFixture(t)
	recorded := "2026-04-01T10-00-riley-OxOLD2"
	dir := filepath.Join(f.ledgerPath, "sessions", recorded)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"version":"1.0","session_name":"`+recorded+
		`","agent_id":"OxOLD2","agent_type":"claude-code","username":"Riley","created_at":"2026-04-01T10:00:00Z","title":"Tune the retry backoff"}`), 0o644))
	runGit(t, f.ledgerPath, "add", "sessions/"+recorded)
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-m", "session: "+recorded)
	runGit(t, f.ledgerPath, "push")

	start := time.Date(2026, 4, 1, 9, 58, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: start, prompt: pushPrompt, reply: "Tuned the backoff.",
		hook: `<session-context agent_id="OxOLD2" recording="true" url="https://sageox.ai/repo/` + e2eRepoID + `/sessions/` + recorded + `/view">`})
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start.Add(24 * time.Hour), prompt: loginPrompt, reply: "Fixed the cookie."})

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	require.NoError(t, r.err, r.out)

	s := r.session(t, e2eClaudeB)
	assert.Equal(t, string(stateRecordedLive), s.State)
	assert.Equal(t, recorded, s.CoveredBy)
	assert.Empty(t, s.Outcome)
	assert.Equal(t, "uploaded", r.session(t, e2eClaudeA).Outcome, "negative control: a session ox never recorded is imported")
	assert.ElementsMatch(t, []string{recorded, r.session(t, e2eClaudeA).SessionName}, remoteSessionDirs(t, f.barePath))
	assert.Zero(t, f.store.holding(pushPrompt), "the recorded session's transcript is never uploaded again")
}

// --- Rule: The preview comes first and nothing moves without confirmation

// Quinn runs the import without confirming: the preview names the
// destination, that anyone can read it, the summarizer, what is ready and
// what is skipped and why, and nothing is summarized, uploaded or committed.
//
// Failure prevented: a coworker's private history published to a public
// Ledger before they saw where it was going.
func TestImportE2E_PreviewComesFirst(t *testing.T) {
	f := newImportFixture(t)
	f.visibility = "public"
	start := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: start.Add(time.Hour), prompt: pushPrompt})
	head := runGit(t, f.barePath, "rev-parse", "HEAD")

	r := f.run(t, importOptions{})
	require.NoError(t, r.err, r.out)
	assert.Contains(t, r.out, "Destination  "+e2eTeam+" · "+e2eRepoID+" · PUBLIC: anyone can read it")
	assert.Contains(t, r.out, "Summaries    claude · ")
	assert.Contains(t, r.out, "Ready to upload (1)")
	assert.Contains(t, r.out, nativeShortID(e2eClaudeA))
	assert.Contains(t, r.out, "Skipped (1)")
	assert.Contains(t, r.out, "no conversation (1)")
	assert.Contains(t, r.out, "Nothing was uploaded. To upload these 1 session:\n  ox session import --yes --session "+e2eClaudeA+"\n")
	assertNothingMoved(t, f, head)

	t.Run("Devon's AI coworker gets the preview and is told to ask", func(t *testing.T) {
		r := f.run(t, importOptions{agentCtx: true, jsonOut: true})
		require.NoError(t, r.err, r.out)
		assert.Equal(t, "preview", r.report.Status)
		assert.Equal(t, "public", r.report.Destination.Visibility)
		assert.Equal(t, importAgentGuidance, r.report.Guidance)
		assert.Equal(t, "ox session import --yes --session "+e2eClaudeA, r.report.NextCommand)
		assert.True(t, r.session(t, e2eClaudeA).Selected)
		assertNothingMoved(t, f, head)
	})
}

// --- Rule: Unfinished and empty sessions are left alone

// Avery's Codex session still has a turn running and one of Sam's transcripts
// has no reply: even a confirmed run leaves both alone, never reading them,
// while a finished session in the same run is imported.
//
// Failure prevented: a half-finished session frozen into the Ledger under the
// name its finished version needs, or an empty transcript uploaded as history.
func TestImportE2E_UnfinishedAndEmptySessionsAreLeftAlone(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	running := f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexB, start: start, prompt: pushPrompt,
		reply: "Still looking.", inFlight: true})
	empty := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: start.Add(time.Hour), prompt: tokenPrompt})
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start.Add(2 * time.Hour), prompt: loginPrompt, reply: "Fixed the cookie."})

	preview := f.run(t, importOptions{})
	require.NoError(t, preview.err, preview.out)
	assert.Contains(t, preview.out, "in progress → finish it, then rerun")

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	require.NoError(t, r.err, r.out)
	assert.Equal(t, string(stateInProgress), r.session(t, e2eCodexB).State)
	assert.Equal(t, string(stateIneligible), r.session(t, e2eClaudeB).State)
	assert.Equal(t, "no conversation", r.session(t, e2eClaudeB).Reason)
	assert.Equal(t, "uploaded", r.session(t, e2eClaudeA).Outcome, "negative control: the finished session is imported")
	assert.Equal(t, []string{r.session(t, e2eClaudeA).SessionName}, remoteSessionDirs(t, f.barePath))
	assert.NotContains(t, f.reads, running)
	assert.NotContains(t, f.reads, empty)

	t.Run("a session resumed while it is summarized is left for a later run", func(t *testing.T) {
		resumed := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeC, start: start.Add(3 * time.Hour), prompt: tokenPrompt,
			reply: "The deploy bot."})
		f.summarizer.before = func(prompt string) {
			if strings.Contains(prompt, tokenPrompt) {
				appendClaudePrompt(t, resumed, e2eClaudeC, f.projectRoot, "and rotate it now", start.Add(4*time.Hour))
			}
		}
		r := f.run(t, importOptions{yes: true, jsonOut: true, sessions: []string{e2eClaudeC}})
		require.NoError(t, r.err, r.out)
		s := r.session(t, e2eClaudeC)
		assert.Equal(t, "skipped", s.Outcome)
		assert.Contains(t, s.Detail, "became active")
		assert.NotContains(t, remoteSessionDirs(t, f.barePath), s.SessionName)
		assert.Zero(t, f.store.holding(tokenPrompt), "nothing of it is uploaded")
	})
}

// --- Rule: A failure is reported with the exact retry, and leaves nothing half-done

// The summarizer times out on one of Devon's sessions: the others are
// uploaded, the failed one is reported with the command that retries it,
// nothing of it reached the Ledger or the LFS store, and the retry command
// then imports it.
//
// Failure prevented: a failed session committed without its summary or
// half-staged in the clone, where the next sync or repair wedges on it.
func TestImportE2E_FailureReportsExactRetryAndLeavesNothingHalfDone(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 14, 16, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded the object."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA2, start: start.Add(time.Hour + 30*time.Second), prompt: tokenPrompt, reply: "The deploy bot."})
	f.summarizer.fail = func(prompt string) error {
		if strings.Contains(prompt, pushPrompt) {
			return context.DeadlineExceeded
		}
		return nil
	}

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	assert.ErrorIs(t, r.err, cli.ErrSilent, "a failed session fails the run")
	assert.Equal(t, "uploaded", r.session(t, e2eClaudeA).Outcome, "the other sessions are uploaded")
	assert.Equal(t, "uploaded", r.session(t, e2eCodexA2).Outcome, "the other sessions are uploaded")
	failed := r.session(t, e2eCodexA)
	assert.Equal(t, "failed", failed.Outcome)
	assert.Contains(t, failed.Detail, "summary")
	assert.Equal(t, "ox session import --session "+e2eCodexA, failed.Retry, "the retry names the full ID")
	assert.ElementsMatch(t, []string{r.session(t, e2eClaudeA).SessionName, r.session(t, e2eCodexA2).SessionName}, remoteSessionDirs(t, f.barePath))
	assert.Zero(t, f.store.holding(pushPrompt), "nothing of the failed session was uploaded")
	assert.NoDirExists(t, filepath.Join(f.ledgerPath, "sessions", failed.SessionName))
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--untracked-files=no"))

	f.summarizer.fail = nil
	retryOpts := optionsFromCommand(t, failed.Retry)
	retryOpts.yes = true // the coworker confirms the retry's preview
	retry := f.run(t, retryOpts)
	require.NoError(t, retry.err, retry.out)
	assert.Equal(t, "uploaded", retry.session(t, e2eCodexA).Outcome, "the reported retry imports it")
	assert.Contains(t, remoteSessionDirs(t, f.barePath), failed.SessionName)
}

// --- Rule: Credential output is redacted before anything leaves the machine

// One of Quinn's past sessions ran `gh auth token`. The token's shape matches
// no secret pattern, so only the credential-output rule can catch it, and
// only if the command and its output go through the same writer. Neither the
// summarizer prompt, the LFS store nor any object on the remote may hold it.
//
// Failure prevented: a coworker's GitHub token published to the team's Ledger
// and sent to an LLM by an import of their old sessions.
func TestImportE2E_CredentialOutputIsRedactedBeforeLeaving(t *testing.T) {
	f := newImportFixture(t)
	const token = "Zq8vN3kL0pR7tY2wX5cB9mJ4hF6dS1aG"
	patterns, problems := session.NewRedactorWithCustomRules(f.projectRoot)
	require.Empty(t, problems)
	for _, p := range session.DefaultExtraDetectors() {
		patterns.AddPattern(p)
	}
	require.False(t, patterns.ContainsSecrets(token), "precondition: no pattern knows this token, so the command rule is under test")

	start := time.Date(2026, 9, 5, 11, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start, prompt: tokenPrompt, reply: "Logged in as the deploy bot.",
		entries: []adapters.RawEntry{
			{Timestamp: start.Add(time.Second), Role: "user", Content: tokenPrompt},
			{Timestamp: start.Add(2 * time.Second), Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"gh auth token"}`, CallID: "call_tok"},
			{Timestamp: start.Add(3 * time.Second), Role: "tool", ToolName: "exec_command", ToolOutput: token, CallID: "call_tok"},
			{Timestamp: start.Add(time.Minute), Role: "assistant", Content: "The CLI is logged in as the deploy bot, so rotate that account."},
		}})

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	require.NoError(t, r.err, r.out)
	s := r.session(t, e2eCodexA)
	require.Equal(t, "uploaded", s.Outcome, s.Detail)

	raw := f.uploadedContent(t, s.SessionName, "raw.jsonl")
	assert.Contains(t, raw, "[REDACTED:credential-output:gh-auth-token]", "the uploaded session shows the output as redacted")
	assert.Zero(t, f.store.holding(token), "the token never reached the LFS store")
	assertRemoteObjectsCleanOf(t, f.barePath, token)
	for _, prompt := range f.summarizer.prompts {
		assert.NotContains(t, prompt, token, "the token was never sent to the summarizer")
	}
}

// Devon previews only last week's Codex sessions and approves them; the AI
// coworker then runs the command the preview printed. Only what Devon saw may
// be uploaded: not the Claude session the filter hid, and not a Codex session
// that became ready after the preview.
//
// Failure prevented: a coworker's whole history, possibly to a public Ledger,
// uploaded on the strength of a preview that showed three sessions.
func TestImportE2E_UploadCommandPinsWhatWasPreviewed(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start, prompt: pushPrompt, reply: "Re-uploaded the object."})
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start.Add(time.Hour), prompt: loginPrompt, reply: "Fixed the cookie."})

	preview := f.run(t, importOptions{agent: nativeimport.AgentCodex, agentCtx: true, jsonOut: true})
	require.NoError(t, preview.err, preview.out)
	assert.Equal(t, "ox session import --yes --session "+e2eCodexA, preview.report.NextCommand)

	withClaude := f.run(t, importOptions{agent: nativeimport.AgentCodex, summarizer: nativeimport.AgentClaude, agentCtx: true, jsonOut: true})
	require.NoError(t, withClaude.err, withClaude.out)
	assert.Equal(t, "ox session import --yes --summarizer claude --session "+e2eCodexA, withClaude.report.NextCommand,
		"the summarizer the coworker chose is part of what they approved")

	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexB, start: start.Add(2 * time.Hour), prompt: tokenPrompt, reply: "The deploy bot."})
	r := f.run(t, optionsFromCommand(t, preview.report.NextCommand))
	require.NoError(t, r.err, r.out)
	assert.Equal(t, "uploaded", r.session(t, e2eCodexA).Outcome)
	assert.Equal(t, []string{r.session(t, e2eCodexA).SessionName}, remoteSessionDirs(t, f.barePath),
		"only the previewed session is uploaded")
}

// Live recording discards a session its summarizer judges "skip" and keeps a
// "local_only" one off the Ledger; the import routes the same way, whether the
// verdict came from the summarizer or from the brief-session prefilter, and a
// rerun remembers it instead of summarizing again.
//
// Failure prevented: trivial sessions filling the Ledger, and a skip reply,
// which has no title, committed as the empty-title meta.json that the daemon
// rewrites on every tick (#1105).
func TestImportE2E_SessionsNotWorthSharingStayLocal(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Never mind."})
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: start.Add(2 * time.Hour), prompt: "fix the typo", reply: "Done."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexB, start: start.Add(3 * time.Hour), prompt: tokenPrompt, reply: "Checked."})
	longReason := strings.Repeat("Personal scratch work on a local credential setup. ", 5)
	f.summarizer.reply = func(prompt string) string {
		switch {
		case strings.Contains(prompt, pushPrompt):
			return `{"quality_category":"skip","score_reason":"Asked one question and left before any work."}`
		case strings.Contains(prompt, tokenPrompt):
			return jsonLine(t, map[string]any{"title": "Check the CLI account", "summary": "Looked at which account the CLI uses.",
				"key_actions": []string{"Checked the account"}, "outcome": "success", "quality_category": "local_only", "score_reason": longReason})
		}
		return ""
	}

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	assert.NoError(t, r.err, r.out)
	assert.Equal(t, "uploaded", r.session(t, e2eClaudeA).Outcome, "negative control: a session worth sharing is imported")
	for _, id := range []string{e2eCodexA, e2eClaudeB} {
		s := r.session(t, id)
		assert.Equal(t, "skipped", s.Outcome, id)
		assert.Contains(t, s.Detail, "not worth sharing", id)
	}
	local := r.session(t, e2eCodexB)
	assert.Equal(t, "skipped", local.Outcome)
	assert.True(t, strings.HasPrefix(local.Detail, "kept local: Personal scratch work"), local.Detail)
	assert.True(t, strings.HasSuffix(local.Detail, "…"), "a long reason is clipped: %q", local.Detail)
	assert.Equal(t, []string{r.session(t, e2eClaudeA).SessionName}, remoteSessionDirs(t, f.barePath))
	assert.Zero(t, f.store.holding(pushPrompt), "nothing of a skipped session is uploaded")
	calls := f.summarizer.calls()
	assert.Equal(t, 3, calls, "a brief session is judged without the summarizer")

	again := f.run(t, importOptions{dryRun: true, jsonOut: true})
	require.NoError(t, again.err, again.out)
	for _, id := range []string{e2eCodexA, e2eClaudeB} {
		s := again.session(t, id)
		assert.Equal(t, string(stateNotShared), s.State, id)
		assert.False(t, s.Selected)
	}
	assert.Equal(t, calls, f.summarizer.calls(), "a rerun does not summarize them again")
}

// The summarizer sees only the redacted transcript, but what it writes is
// untrusted too. A summary that carries a credential pattern holds its
// session, wherever the model put it: in text the markdown shows, or in a
// field only summary.json carries. Nothing of it reaches LFS, where the
// Ledger's pre-push gate, which reads only git objects, would never see it.
//
// Failure prevented: a key a model repeated in its summary published in the
// uploaded summary.md, or committed in summary.json past a scan that could
// not read indented JSON.
func TestImportE2E_SummaryThatRepeatsASecretIsHeld(t *testing.T) {
	const key = "AKIAIOSFODNN7EXAMPLE" // AWS's published example key
	for _, tc := range []struct {
		name, field, wantFile string
	}{
		{"in the summary text", "summary", "meta.json"},
		{"only in a field no markdown shows", "score_reason", "summary.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t)
			start := time.Date(2026, 9, 3, 15, 0, 0, 0, time.UTC)
			f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
			f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Rotated it."})
			f.summarizer.reply = func(prompt string) string {
				if !strings.Contains(prompt, pushPrompt) {
					return ""
				}
				reply := map[string]any{
					"title": "Rotate the leaked deploy key", "outcome": "success", "key_actions": []string{"Rotated the key"},
					"summary": "Found the leaked key in the push log and rotated it.",
				}
				reply[tc.field] = "The key " + key + " was in the push log."
				return jsonLine(t, reply)
			}

			r := f.run(t, importOptions{yes: true, jsonOut: true})
			assert.ErrorIs(t, r.err, cli.ErrSilent)
			held := r.session(t, e2eCodexA)
			assert.Equal(t, "failed", held.Outcome)
			assert.Contains(t, held.Detail, "possible secret remains in "+tc.wantFile)
			assert.Equal(t, "uploaded", r.session(t, e2eClaudeA).Outcome, "negative control")
			assert.Zero(t, f.store.holding(key), "the key never reached the LFS store")
			assertRemoteObjectsCleanOf(t, f.barePath, key)
			assert.Equal(t, []string{r.session(t, e2eClaudeA).SessionName}, remoteSessionDirs(t, f.barePath))
			assert.NotContains(t, runGit(t, f.ledgerPath, "log", "--all", "-p"), key, "nor any local commit")
		})
	}
}

// A push fails partway through a run. Its sessions stay pending, the next
// push carries their commits, and every session is reported uploaded and
// announced once. When the last push fails, the next run pushes first.
//
// Failure prevented: sessions that did reach the Ledger reported as not
// pushed, the run failing, and the upload notice never sent.
func TestImportE2E_FailedPushIsCarriedByTheNextOne(t *testing.T) {
	f := newImportFixture(t)
	f.pushBatch = 1
	pushes, failFirst := 0, 1
	f.push = func(ctx context.Context, ledgerPath string) error {
		pushes++
		if pushes <= failFirst {
			return errors.New("network is unreachable")
		}
		return pushLedger(ctx, ledgerPath)
	}
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded the object."})

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	assert.NoError(t, r.err, r.out)
	names := []string{r.session(t, e2eClaudeA).SessionName, r.session(t, e2eCodexA).SessionName}
	for _, id := range []string{e2eClaudeA, e2eCodexA} {
		assert.Equal(t, "uploaded", r.session(t, id).Outcome, id)
	}
	assert.ElementsMatch(t, names, remoteSessionDirs(t, f.barePath))
	assert.ElementsMatch(t, names, f.notified, "each session is announced once")
	assert.Equal(t, 2, pushes)

	t.Run("a failed last push is pushed first by the next run", func(t *testing.T) {
		pushes, failFirst = 0, 1
		f.pushBatch = 0
		f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexB, start: start.Add(2 * time.Hour), prompt: tokenPrompt, reply: "The deploy bot."})
		held := f.run(t, importOptions{yes: true, jsonOut: true})
		assert.ErrorIs(t, held.err, cli.ErrSilent)
		s := held.session(t, e2eCodexB)
		assert.Equal(t, "committed", s.Outcome)
		assert.Contains(t, s.Detail, "not pushed")
		assert.NotContains(t, remoteSessionDirs(t, f.barePath), s.SessionName)

		next := f.run(t, importOptions{yes: true, jsonOut: true})
		require.NoError(t, next.err, next.out)
		assert.Contains(t, remoteSessionDirs(t, f.barePath), s.SessionName)
		assert.Equal(t, string(stateAlreadyImported), next.session(t, e2eCodexB).State)
	})
}

// The preview accounts for every native file it did not list as a candidate,
// and keeps one entry per native session.
//
// Failure prevented: another repo's sessions, ox's own summarizer runs, or
// subagent threads uploaded as this repo's history; or a coworker told
// nothing about why most of their files were left out.
func TestImportE2E_PreviewCountsWhatItIgnores(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC)
	other := t.TempDir()
	runGit(t, other, "init", "-q")
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	longer := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt + " and on Firefox too",
		reply: "Fixed the cookie on both.", folder: "-zz-copy-of-the-repo"})
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Elsewhere.", cwd: other})
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeC, start: start.Add(2 * time.Hour), prompt: tokenPrompt, reply: "An ox run.",
		cwd: filepath.Join(paths.DataDir(), "agent-runs", "run-1")})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(3 * time.Hour), prompt: pushPrompt, reply: "Both repos.", alsoIn: other})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexB, start: start.Add(4 * time.Hour), prompt: tokenPrompt, reply: "A guardian thread.",
		source: json.RawMessage(`{"subagent":{"other":"guardian"}}`)})
	writeLines := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	writeLines(filepath.Join(f.claudeProjects, "-work-x", e2eClaudeA, "subagents", "agent-a.jsonl"), "{}\n")
	writeLines(filepath.Join(f.claudeProjects, "-work-junk", "9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a.jsonl"), "not json\n")

	r := f.run(t, importOptions{dryRun: true, jsonOut: true})
	require.NoError(t, r.err, r.out)
	assert.Equal(t, importIgnored{OtherFolders: 1, OxRuns: 1, SubagentThreads: 1, InternalThreads: 1, Unreadable: 1}, r.report.Ignored)
	mixed := r.session(t, e2eCodexA)
	assert.Equal(t, string(stateIneligible), mixed.State)
	assert.Equal(t, "it also worked in other repositories", mixed.Reason)
	copies := 0
	for _, s := range r.report.Sessions {
		if s.NativeID == e2eClaudeA {
			copies++
			info, err := os.Stat(longer)
			require.NoError(t, err)
			assert.Equal(t, info.Size(), s.SizeBytes, "the longest copy is the one imported")
		}
	}
	assert.Equal(t, 1, copies, "one entry per native session")

	recent := f.run(t, importOptions{dryRun: true, jsonOut: true, since: start.Add(90 * time.Minute)})
	require.NoError(t, recent.err, recent.out)
	for _, s := range recent.report.Sessions {
		assert.NotEqual(t, e2eClaudeA, s.NativeID, "a session last active before --since is left out")
	}
	codexOnly := f.run(t, importOptions{dryRun: true, jsonOut: true, agent: nativeimport.AgentCodex})
	require.NoError(t, codexOnly.err, codexOnly.out)
	for _, s := range codexOnly.report.Sessions {
		assert.Equal(t, string(nativeimport.AgentCodex), s.Agent)
	}

	t.Run("a summarizer that is not installed is named, not attempted", func(t *testing.T) {
		f.unusable = true
		r := f.run(t, importOptions{})
		require.NoError(t, r.err, r.out)
		assert.Contains(t, r.out, "needs summarizer → --summarizer claude|codex")
		assert.Zero(t, f.summarizer.calls())
	})
}

// At a terminal, the import shows the preview and asks; a "no" moves nothing,
// and a "yes" reports each session's outcome in plain text.
//
// Failure prevented: an upload a coworker declined, or a report that hides
// which session failed and how to retry it.
func TestImportE2E_InteractiveRunAsksFirst(t *testing.T) {
	f := newImportFixture(t)
	f.interactive = true
	var asked []string
	answer := false
	f.confirm = func(prompt string) (bool, error) {
		asked = append(asked, prompt)
		return answer, nil
	}
	start := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded."})
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: start.Add(2 * time.Hour), prompt: "fix the typo", reply: "Done."})
	f.summarizer.fail = func(prompt string) error {
		if strings.Contains(prompt, pushPrompt) {
			return errors.New("the model is overloaded")
		}
		return nil
	}
	head := runGit(t, f.barePath, "rev-parse", "HEAD")

	declined := f.run(t, importOptions{})
	require.NoError(t, declined.err, declined.out)
	require.Len(t, asked, 1)
	assert.Equal(t, "Upload 3 sessions to "+e2eTeam+"'s Ledger (private)?", asked[0])
	assert.Contains(t, declined.out, "Ready to upload (3)")
	assert.Contains(t, declined.out, "Nothing was uploaded.")
	assertNothingMoved(t, f, head)

	answer = true
	r := f.run(t, importOptions{})
	assert.ErrorIs(t, r.err, cli.ErrSilent, "a failed session fails the run")
	assert.Contains(t, r.out, "[1/3]")
	assert.Contains(t, r.out, "summarizing… ready")
	assert.Contains(t, r.out, "failed\n      summary: the model is overloaded\n      retry: ox session import --session "+e2eCodexA+"\n")
	assert.Contains(t, r.out, "skipped: not worth sharing")
	assert.Contains(t, r.out, "/c/ses_")
	assert.Contains(t, r.out, "3 selected · 1 uploaded · 1 failed · 1 skipped")
}

// Two imports against one Ledger never interleave: the second is refused
// while the first holds the import lock.
//
// Failure prevented: two runs racing to stage, commit and push the same
// sessions.
func TestImportE2E_OneImportAtATime(t *testing.T) {
	f := newImportFixture(t)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC),
		prompt: loginPrompt, reply: "Fixed the cookie."})
	target := filepath.Join(f.ledgerPath, ".sageox", "cache", "session-import")
	require.NoError(t, os.MkdirAll(target, 0o700))
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- fileutil.WithFileLockTimeout(context.Background(), target, time.Second, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	head := runGit(t, f.barePath, "rev-parse", "HEAD")
	var out bytes.Buffer
	env, dest := f.envFor(f.ledgerPath, importOptions{yes: true, jsonOut: true})
	err := runSessionImportFlow(context.Background(), &out, importOptions{yes: true, jsonOut: true}, env, dest)
	close(release)
	require.NoError(t, <-done)
	require.ErrorIs(t, err, cli.ErrSilent)
	got := decodeRefusal(t, &out)
	assert.Equal(t, importErrInProgress, got["error"])
	assertNothingMoved(t, f, head)
}

// A clone in any state #1105 describes is refused before anything is read,
// and so is one holding commits it cannot push.
//
// Failure prevented: an import adding commits to a clone mid-rebase, where
// git add would mark a teammate's conflict resolved.
func TestImportE2E_WedgedLedgerIsRefused(t *testing.T) {
	cases := []struct {
		name, want string
		wedge      func(t *testing.T, f *importFixture)
	}{
		{"a rebase in progress", "rebase", func(t *testing.T, f *importFixture) {
			require.NoError(t, os.MkdirAll(filepath.Join(f.ledgerPath, ".git", "rebase-merge"), 0o755))
		}},
		{"an earlier commit that cannot be pushed", "could not be pushed", func(t *testing.T, f *importFixture) {
			require.NoError(t, os.WriteFile(filepath.Join(f.ledgerPath, "note.txt"), []byte("x"), 0o644))
			runGit(t, f.ledgerPath, "add", "note.txt")
			runGit(t, f.ledgerPath, "commit", "--no-verify", "-q", "-m", "an earlier run's commit")
			f.push = func(context.Context, string) error { return errors.New("offline") }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t)
			f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC),
				prompt: loginPrompt, reply: "Fixed the cookie."})
			tc.wedge(t, f)
			var out bytes.Buffer
			env, dest := f.envFor(f.ledgerPath, importOptions{yes: true, jsonOut: true})
			err := runSessionImportFlow(context.Background(), &out, importOptions{yes: true, jsonOut: true}, env, dest)
			require.ErrorIs(t, err, cli.ErrSilent)
			got := decodeRefusal(t, &out)
			assert.Equal(t, importErrLedgerWedged, got["error"])
			assert.Contains(t, got["message"], tc.want)
			assert.Zero(t, f.summarizer.calls(), "nothing is summarized")
			assert.Zero(t, f.store.count(), "nothing is uploaded")
		})
	}
}

// Each way one session can fail holds only that session, with the reason in
// its report, and leaves nothing of it in the Ledger or the LFS store.
//
// Failure prevented: a failure in one step leaving a half-published session,
// or a misleading reason sending the coworker to fix the wrong thing.
func TestImportE2E_EachFailureHoldsOnlyItsSession(t *testing.T) {
	start := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	name := nativeimport.Name(nativeimport.AgentClaude, e2eClaudeA, start.Add(time.Second))
	cases := []struct {
		name          string
		setup         func(t *testing.T, f *importFixture, s *pastSession)
		outcome, want string
	}{
		{"the adapter cannot read it", func(_ *testing.T, f *importFixture, _ *pastSession) {
			f.readErr = errors.New("adapter crashed")
		}, "failed", "read claude session: adapter crashed"},
		{"the converted session is too large", func(_ *testing.T, f *importFixture, _ *pastSession) {
			f.readErr = fmt.Errorf("convert: %w", adapters.ErrAdapterOutputLimit)
		}, "failed", "too large to import"},
		{"the conversion keeps no conversation", func(_ *testing.T, _ *importFixture, s *pastSession) {
			s.entries = []adapters.RawEntry{} // the adapter understood nothing in it
		}, "skipped", "no conversation after conversion"},
		{"the LFS store refuses the upload", func(_ *testing.T, f *importFixture, _ *pastSession) {
			f.store.failBatch = true
		}, "failed", "upload to LFS"},
		{"the summarizer CLI is gone", func(_ *testing.T, f *importFixture, _ *pastSession) {
			f.summarizer.unavailable = true
		}, "failed", "not available to summarize"},
		{"the summarizer keeps exiting with an error", func(_ *testing.T, f *importFixture, _ *pastSession) {
			f.summarizer.exitCode = 1
		}, "failed", "summary: claude exited with code 1"},
		{"the summarizer CLI is not logged in", func(_ *testing.T, f *importFixture, _ *pastSession) {
			f.summarizer.exitCode, f.summarizer.exitOutput = 1, "Not logged in · Please run /login"
		}, "failed", "summary: claude exited with code 1: Not logged in · Please run /login"},
		{"the transcript is deleted while it is summarized", func(t *testing.T, f *importFixture, _ *pastSession) {
			f.summarizer.before = func(string) {
				for path := range f.native {
					require.NoError(t, os.Remove(path))
				}
			}
		}, "skipped", "native file is gone"},
		{"a rebase starts while it is summarized", func(t *testing.T, f *importFixture, _ *pastSession) {
			f.summarizer.before = func(string) {
				require.NoError(t, os.MkdirAll(filepath.Join(f.ledgerPath, ".git", "rebase-merge"), 0o755))
			}
		}, "failed", "the Ledger is not safe to write"},
		{"another machine imported it meanwhile", func(t *testing.T, f *importFixture, _ *pastSession) {
			f.summarizer.before = func(string) {
				dir := filepath.Join(f.ledgerPath, "sessions", name)
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"session_name":"`+name+`"}`), 0o644))
				runGit(t, f.ledgerPath, "add", "sessions/"+name)
				runGit(t, f.ledgerPath, "commit", "--no-verify", "-q", "-m", "pulled: another machine's import")
			}
		}, "skipped", "imported from another machine meanwhile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t)
			s := pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."}
			tc.setup(t, f, &s)
			f.add(t, s)
			r := f.run(t, importOptions{yes: true, jsonOut: true})
			got := r.session(t, e2eClaudeA)
			assert.Equal(t, tc.outcome, got.Outcome, got.Detail)
			assert.Contains(t, got.Detail, tc.want)
			assert.Zero(t, f.store.holding(loginPrompt), "nothing of it reached the LFS store")
			assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--untracked-files=no"), "nothing half-staged")
			staging, _ := os.ReadDir(importStagingRoot(f.ledgerPath))
			assert.Empty(t, staging)
		})
	}

	t.Run("summaries that keep failing validation get the deterministic one", func(t *testing.T) {
		f := newImportFixture(t)
		f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start, prompt: pushPrompt, reply: "Re-uploaded."})
		f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start.Add(time.Hour), prompt: loginPrompt, reply: "Fixed it."})
		f.summarizer.reply = func(string) string { return "I cannot summarize this." }
		r := f.run(t, importOptions{yes: true, jsonOut: true})
		require.NoError(t, r.err, r.out)
		s := r.session(t, e2eCodexA)
		require.Equal(t, "uploaded", s.Outcome, s.Detail)
		assert.Equal(t, "uploaded", r.session(t, e2eClaudeA).Outcome)
		assert.Equal(t, 6, f.summarizer.calls(), "three attempts each")
		assert.Contains(t, f.summarizer.prompts[1], "Your previous answer was rejected", "a retry says what was wrong")
		meta := remoteMeta(t, f.barePath, s.SessionName)
		assert.True(t, strings.HasPrefix(meta.Title, "Every Ledger push"), "the fallback title is the first prompt: %q", meta.Title)
		assert.Contains(t, meta.Summary, "Summary written from the session's prompts")
	})
}

// A session ox is recording right now belongs to that recording.
//
// Failure prevented: a live session imported mid-flight, then recorded again
// when it ends.
func TestImportE2E_SessionOxIsRecordingIsLeftAlone(t *testing.T) {
	f := newImportFixture(t)
	path := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC),
		prompt: loginPrompt, reply: "Fixed the cookie."})
	state, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{
		AgentID: "OxLIVE", AdapterName: "claude-code", Username: "devon",
		AgentSessionID: e2eClaudeA, AgentSessionSource: "startup", SessionFile: path,
	})
	require.NoError(t, err)

	r := f.run(t, importOptions{yes: true, jsonOut: true})
	require.NoError(t, r.err, r.out)
	s := r.session(t, e2eClaudeA)
	assert.Equal(t, string(stateInProgress), s.State)
	assert.Equal(t, "ox is recording it: "+filepath.Base(state.SessionPath), s.Reason)
	assert.Zero(t, f.summarizer.calls())
}

// --session names exactly one of this repo's sessions or the run is refused,
// before anything is summarized.
//
// Failure prevented: a retry that silently imports nothing, or the wrong one
// of two sessions that share a prefix.
func TestImportE2E_SessionFlagMustNameOneSession(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA, start: start, prompt: pushPrompt, reply: "Re-uploaded."})
	f.add(t, pastSession{agent: nativeimport.AgentCodex, id: e2eCodexA2, start: start.Add(30 * time.Second), prompt: tokenPrompt, reply: "The bot."})
	for _, tc := range []struct{ prefix, want string }{
		{"ffffffff", "matches no session of this repo"},
		{e2eCodexA[:8], "matches 2 sessions; use more characters"},
	} {
		var out bytes.Buffer
		opts := importOptions{yes: true, jsonOut: true, sessions: []string{tc.prefix}}
		env, dest := f.envFor(f.ledgerPath, opts)
		require.ErrorIs(t, runSessionImportFlow(context.Background(), &out, opts, env, dest), cli.ErrSilent)
		got := decodeRefusal(t, &out)
		assert.Equal(t, importErrBadFlag, got["error"])
		assert.Contains(t, got["message"], tc.want)
	}
	assert.Zero(t, f.summarizer.calls())
}

// A session that changes while an earlier one is being summarized is left
// for a later run, exactly as if it had changed before the preview.
func TestImportE2E_SessionThatChangesBeforeItsTurnIsLeft(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	later := f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeB, start: start.Add(time.Hour), prompt: pushPrompt, reply: "Re-uploaded."})
	f.summarizer.before = func(prompt string) {
		if strings.Contains(prompt, loginPrompt) {
			appendClaudePrompt(t, later, e2eClaudeB, f.projectRoot, "one more question", start.Add(2*time.Hour))
		}
	}
	r := f.run(t, importOptions{yes: true, jsonOut: true})
	require.NoError(t, r.err, r.out)
	assert.Equal(t, "uploaded", r.session(t, e2eClaudeA).Outcome)
	changed := r.session(t, e2eClaudeB)
	assert.Equal(t, "skipped", changed.Outcome)
	assert.Equal(t, "became active since the preview", changed.Detail)
	assert.NotContains(t, f.reads, later, "it was never read")
}

// A session ox began recording only when it was resumed is skipped like any
// recorded session, and the preview says the part before the resume is not
// in the Ledger.
//
// Failure prevented: a coworker told a session is in the Ledger when only
// its tail is.
func TestImportE2E_SessionRecordedOnlyFromAResumeIsReported(t *testing.T) {
	f := newImportFixture(t)
	start := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	f.add(t, pastSession{agent: nativeimport.AgentClaude, id: e2eClaudeA, start: start, prompt: loginPrompt, reply: "Fixed the cookie."})
	recording := "2026-09-25T10-00-devon-OxRSME"
	dir := filepath.Join(f.ledgerPath, "sessions", recording)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	resumedAt := start.Add(49 * time.Hour).Format(time.RFC3339)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"version":"1.0","session_name":"`+recording+
		`","agent_type":"claude-code","created_at":"`+resumedAt+`","title":"Finish the cookie fix","native_sessions":[{"id":"`+e2eClaudeA+
		`","source":"resume","first_seen":"`+resumedAt+`","last_seen":"`+resumedAt+`"}]}`), 0o644))
	runGit(t, f.ledgerPath, "add", "sessions/"+recording)
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-q", "-m", "session: "+recording)

	r := f.run(t, importOptions{dryRun: true, jsonOut: true})
	require.NoError(t, r.err, r.out)
	s := r.session(t, e2eClaudeA)
	assert.Equal(t, string(stateRecordedLive), s.State)
	assert.Equal(t, recording, s.CoveredBy)
	assert.Contains(t, s.Reason, "the part before that is not in the Ledger")

	text := f.run(t, importOptions{dryRun: true})
	require.NoError(t, text.err, text.out)
	assert.Contains(t, text.out, "recorded live by ox, not from its start (1)")
}
