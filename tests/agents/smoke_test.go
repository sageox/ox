//go:build agents

// Package agents_test drives real coding agents and reads their transcripts
// back through ox.
//
// Nothing else in this repo has ever done that. `make test-integration` is a
// stub that prints a message and exits 1, and the only real-agent gate lives
// in a separate repo that has not run since April 2026 and installs an
// archived opencode plus a deprecated pi package. That gap is why six adapters
// shipped parsers for formats their agent never wrote while every unit test
// stayed green: the fixtures were hand-authored in the same guessed format the
// parser expected.
//
// This test cannot be fooled that way. It makes the real agent write a real
// transcript containing a marker string, then asserts ox's own reader returns
// that marker. If the agent changes its format, this fails.
//
// Opt-in — it costs API calls and needs each agent authenticated:
//
//	OX_TEST_REAL_AGENTS=1 go test -tags=agents ./tests/agents/ -v
package agents_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentCase describes how to drive one agent for a single non-interactive turn
// and where ox should look for what it wrote.
type agentCase struct {
	// adapter is the ox adapter name.
	adapter string
	// bin is the binary to invoke, resolved against PATH.
	//
	// Watch out for name collisions: on a dev machine `pi` in PATH is commonly
	// an ipython symlink rather than the coding agent, and PATH order decides
	// which one wins. The run logs the resolved path for exactly this reason —
	// check it before trusting a result.
	bin string
	// args builds a one-shot invocation for prompt. sessionDir is a writable
	// temp directory for agents that can be told where to keep sessions.
	args func(prompt, sessionDir string) []string
	// isolatesSessions reports whether args actually confines the agent's
	// session storage to sessionDir. When false the run writes into the
	// developer's real home and cleanup is best-effort — say so rather than
	// pretending otherwise.
	isolatesSessions bool
	// verified records where the invocation came from. An unverified
	// invocation is a guess, and guesses are what this suite exists to catch,
	// so an empty value skips the case loudly instead of running a command
	// nobody confirmed.
	verified string
}

var cases = []agentCase{
	{
		adapter: "pi",
		bin:     "pi", // see the collision warning on bin
		args: func(prompt, sessionDir string) []string {
			return []string{"--print", "--session-dir", sessionDir, prompt}
		},
		isolatesSessions: true,
		verified:         "pi --help, @mariozechner/pi-coding-agent 0.64.0: --print for non-interactive, --session-dir for storage",
	},
	{
		adapter: "opencode",
		bin:     "opencode",
		args: func(prompt, _ string) []string {
			return []string{"run", prompt}
		},
		isolatesSessions: false, // writes to ~/.local/share/opencode
		verified:         "opencode --help, 1.1.19: `opencode run [message..]`",
	},
	// Every other adapter is deliberately absent. Adding one means confirming
	// its real non-interactive invocation from its own --help or docs and
	// recording that in `verified` — not guessing a flag that looks right.
}

const promptTemplate = "Reply with exactly this text and nothing else: %s"

func TestAgentWritesATranscriptOxCanRead(t *testing.T) {
	if os.Getenv("OX_TEST_REAL_AGENTS") != "1" {
		t.Skip("set OX_TEST_REAL_AGENTS=1 to drive real agents (costs API calls, needs each agent authenticated)")
	}

	repoRoot := repoRoot(t)
	binDir := t.TempDir()

	// A gate that skips every case and exits 0 reports "real agents verified"
	// when nothing ran at all — the same false green this whole PR is about.
	var ran int
	t.Cleanup(func() {
		if ran == 0 {
			t.Errorf("OX_TEST_REAL_AGENTS=1 was set but no agent case actually ran: none of %d agents was installed, authenticated, and had a confirmed invocation. "+
				"Nothing was verified; do not read this run as a pass.", len(cases))
		}
	})

	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			if tc.verified == "" {
				t.Skipf("%s: no confirmed non-interactive invocation — add one from the agent's own --help before enabling", tc.adapter)
			}
			agentBin, err := exec.LookPath(tc.bin)
			if err != nil {
				t.Skipf("%s: %s not installed", tc.adapter, tc.bin)
			}
			t.Logf("%s: driving %s (%s)", tc.adapter, agentBin, tc.verified)

			workdir := newGitRepo(t)
			sessionDir := t.TempDir()
			marker := fmt.Sprintf("ox-smoke-%s-%d", tc.adapter, time.Now().UnixNano())

			if !tc.isolatesSessions {
				t.Logf("%s: cannot confine session storage; this run writes into your real home", tc.adapter)
			}

			runAgent(t, agentBin, tc.args(fmt.Sprintf(promptTemplate, marker), sessionDir), workdir)

			adapterBin := buildAdapter(t, repoRoot, tc.adapter, binDir)
			sessionFile := findSession(t, adapterBin, workdir, sessionDir)
			entries := readEntries(t, adapterBin, sessionFile)

			ran++

			if len(entries) == 0 {
				t.Fatalf("%s: the agent just wrote a transcript and ox read zero entries from it — the reader does not match the installed agent's format",
					tc.adapter)
			}
			// The marker has to come back from BOTH sides. It is in the prompt,
			// so a reader that handles user turns and drops every assistant
			// turn would satisfy a plain "is the marker anywhere" check — and
			// that is exactly the shape of several of the bugs this PR fixes,
			// where user content is a bare string and assistant content is an
			// array of blocks.
			roles := rolesContainingMarker(entries, marker)
			if !roles["user"] {
				t.Errorf("%s: the prompt did not survive extraction — ox read %d entries and none of the user turns contained %q\n%s",
					tc.adapter, len(entries), marker, summarize(entries))
			}
			if !roles["assistant"] {
				t.Errorf("%s: ox read %d entries but the agent's REPLY is not among them — assistant turns are being dropped\n%s",
					tc.adapter, len(entries), summarize(entries))
			}
		})
	}
}

