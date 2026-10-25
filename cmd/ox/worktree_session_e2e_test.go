//go:build slow

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorktreeSession_RecordsAndUploadsFromEveryCheckoutLayout drives a whole
// Claude Code session through the real ox binary and the real Claude adapter,
// in each place a session runs:
//
//   - plain checkout: Claude Code, its hooks, and the AI coworker's commands
//     all run in the main checkout;
//   - desktop app: Claude Code runs in a linked worktree under
//     <repo>/.claude/worktrees/<name> and writes its transcript to that
//     worktree's ~/.claude/projects bucket, but the app runs ox's hooks in the
//     main checkout (--project-config-root), while the coworker's own
//     `ox agent <id> …` commands run in the worktree;
//   - CLI in a worktree: everything runs in the linked worktree.
//
// Each primes through the SessionStart hook, resolves its agent ID with a
// heartbeat, captures a turn through PostToolUse, and stops. The upload must
// reach the Ledger remote with the turn in its raw.jsonl.
//
// Failures prevented, in the desktop layout:
//   - #1407: prime stores the instance in the main checkout's store, so every
//     `ox agent <id> …` call from the worktree fails with "instance not found";
//   - #1397: the transcript in the worktree's bucket is judged to belong to
//     another repository, so no hook captures a turn and `session stop`
//     uploads nothing while reporting success.
func TestWorktreeSession_RecordsAndUploadsFromEveryCheckoutLayout(t *testing.T) {
	oxBin, adapterDir := buildIncrementalE2EBinaries(t)

	layouts := []struct {
		name string
		// where Claude Code runs (its transcript bucket and every turn's cwd),
		// where ox's hooks run, and where the coworker's commands run
		claude, hooks, commands string
	}{
		{"plain checkout", "main", "main", "main"},
		{"desktop app worktree", "worktree", "main", "worktree"},
		{"CLI in a worktree", "worktree", "worktree", "worktree"},
	}
	for _, layout := range layouts {
		t.Run(layout.name, func(t *testing.T) {
			w := newWorktreeSessionE2E(t, oxBin, adapterDir)
			dir := map[string]string{"main": w.main, "worktree": w.worktree}
			nativeID := uuid.NewString()
			transcript := w.claudeTranscript(t, dir[layout.claude], nativeID)

			// Given: Claude Code starts, and its SessionStart hook primes ox.
			startOut := w.hook(t, dir[layout.hooks], "SessionStart", nativeID, dir[layout.claude], transcript.path)
			agentID := primedAgentID(t, startOut)

			// Then: the coworker's own commands resolve the agent it was given.
			w.ox(t, dir[layout.commands], "agent", agentID, "heartbeat")

			// When: a turn lands in Claude Code's transcript and PostToolUse fires.
			const prompt = "Fix the worktree upload bug"
			at := time.Now().Add(time.Second).UTC()
			writeClaudeEntry(t, transcript, claudeUserEntry(at.Format(time.RFC3339Nano), prompt))
			writeClaudeEntry(t, transcript, claudeAssistantEntry(at.Add(time.Second).Format(time.RFC3339Nano),
				"Reading the adapter.", "Read", `{"file_path":"session.go"}`))
			w.hook(t, dir[layout.hooks], "PostToolUse", nativeID, dir[layout.claude], transcript.path)

			// And: the coworker stops its recording.
			stopOut := w.ox(t, dir[layout.commands], "agent", agentID, "session", "stop")
			var stop pipeline.StopOutput
			jsonStart := strings.Index(stopOut, "{")
			require.NotEqual(t, -1, jsonStart, "session stop must print a JSON result: %s", stopOut)
			require.NoError(t, json.Unmarshal([]byte(stopOut[jsonStart:]), &stop), "session stop JSON: %s", stopOut)

			// Then: the session was captured and uploaded, not reported as a
			// success with nothing behind it.
			require.True(t, stop.Success, "session stop: %s", stopOut)
			assert.Empty(t, stop.UploadWarning, "nothing may be held back from the Ledger")
			assert.GreaterOrEqual(t, stop.EntryCount, 2, "the prompt and the reply must be captured")

			// And: the Ledger remote holds the session, and the raw.jsonl it
			// points at carries the turn.
			uploaded := w.uploadedRawJSONL(t, agentID)
			assert.Contains(t, uploaded, prompt, "the uploaded raw.jsonl must carry the coworker's prompt")
			assert.Contains(t, uploaded, "Reading the adapter.", "the uploaded raw.jsonl must carry the reply")
		})
	}
}

// worktreeSessionE2E is an ox project with a linked worktree checked out where
// the Claude desktop app puts one, a Ledger cloned from a bare remote, and a
// stub SageOx endpoint that serves the Ledger's LFS store.
type worktreeSessionE2E struct {
	oxBin, adapterDir string
	main, worktree    string // symlink-resolved, as Claude Code records paths
	user              string
	home, configDir   string
	ledgerBare        string
	env               []string
	lfs               *recordingLFSServer
}

