package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ephemeralScopedBody and ephemeralOtherBody carry a marker that appears nowhere
// else, so finding one in prime output can only mean the rule body was inlined.
const (
	ephemeralScopedBody = "Keep page components small. MARKER-SCOPED-BODY\n"
	ephemeralOtherBody  = "Only the other repository needs this. MARKER-OTHER-BODY\n"
	ephemeralTeamBody   = "Page a human on data loss. MARKER-TEAM-BODY\n"
)

// ephemeralContextBundle is the wire shape of the team context endpoint, spelled as
// JSON rather than marshaled from the client's struct, so a renamed field on either
// side fails here instead of following along.
func ephemeralContextBundle(t *testing.T) []byte {
	t.Helper()
	rule := func(name, frontmatter, body string) map[string]string {
		return map[string]string{
			"name":    name,
			"path":    "agents/rules/" + name + ".md",
			"content": "---\nname: " + name + "\ndescription: " + name + " rule.\n" + frontmatter + "audience: ai\nstatus: active\n---\n\n" + body,
		}
	}
	bundle := map[string]any{
		"team_id":          "team_fixture",
		"team_name":        "Fixture",
		"docs":             []any{},
		"agents_md":        "",
		"claude_md":        "",
		"memory":           "",
		"skills":           []any{},
		"skills_truncated": false,
		"rules": []any{
			rule("scoped-pages", "repos: [acme/web-app]\nglobs: [\"site/**/*.{tsx,css}\"]\n", ephemeralScopedBody),
			rule("other-repo", "repos: [acme/other]\n", ephemeralOtherBody),
			rule("team-wide", "", ephemeralTeamBody),
		},
	}
	data, err := json.Marshal(bundle)
	require.NoError(t, err)
	return data
}

// buildOxForPrimeTest builds the real binary from this package. A real prime has to
// run in its own process: OX_EPHEMERAL and the runtime capabilities it decides are
// cached process-wide, so an in-process call would read the test binary's answer.
func buildOxForPrimeTest(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ox")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = packageDir // TestMain moved the working directory into a sandbox
	out, err := build.CombinedOutput()
	require.NoError(t, err, "go build: %s", out)
	return bin
}

// runEphemeralPrime runs real `ox agent prime` the way a cloud session does: no
// daemon, no team checkout, OX_EPHEMERAL=1, and a token bound to the endpoint.
func runEphemeralPrime(t *testing.T, bin, repo, home, endpointURL string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "agent", "prime", "--agent", "claude-code")
	cmd.Dir = repo
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"OX_EPHEMERAL=1",
		"SAGEOX_TOKEN=ephemeral-test-token",
		"SAGEOX_ENDPOINT=" + endpointURL,
		"GIT_CONFIG_NOSYSTEM=1",
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "ox agent prime failed: %s", stderr.String())
	return stdout.String()
}

// newCommittedConfigRepo creates a real git repository holding the shape of
// .sageox/config.json that `ox init` commits. That shape has no org or project
// field, which is what made a slug built from those fields come out empty.
func newCommittedConfigRepo(t *testing.T, endpointURL, origin string) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, gitErr := cmd.CombinedOutput()
		require.NoError(t, gitErr, "git %v: %s", args, out)
	}
	git("init", "-q")
	git("config", "user.email", "fixture@test.sageox.ai")
	git("config", "user.name", "Fixture")
	if origin != "" {
		git("remote", "add", "origin", origin)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))
	config := fmt.Sprintf(`{
  "config_version": "2",
  "repo_id": "repo_00000000-0000-7000-8000-000000000000",
  "repo_remote_hashes": ["sha256:0000000000000000000000000000000000000000000000000000000000000000"],
  "team_id": "team_fixture",
  "team_name": "Fixture",
  "endpoint": %q,
  "update_frequency_hours": 24,
  "murmuring": "auto"
}`, endpointURL)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".sageox", "config.json"), []byte(config), 0o644))
	git("add", ".sageox/config.json")
	git("commit", "-q", "-m", "init")
	return repo
}

var teamRulesBlock = regexp.MustCompile(`(?s)<team-rules[ >].*?</team-rules>`)

// teamRuleRowPath returns the Path column of a rule's row in <team-rules>, or "" when
// the rule has no row.
func teamRuleRowPath(primeOutput, name string) string {
	block := teamRulesBlock.FindString(primeOutput)
	for line := range strings.SplitSeq(block, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) >= 4 && strings.TrimSpace(cells[1]) == name {
			return strings.TrimSpace(cells[len(cells)-2])
		}
	}
	return ""
}

// TestEphemeralPrime_RepoScopedRuleReachesTheSession runs real prime against a real
// repository and a local server. A cloud session has no team checkout, so this
// bundle is the only way a rule reaches it. A team that scopes its rules to named
// repos loses all of them with nothing said if repo identity is lost here.
func TestEphemeralPrime_RepoScopedRuleReachesTheSession(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the ox binary and runs four primes")
	}
	bin := buildOxForPrimeTest(t)
	bundle := ephemeralContextBundle(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/teams/team_fixture/context" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bundle)
	}))
	defer server.Close()

	// scoped-pages and other-repo both name repos.
	withheld := "2 team rules name specific repositories and were withheld"

	tests := []struct {
		name         string
		origin       string
		wantScoped   bool
		wantWithheld bool
	}{
		{name: "https origin names this repo", origin: "https://github.com/acme/web-app.git", wantScoped: true},
		{name: "ssh origin names this repo", origin: "git@github.com:acme/web-app.git", wantScoped: true},
		// A rule for another repo staying away is ordinary scoping, not a loss.
		{name: "origin names another repo", origin: "https://github.com/acme/unrelated.git"},
		// With no identity nothing can be matched, and the session must be told.
		{name: "no origin", origin: "", wantWithheld: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			repo := newCommittedConfigRepo(t, server.URL, tt.origin)
			out := runEphemeralPrime(t, bin, repo, home, server.URL)

			require.NotContains(t, out, `status="degraded"`, "prime never reached team context: %s", out)
			require.NotEmpty(t, teamRuleRowPath(out, "team-wide"),
				"an unscoped rule must arrive in every case, or this run measured nothing: %s", out)

			scopedPath := teamRuleRowPath(out, "scoped-pages")
			if tt.wantScoped {
				require.NotEmpty(t, scopedPath, "the repo-scoped rule vanished from prime: %s", teamRulesBlock.FindString(out))
				onDisk, readErr := os.ReadFile(scopedPath)
				require.NoError(t, readErr, "the row points at a file the session cannot read")
				require.Contains(t, string(onDisk), ephemeralScopedBody)
				require.NotContains(t, out, "MARKER-SCOPED-BODY", "a scoped rule must cost an index row, not its body")
			} else {
				require.Empty(t, scopedPath)
			}
			require.Empty(t, teamRuleRowPath(out, "other-repo"), "a rule for another repo reached this one")

			if tt.wantWithheld {
				require.Contains(t, out, withheld, "scoped rules were dropped and prime said nothing")
			} else {
				require.NotContains(t, out, "withheld:", "prime reported a loss that did not happen")
			}
		})
	}
}
