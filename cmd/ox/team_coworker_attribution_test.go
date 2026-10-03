//go:build !short

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/repotools"
)

// serveTeamCoworker binds a valid team SAGEOX_TOKEN to an endpoint whose
// introspection answer carries coworker verbatim. Callers isolate XDG_CACHE_HOME.
func serveTeamCoworker(t *testing.T, coworker string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"active":true,"principal_kind":"team-service","coworker":`+coworker+`}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SAGEOX_ENDPOINT", srv.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
}

const ripCoworker = `{"id":"agt_rip","display_name":"Rip"}`

// Failure prevented: prime tells a coworker it is its machine's git user.
func TestCurrentUserIdentity_TeamCoworker(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	serveTeamCoworker(t, `{"id":"agt_rip","display_name":"Rip Van Winkle"}`)

	name, aliases, kind := currentUserIdentity(endpoint.Get())
	assert.Equal(t, "Rip Van Winkle", name)
	assert.Equal(t, []string{"Rip Van Winkle", "rip-van-winkle"}, aliases)
	assert.Equal(t, "ai", kind)
}

// Failure prevented: the coworker kind never reaches the agent, or every
// person's prime grows an empty attribute.
func TestOutputAgentPrimeXML_YouKind(t *testing.T) {
	render := func(kind string) string {
		var buf bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&buf)
		_, err := outputAgentPrimeXML(cmd, agentPrimeOutput{AgentID: "a", Status: "fresh", CurrentUserName: "Rip", CurrentUserKind: kind})
		require.NoError(t, err)
		return buf.String()
	}
	assert.Contains(t, render("ai"), `you="Rip" you_kind="ai"`)
	assert.NotContains(t, render(""), "you_kind")
}

// runMurmurToOutbox runs ox murmur with no daemon, so the murmur is queued to
// a scratch ledger's outbox; it returns the error and that outbox.
func runMurmurToOutbox(t *testing.T) (string, error) {
	t.Helper()
	projectDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, ".sageox"), 0o755))
	out, err := exec.Command("git", "-C", projectDir, "init").CombinedOutput()
	require.NoError(t, err, "git init: %s", out)
	ledgerDir := t.TempDir()
	require.NoError(t, config.SaveLocalConfig(projectDir, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledgerDir}}))
	t.Setenv(config.EnvProjectRoot, projectDir)
	t.Setenv("SAGEOX_DAEMON", "false")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Chdir(projectDir)

	cmd := *murmurCmd
	_ = cmd.Flags().Set("scope", "ledger")
	cmd.SetOut(io.Discard)
	return daemon.MurmurOutboxDir(ledgerDir), cmd.RunE(&cmd, []string{"rebasing the auth branch"})
}

// Failure prevented: a coworker's murmurs reach teammates as a human's.
func TestMurmur_TeamCoworkerIsTheAIPrincipal(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	serveTeamCoworker(t, ripCoworker)

	outbox, err := runMurmurToOutbox(t)
	require.NoError(t, err)

	entries, err := os.ReadDir(outbox)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	data, err := os.ReadFile(filepath.Join(outbox, entries[0].Name()))
	require.NoError(t, err)
	var payload daemon.MurmurPayload
	require.NoError(t, json.Unmarshal(data, &payload))
	var m ledger.MurmurFile
	require.NoError(t, json.Unmarshal(payload.MurmurJSON, &m))
	assert.Equal(t, "rip", m.PrincipalID)
	assert.Equal(t, "ai", m.PrincipalType)
	assert.Equal(t, "Rip", m.PrincipalDisplay)
}

// Failure prevented: a team token with no coworker murmurs under the machine's
// git identity.
func TestMurmur_TeamTokenWithoutCoworkerRefuses(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	serveTeamCoworker(t, `null`)

	outbox, err := runMurmurToOutbox(t)
	require.ErrorIs(t, err, auth.ErrNoCoworker)
	assert.NoDirExists(t, outbox)
}

// Failure prevented: a coworker's plans are authored by its machine's git user,
// or a token with no coworker changes plan authorship at all.
func TestPlanAuthor_TeamToken(t *testing.T) {
	t.Run("coworker named", func(t *testing.T) {
		root := newPlanCaptureTestRepo(t)
		serveTeamCoworker(t, ripCoworker)

		prov, _ := resolvePlanProvenance(root)
		require.NotNil(t, prov)
		assert.Equal(t, "Rip", prov.AuthorName)
		assert.Equal(t, []string{"Rip"}, planAuthors(root))
	})
	t.Run("no coworker attached", func(t *testing.T) {
		root := newPlanCaptureTestRepo(t)
		t.Setenv("SAGEOX_TOKEN", "")
		before := planAuthors(root)
		serveTeamCoworker(t, `null`)

		assert.Equal(t, before, planAuthors(root))
	})
}

// Failure prevented: a coworker's session is listed under its machine's git
// user with no principal id.
func TestSessionMeta_TeamCoworker(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	serveTeamCoworker(t, ripCoworker)
	state := startTestRecording(t, projectRoot, "OxAiCw", "claude-code")

	require.NoError(t, writeRawHeader(projectRoot, state))
	lines := readJSONLLines(t, filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NotEmpty(t, lines)
	header, ok := lines[0]["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Rip", header["username"])

	ep := endpoint.GetForProject(projectRoot)
	meta := sessionMetaBase("s", identity.AttributionDisplayName(ep, ""), "OxAiCw", "claude-code", time.Now(), projectRoot, state.SessionID).Build()
	assert.Equal(t, "agt_rip", meta.UserID)
}

// Failure prevented: a coworker's carts commits fail, because Dolt refuses an
// author with no email and a coworker has none.
func TestCartsCommitter_CoworkerKeepsMachineGitIdentity(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "Pod")
	t.Setenv("GIT_CONFIG_KEY_1", "user.email")
	t.Setenv("GIT_CONFIG_VALUE_1", "pod@example.com")

	assert.Equal(t, &repotools.GitIdentity{Name: "Pod", Email: "pod@example.com"},
		cartsCommitter(identity.Attribution{Name: "Rip", DisplayName: "Rip", Username: "rip", AI: true}))
	assert.Equal(t, &repotools.GitIdentity{Name: "Ada", Email: "ada@example.com"},
		cartsCommitter(identity.Attribution{Name: "Ada", Email: "ada@example.com"}))
}