func newWorktreeSessionE2E(t *testing.T, oxBin, adapterDir string) *worktreeSessionE2E {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	w := &worktreeSessionE2E{
		oxBin: oxBin, adapterDir: adapterDir,
		main:      filepath.Join(root, "repo"),
		home:      filepath.Join(root, "home"),
		configDir: filepath.Join(root, "config"),
		lfs:       newRecordingLFSServer(t),
		user:      "oxtest-wt-" + uuid.NewString()[:8],
	}
	w.worktree = filepath.Join(w.main, ".claude", "worktrees", "brave-turing-1a2b3c")
	dataDir, cacheDir, stateDir := filepath.Join(root, "data"), filepath.Join(root, "cache"), filepath.Join(root, "state")
	for _, dir := range []string{w.main, w.home, w.configDir, dataDir, cacheDir, stateDir} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}
	credentialsFile := filepath.Join(w.configDir, "sageox", "git-credentials.json")
	w.env = []string{
		"HOME=" + w.home,
		"XDG_CONFIG_HOME=" + w.configDir,
		"XDG_DATA_HOME=" + dataDir,
		"XDG_CACHE_HOME=" + cacheDir,
		"XDG_STATE_HOME=" + stateDir,
		"OX_XDG_ENABLE=1",
		"OX_GIT_CREDENTIALS_FILE=" + credentialsFile,
		"OX_ADAPTER_PATH=" + adapterDir,
		"AGENT_ENV=claude-code",
		// session markers live under /tmp/<USER>, outside every temp dir
		"USER=" + w.user,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Devon", "GIT_AUTHOR_EMAIL=devon@test.sageox.ai",
		"GIT_COMMITTER_NAME=Devon", "GIT_COMMITTER_EMAIL=devon@test.sageox.ai",
		"SAGEOX_DAEMON=false",
	}
	endpointURL := w.lfs.URL

	// The project commits .sageox/config.json, as `ox init` has every coworker
	// do, so the linked worktree checks out a .sageox/ of its own.
	w.git(t, w.main, "init", "-q", "--initial-branch=main")
	w.git(t, w.main, "config", "user.name", "Devon")
	w.git(t, w.main, "config", "user.email", "devon@test.sageox.ai")
	require.NoError(t, os.MkdirAll(filepath.Join(w.main, ".sageox"), 0o755))
	const repoID = "repo_019c0000-0000-7000-8000-00000000e2e1"
	projectConfig := fmt.Sprintf(`{"config_version":"2","repo_id":%q,"endpoint":%q,"team_id":"team_e2e","team_name":"E2E","session_recording":"auto"}`, repoID, endpointURL)
	require.NoError(t, os.WriteFile(filepath.Join(w.main, ".sageox", "config.json"), []byte(projectConfig+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(w.main, ".gitignore"), []byte(".claude/\n"), 0o644))
	w.git(t, w.main, "add", ".sageox/config.json", ".gitignore")
	w.git(t, w.main, "commit", "-q", "-m", "initialize SageOx")
	w.git(t, w.main, "worktree", "add", "-q", "-b", "claude/brave-turing", w.worktree)

	// sign in: an auth token for the API and git credentials for the Ledger
	const accessToken = "fake-pat-for-e2e-test"
	authJSON := fmt.Sprintf(`{"tokens":{%q:{"access_token":%q,"token_type":"bearer","expires_at":%q}}}`,
		endpointURL, accessToken, time.Now().Add(24*time.Hour).Format(time.RFC3339))
	require.NoError(t, os.MkdirAll(filepath.Join(w.configDir, "sageox"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(w.configDir, "sageox", "auth.json"), []byte(authJSON), 0o600))
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", w.configDir)
	t.Setenv("XDG_DATA_HOME", dataDir)
	t.Setenv("OX_GIT_CREDENTIALS_FILE", credentialsFile)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(endpointURL, gitserver.GitCredentials{
		Username: "devon", Token: "test-token", ServerURL: endpointURL, ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint(accessToken), // fresh for this login: no refresh call
	}))

	// The Ledger: a clone of a bare remote at the default path for this repo.
	// LFS batch URLs derive from the fetch remote, so it names the stub; the
	// push still lands on the bare repository.
	w.ledgerBare = filepath.Join(root, "ledger.git")
	initBareRemote(t, w.ledgerBare, "--initial-branch=main")
	seed := filepath.Join(root, "ledger-seed")
	w.git(t, root, "clone", "-q", w.ledgerBare, seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("ledger\n"), 0o644))
	w.git(t, seed, "add", "README.md")
	w.git(t, seed, "commit", "-q", "-m", "seed")
	w.git(t, seed, "push", "-q", "origin", "main")
	ledgerPath := paths.LedgersDataDir(repoID, endpointURL)
	require.NoError(t, os.MkdirAll(filepath.Dir(ledgerPath), 0o755))
	w.git(t, root, "clone", "-q", w.ledgerBare, ledgerPath)
	w.git(t, ledgerPath, "remote", "set-url", "origin", endpointURL+"/ledger.git")
	w.git(t, ledgerPath, "remote", "set-url", "--push", "origin", w.ledgerBare)

	t.Setenv("USER", w.user)
	markers := filepath.Dir(paths.TempDir())
	t.Cleanup(func() { _ = os.RemoveAll(markers) })
	return w
}

