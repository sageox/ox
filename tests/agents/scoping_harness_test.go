//go:build agents

package agents_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/skillmanager"
)

// requireLiveAgent keeps a run that could not reach the agent from looking like a run
// that passed. Under CI a missing agent fails; on a laptop it reports itself as
// unproven, in words.
func requireLiveAgent(t *testing.T, bin string) string {
	t.Helper()
	unproven := func(format string, args ...any) {
		t.Helper()
		message := fmt.Sprintf(format, args...)
		if os.Getenv("CI") == "true" {
			t.Fatalf("UNPROVEN under CI: %s", message)
		}
		t.Skipf("UNPROVEN: %s", message)
	}
	if os.Getenv("OX_TEST_REAL_AGENTS") != "1" {
		unproven("set OX_TEST_REAL_AGENTS=1 to drive real agents (costs API calls)")
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		unproven("%s is not installed", bin)
	}
	return path
}

// invalidRun reports a run that could not measure. Its message never matches a
// claim's expected failure, so a broken run can neither pass nor pose as a red.
func invalidRun(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf("INVALID: "+format, args...)
}

func uniqueMarker(t *testing.T, label string) string {
	t.Helper()
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatalf("random marker: %v", err)
	}
	return "MARKER-" + label + "-" + hex.EncodeToString(random[:])
}

var (
	oxBinaryOnce sync.Once
	oxBinaryPath string
	oxBinaryErr  error
)

// TestMain removes what this suite's real ox processes leave behind: the 140 MB binary,
// and any daemon. Almost every ox command can start a daemon on its way out, even
// `daemon stop`, so stopping politely is not enough.
func TestMain(m *testing.M) {
	code := m.Run()
	if oxBinaryPath != "" {
		stopProcessesOf(oxBinaryPath)
		_ = os.RemoveAll(filepath.Dir(oxBinaryPath))
	}
	os.Exit(code)
}

// stopProcessesOf ends the daemons running from one binary. The path is a unique temp
// directory, so this cannot reach a developer's own ox. It repeats because an ox command
// can hand off a daemon start that lands just after the command itself exits.
func stopProcessesOf(bin string) {
	for range 3 {
		_ = exec.Command("pkill", "-f", bin).Run()
		time.Sleep(time.Second)
	}
}

// headOxBinary builds ox from this checkout once per test binary. It lives outside
// t.TempDir so parallel subtests and later tests can share it.
func headOxBinary(t *testing.T) string {
	t.Helper()
	oxBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ox-scoping-bin-")
		if err != nil {
			oxBinaryErr = err
			return
		}
		oxBinaryPath = filepath.Join(dir, "ox")
		cmd := exec.Command("go", "build", "-o", oxBinaryPath, "./cmd/ox")
		cmd.Dir = repoRoot(t)
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			oxBinaryErr = fmt.Errorf("go build ./cmd/ox: %w\n%s", buildErr, out)
		}
	})
	if oxBinaryErr != nil {
		t.Fatal(oxBinaryErr)
	}
	return oxBinaryPath
}

// oxEnvironment is an isolated home for a real ox process, so a run touches none of
// the developer's config, credentials or daemon. The local server stands in for the
// cloud; nothing a local sync or prime needs comes from it, and answering 404 keeps
// the process offline.
type oxEnvironment struct {
	home     string
	endpoint string
}

func newOxEnvironment(t *testing.T) oxEnvironment {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	return oxEnvironment{home: home, endpoint: server.URL}
}