func runAgent(t *testing.T, bin string, args []string, workdir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// an auth failure is the common case on a fresh machine and is not a
		// product defect, so name it rather than reporting a bare exit code
		t.Skipf("agent run failed (often unauthenticated): %v\n%s", err, truncate(string(out), 600))
	}
	t.Logf("agent output: %s", truncate(string(out), 300))
}

// findSession asks the adapter where the agent just wrote, trying the isolated
// session dir first for agents that support one.
func findSession(t *testing.T, adapterBin, workdir, sessionDir string) string {
	t.Helper()

	args := []string{"find-session", "--repo-root", workdir}
	out, err := exec.Command(adapterBin, args...).CombinedOutput()
	if err != nil || strings.Contains(string(out), `"error"`) {
		// fall back to locating the transcript ourselves, so a broken
		// find-session does not mask a working reader (or vice versa)
		if found := newestFileUnder(sessionDir); found != "" {
			t.Logf("find-session did not resolve it (%s); using the transcript found under the isolated session dir", strings.TrimSpace(string(out)))
			return found
		}
		t.Fatalf("find-session failed and no transcript appeared under %s: %v\n%s", sessionDir, err, out)
	}

	var res struct {
		SessionFile string `json:"session_file"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("find-session returned unparseable JSON: %v\n%s", err, out)
	}
	if res.SessionFile == "" {
		t.Fatal("find-session returned an empty session_file")
	}
	return res.SessionFile
}

func readEntries(t *testing.T, adapterBin, sessionFile string) []map[string]any {
	t.Helper()
	out, err := exec.Command(adapterBin, "read", "--session-file", sessionFile).CombinedOutput()
	if err != nil {
		t.Fatalf("read failed for %s: %v\n%s", sessionFile, err, out)
	}
	var res struct {
		Entries []map[string]any `json:"entries"`
		Error   string           `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("read returned unparseable JSON: %v\n%s", err, truncate(string(out), 400))
	}
	if res.Error != "" {
		t.Fatalf("read reported: %s", res.Error)
	}
	return res.Entries
}

// rolesContainingMarker reports which roles carried the marker through
// extraction, so the caller can require both sides of the exchange rather than
// accepting whichever one happened to survive.
func rolesContainingMarker(entries []map[string]any, marker string) map[string]bool {
	roles := map[string]bool{}
	for _, e := range entries {
		role, _ := e["role"].(string)
		for _, field := range []string{"content", "tool_output"} {
			if s, ok := e[field].(string); ok && strings.Contains(s, marker) {
				roles[role] = true
			}
		}
	}
	return roles
}

func summarize(entries []map[string]any) string {
	var b strings.Builder
	for i, e := range entries {
		if i >= 8 {
			b.WriteString(fmt.Sprintf("  ... and %d more\n", len(entries)-i))
			break
		}
		role, _ := e["role"].(string)
		content, _ := e["content"].(string)
		b.WriteString(fmt.Sprintf("  [%s] %s\n", role, truncate(content, 100)))
	}
	return b.String()
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		// never touch the developer's real git identity
		{"config", "user.email", "smoke@example.invalid"},
		{"config", "user.name", "ox smoke test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Test repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func buildAdapter(t *testing.T, repoRoot, adapter, binDir string) string {
	t.Helper()
	bin := filepath.Join(binDir, "ox-adapter-"+adapter)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/ox-adapter-"+adapter)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the %s adapter failed: %v\n%s", adapter, err, out)
	}
	return bin
}

func newestFileUnder(dir string) string {
	var newest string
	var newestAt time.Time
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // a walk error on one entry must not abort the search
		}
		if info.ModTime().After(newestAt) {
			newest, newestAt = path, info.ModTime()
		}
		return nil
	})
	return newest
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the repo root")
		}
		dir = parent
	}
}