func (w *worktreeSessionE2E) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, w.env...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v in %s: %s", args, dir, out)
	return strings.TrimSpace(string(out))
}

// ox runs the ox binary in dir and returns its stdout.
func (w *worktreeSessionE2E) ox(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := testguard.OxCmdContext(t, ctx, w.oxBin, dir, w.env, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if stderr.Len() > 0 {
		t.Logf("ox %v (in %s) stderr:\n%s", args, dir, stderr.String())
	}
	require.NoError(t, err, "ox %v in %s failed:\nstdout: %s\nstderr: %s", args, dir, out, stderr.String())
	return string(out)
}

// hook runs `ox agent hook <event>` in dir the way Claude Code's installed
// hook command does, with Claude Code's payload on stdin.
func (w *worktreeSessionE2E) hook(t *testing.T, dir, event, nativeID, claudeCwd, transcriptPath string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"session_id": nativeID, "hook_event_name": event, "source": "startup",
		"cwd": claudeCwd, "transcript_path": transcriptPath,
	})
	require.NoError(t, err)
	cmd := testguard.OxCmd(t, w.oxBin, dir, w.env, "agent", "hook", event)
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "hook %s in %s failed:\n%s", event, dir, out)
	return string(out)
}

// claudeTranscript creates the transcript Claude Code writes for a session
// running in cwd: ~/.claude/projects/<cwd with every / . _ as ->/<id>.jsonl.
func (w *worktreeSessionE2E) claudeTranscript(t *testing.T, cwd, nativeID string) claudeE2ESource {
	t.Helper()
	bucket := strings.NewReplacer(string(os.PathSeparator), "-", ".", "-", "_", "-").Replace(cwd)
	dir := filepath.Join(w.home, ".claude", "projects", bucket)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, nativeID+".jsonl")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	return claudeE2ESource{path: path, repoRoot: cwd, sessionID: nativeID}
}

// uploadedRawJSONL returns the raw.jsonl content the Ledger remote's commit
// points at for agentID's session: the LFS pointer on the remote, resolved
// against what the stub LFS store received.
func (w *worktreeSessionE2E) uploadedRawJSONL(t *testing.T, agentID string) string {
	t.Helper()
	tree := w.git(t, w.ledgerBare, "ls-tree", "-r", "--name-only", "main")
	var rawPath string
	for _, p := range strings.Split(tree, "\n") {
		if strings.HasPrefix(p, "sessions/") && strings.HasSuffix(p, "-"+agentID+"/raw.jsonl") {
			rawPath = p
		}
	}
	require.NotEmpty(t, rawPath, "the session must reach the Ledger remote; remote tree:\n%s", tree)
	pointer := w.git(t, w.ledgerBare, "show", "main:"+rawPath)
	oid, _, err := lfs.ParsePointer(pointer + "\n")
	require.NoError(t, err, "the remote raw.jsonl must be an LFS pointer, got:\n%s", pointer)
	content, ok := w.lfs.object(oid)
	require.True(t, ok, "the LFS object %s the remote points at was never uploaded", oid)
	return content
}

var primedAgentIDPattern = regexp.MustCompile(`agent_id="(Ox[A-Za-z0-9]+)"`)

// primedAgentID reads the agent ID that prime hands the AI coworker.
func primedAgentID(t *testing.T, primeOut string) string {
	t.Helper()
	m := primedAgentIDPattern.FindStringSubmatch(primeOut)
	require.NotNil(t, m, "prime must hand out an agent ID:\n%s", truncateStr(primeOut, 4000))
	return m[1]
}

// recordingLFSServer is a stub SageOx endpoint: its Git LFS batch API accepts
// every upload and keeps the bytes; every other route is not found.
type recordingLFSServer struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string]string
}

func newRecordingLFSServer(t *testing.T) *recordingLFSServer {
	t.Helper()
	s := &recordingLFSServer{objects: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/info/lfs/objects/batch"):
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
						Upload: &lfs.Action{Href: s.URL + "/upload/" + obj.OID},
						Verify: &lfs.Action{Href: s.URL + "/verify"},
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
			s.objects[strings.TrimPrefix(r.URL.Path, "/upload/")] = string(body)
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/verify":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *recordingLFSServer) object(oid string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, ok := s.objects[strings.TrimPrefix(oid, "sha256:")]
	if !ok {
		content, ok = s.objects[oid]
	}
	return content, ok
}