func (e oxEnvironment) vars() []string {
	return []string{
		"HOME=" + e.home,
		"XDG_CONFIG_HOME=" + filepath.Join(e.home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(e.home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(e.home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(e.home, ".local", "state"),
		"SAGEOX_TOKEN=agents-test-token",
		"SAGEOX_ENDPOINT=" + e.endpoint,
		"GIT_CONFIG_NOSYSTEM=1",
	}
}

// stopDaemon stops the daemon without letting this command start another on its way out.
func (e oxEnvironment) stopDaemon(t *testing.T, dir, bin string) {
	t.Helper()
	if _, err := e.runWith(t, dir, bin, []string{"OX_NO_DAEMON=1"}, "daemon", "stop"); err != nil {
		t.Logf("daemon stop: %v", err)
	}
}

func (e oxEnvironment) run(t *testing.T, dir, bin string, args ...string) (string, error) {
	t.Helper()
	return e.runWith(t, dir, bin, nil, args...)
}

func (e oxEnvironment) runWith(t *testing.T, dir, bin string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{"PATH=" + os.Getenv("PATH")}, e.vars()...), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("ox %v: %w\nstderr: %s", args, err, truncate(stderr.String(), 800))
	}
	return stdout.String(), nil
}

// teamRuleFixture is one published rule. Its body carries a unique marker, so finding
// the marker in a transcript can only mean the rule's text reached the agent.
type teamRuleFixture struct {
	name       string
	globs      []string
	visibility string
	body       string
	marker     string
}

func (r teamRuleFixture) source(repoSlug string) string {
	quoted := make([]string, len(r.globs))
	for i, glob := range r.globs {
		quoted[i] = fmt.Sprintf("%q", glob)
	}
	visibility := ""
	if r.visibility != "" {
		visibility = "visibility: " + r.visibility + "\n"
	}
	return fmt.Sprintf("---\nname: %s\ndescription: %s rules.\nrepos: [%q]\nglobs: [%s]\naudience: ai\n%sstatus: active\n---\n\n%s",
		r.name, r.name, repoSlug, strings.Join(quoted, ", "), visibility, r.body)
}

// scopedRuleBody is sized like a real rule: short enough to read, long enough that a
// byte count means something.
func scopedRuleBody(heading, marker string) string {
	return fmt.Sprintf("# %s\n\n%s\n\n- Keep page components small.\n- Fetch data in the route, never in a leaf.\n- Handle the empty state on purpose.\n", heading, marker)
}

const fixtureRepoSlug = "acme/web-app"

// scopingFixture is a real git repository and a real Team Context checkout, wired the
// way a teammate's machine is. Only the rule files differ from production.
type scopingFixture struct {
	env        oxEnvironment
	repo       string
	teamPath   string
	rules      []teamRuleFixture
	controlRel string
}

const fixtureTeamID = "team_fixture"

func newScopingFixture(t *testing.T, env oxEnvironment, rules []teamRuleFixture, files []string) *scopingFixture {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fx := &scopingFixture{env: env, repo: repo, rules: rules, controlRel: "control/beacon.txt"}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "fixture@test.sageox.ai")
	runGit(t, repo, "config", "user.name", "Fixture")
	runGit(t, repo, "remote", "add", "origin", "https://github.com/"+fixtureRepoSlug+".git")

	// repo_id is unique per fixture because it names the daemon: checkouts of one repo
	// share one, across directories, so a reused ID would reach another fixture's daemon.
	writeFixtureFile(t, repo, ".sageox/config.json", fmt.Sprintf(`{
  "config_version": "2",
  "repo_id": "repo_%s",
  "repo_remote_hashes": ["sha256:0000000000000000000000000000000000000000000000000000000000000000"],
  "team_id": %q,
  "team_name": "Fixture",
  "endpoint": %q,
  "update_frequency_hours": 24,
  "murmuring": "auto"
}`, newSessionID(t), fixtureTeamID, env.endpoint))

	// The real stanza `ox init` commits, read from the code that writes it.
	for _, scoped := range skillmanager.ScopedIgnoreFiles() {
		if scoped.Dir == ".claude" {
			writeFixtureFile(t, repo, ".claude/.gitignore", strings.Join(scoped.Entries, "\n")+"\n")
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range append(slices.Clone(files), fx.controlRel) {
		writeFixtureFile(t, repo, rel, "// fixture file: "+filepath.Base(rel)+"\n")
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "fixture")

	fx.teamPath = fx.createTeamContext(t)
	// Registers the checkout so `ox sync` treats it as this repo's Team Context.
	writeFixtureFile(t, repo, ".sageox/config.local.toml", fmt.Sprintf(
		"[[team_contexts]]\nteam_id = %q\nteam_name = \"Fixture\"\npath = %q\n", fixtureTeamID, fx.teamPath))
	return fx
}

// createTeamContext publishes the rules to a checkout with a real remote, because a
// daemon pull is what `ox sync` drives before it converges.
func (fx *scopingFixture) createTeamContext(t *testing.T) string {
	t.Helper()
	team := filepath.Join(fx.env.home, "team-context")
	for _, rule := range fx.rules {
		writeFixtureFile(t, team, "agents/rules/"+rule.name+".md", rule.source(fixtureRepoSlug))
	}
	runGit(t, team, "init", "-q", "-b", "main")
	runGit(t, team, "config", "user.email", "fixture@test.sageox.ai")
	runGit(t, team, "config", "user.name", "Fixture")
	runGit(t, team, "add", "-A")
	runGit(t, team, "commit", "-q", "-m", "publish rules")
	remote := filepath.Join(fx.env.home, "team-context-remote.git")
	runGit(t, fx.env.home, "init", "-q", "--bare", remote)
	runGit(t, team, "remote", "add", "origin", remote)
	runGit(t, team, "push", "-q", "origin", "main")
	runGit(t, team, "branch", "-q", "--set-upstream-to=origin/main", "main")
	return team
}

// syncWith runs real `ox sync` with bin, which pulls through a real daemon and then
// runs real Reconcile, and stops that daemon so the next binary starts its own.
func (fx *scopingFixture) syncWith(t *testing.T, bin string) {
	t.Helper()
	t.Cleanup(func() { fx.env.stopDaemon(t, fx.repo, bin) })
	var out string
	var err error
	// A daemon started right after the last one stopped can miss ox's wait for it. That
	// says nothing about scoping, so it is the one failure worth another try.
	for range 4 {
		out, err = fx.env.run(t, fx.repo, bin, "sync", "--team", fixtureTeamID, "--json")
		if err == nil || !strings.Contains(out, "daemon did not start in time") {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		t.Fatalf("ox sync: %v\n%s", err, truncate(out, 1500))
	}
	fx.env.stopDaemon(t, fx.repo, bin)
}

// projections returns the Team Rule files in the Claude root, by name.
func (fx *scopingFixture) projections(t *testing.T) map[string][]byte {
	t.Helper()
	root := filepath.Join(fx.repo, ".claude", "rules")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string][]byte{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "-team.md") {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		found[entry.Name()] = content
	}
	return found
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func writeFixtureFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// claudeTranscript is what a real Claude Code session recorded. Rule text is read from
// the transcript's attachments, never from what the model chose to say.
type claudeTranscript struct {
	raw           []byte
	reads         []string
	loaded        []loadedRule
	parallelReads bool // a Read started before the previous one returned
}

// loadedRule is one piece of rule or instruction text Claude Code put in the session.
type loadedRule struct {
	path    string
	text    string
	atStart bool // loaded at session start rather than by a file read
	reads   int  // how many file reads had completed when it arrived
}

// attachmentsHolding returns the loaded rules whose text contains marker.
func (c claudeTranscript) attachmentsHolding(marker string) []loadedRule {
	var found []loadedRule
	for _, rule := range c.loaded {
		if strings.Contains(rule.text, marker) {
			found = append(found, rule)
		}
	}
	return found
}

// parseClaudeTranscript reads the session JSONL. nested_memory and instructions are
// the attachment types Claude Code uses for rule files. This shape is undocumented,
// which is why every session also carries a control rule: if the shape changes, the
// control reads as absent and the run is INVALID instead of "rule absent".
func parseClaudeTranscript(t *testing.T, raw []byte) claudeTranscript {
	t.Helper()
	transcript := claudeTranscript{raw: raw}
	readPaths := map[string]string{}
	completedReads, outstanding := 0, 0
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<28)
	for scanner.Scan() {
		var row struct {
			Type       string `json:"type"`
			Attachment *struct {
				Type    string          `json:"type"`
				Path    string          `json:"path"`
				Content json.RawMessage `json:"content"`
				Files   []struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				} `json:"files"`
			} `json:"attachment"`
			Message *struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue // a row this reader does not model; the control catches real drift
		}
		switch row.Type {
		case "attachment":
			if row.Attachment == nil {
				continue
			}
			switch row.Attachment.Type {
			case "nested_memory":
				var content struct {
					Content string `json:"content"`
				}
				if err := json.Unmarshal(row.Attachment.Content, &content); err == nil {
					transcript.loaded = append(transcript.loaded, loadedRule{
						path: row.Attachment.Path, text: content.Content, reads: completedReads,
					})
				}
			case "instructions":
				for _, file := range row.Attachment.Files {
					transcript.loaded = append(transcript.loaded, loadedRule{
						path: file.Path, text: file.Content, atStart: true, reads: completedReads,
					})
				}
			}
		case "assistant", "user":
			if row.Message == nil {
				continue
			}
			var blocks []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				ToolUseID string `json:"tool_use_id"`
				Input     struct {
					FilePath string `json:"file_path"`
				} `json:"input"`
			}
			if err := json.Unmarshal(row.Message.Content, &blocks); err != nil {
				continue // a bare string message
			}
			for _, block := range blocks {
				switch block.Type {
				case "tool_use":
					if block.Name == "Read" {
						readPaths[block.ID] = block.Input.FilePath
						if outstanding > 0 {
							transcript.parallelReads = true
						}
						outstanding++
					}
				case "tool_result":
					if path, isRead := readPaths[block.ToolUseID]; isRead {
						transcript.reads = append(transcript.reads, path)
						completedReads++
						outstanding--
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return transcript
}

var newlineRun = regexp.MustCompile(`\n+$`)

// claudeSession runs one real Claude Code session that reads files in order, and
// returns its transcript. Project settings only: the developer's own rules and hooks
// are not part of what is being measured, and hooks off keeps a real prime out of
// these runs, so what loads is what the native root holds.
//
// A model occasionally stops before its last read. That run measured nothing, so it
// gets replaced by another rather than counted; three in a row is INVALID.
func claudeSession(t *testing.T, claude, repo string, files ...string) claudeTranscript {
	t.Helper()
	var missing string
	for range 3 {
		transcript := runClaudeSession(t, claude, repo, files)
		missing = ""
		for _, file := range files {
			if want := filepath.Join(repo, filepath.FromSlash(file)); !slices.Contains(transcript.reads, want) {
				missing = fmt.Sprintf("%s, having read %v", want, transcript.reads)
			}
		}
		if missing == "" && !transcript.parallelReads {
			return transcript
		}
	}
	invalidRun(t, "the session never read its files one per turn; missing: %q", missing)
	return claudeTranscript{}
}

func runClaudeSession(t *testing.T, claude, repo string, files []string) claudeTranscript {
	t.Helper()
	sessionID := newSessionID(t)
	var steps strings.Builder
	for i, file := range files {
		fmt.Fprintf(&steps, "Step %d: use the Read tool on %s and wait for the result before doing anything else.\n", i+1, filepath.Join(repo, filepath.FromSlash(file)))
	}
	prompt := "Do every one of these steps, one tool call per turn, and read nothing else.\n" + steps.String() + "Then reply with exactly the word DONE."

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, "-p",
		"--model", "haiku",
		"--output-format", "json",
		"--setting-sources", "project",
		"--settings", `{"disableAllHooks":true}`,
		"--session-id", sessionID,
		"--allowedTools", "Read",
		"--max-turns", fmt.Sprint(len(files)*2+2),
		prompt)
	cmd.Dir = repo
	cmd.Env = agentEnvironment()
	out, err := cmd.CombinedOutput()
	if err != nil {
		invalidRun(t, "claude -p failed: %v\n%s", err, truncate(string(out), 800))
	}

	path := claudeTranscriptPath(t, sessionID)
	raw, err := os.ReadFile(path)
	if err != nil {
		invalidRun(t, "transcript for session %s: %v", sessionID, err)
	}
	t.Cleanup(func() { removeClaudeTranscript(path) })
	return parseClaudeTranscript(t, raw)
}

// removeClaudeTranscript keeps throwaway repos from piling up under ~/.claude. Parallel
// sessions share one project directory, so it goes only with the last transcript.
func removeClaudeTranscript(path string) {
	_ = os.Remove(path)
	dir := filepath.Dir(path)
	if others, err := filepath.Glob(filepath.Join(dir, "*.jsonl")); err == nil && len(others) == 0 {
		_ = os.RemoveAll(dir)
	}
}

// agentEnvironment hands a real agent the developer's own environment for auth,
// minus everything that would connect the run to a live ox session or a parent agent.
func agentEnvironment() []string {
	env := make([]string, 0, len(os.Environ())) // safe: deliberate allowlist filter for a real agent run
	for _, entry := range os.Environ() { // safe: filtered below
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "SAGEOX_") || strings.HasPrefix(name, "OX_") || strings.HasPrefix(name, "FEATURE_") ||
			name == "AGENT_ENV" || name == "CLAUDE_ENV_FILE" || name == "CLAUDECODE" {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func newSessionID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func claudeTranscriptPath(t *testing.T, sessionID string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = filepath.Join(home, ".claude")
	}
	matches, err := filepath.Glob(filepath.Join(base, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) != 1 {
		invalidRun(t, "expected one transcript for session %s under %s, found %v (%v)", sessionID, base, matches, err)
	}
	return matches[0]
}

func toolVersion(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", bin, err)
	}
	return strings.TrimSpace(string(out))
}

// claudeVisibleBody is the rule text Claude Code should deliver: the source body with
// its trailing newlines removed. Claude Code drops the ownership stamp, which is an
// HTML comment, along with the blank line before it.
func claudeVisibleBody(body string) string {
	return newlineRun.ReplaceAllString(body, "")
}
