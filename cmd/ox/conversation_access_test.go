package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/sageox/ox/internal/teamaccess"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Access gate: signed out, outside the team, or unverifiable, every
// conversation read and `ox agent team-ctx` refuses BEFORE a single team file
// is opened — whatever form the id arrives in. These tests stage a real
// project (config.json + config.local.toml) so the default reader path runs,
// and replace only the final open with a stub that fails the test if a
// refused caller ever reaches it.

const gateTestTeam = "team_gate"

// stageGateProject writes an initialized project bound to gateTestTeam on ep,
// with a team-context checkout holding one discussion and a distilled file,
// and points OX_PROJECT_ROOT at it. Returns the project root.
func stageGateProject(t *testing.T, ep string) string {
	t.Helper()
	root := t.TempDir()
	teamDir := filepath.Join(t.TempDir(), gateTestTeam)
	require.NoError(t, os.MkdirAll(filepath.Join(teamDir, "discussions", "2026-09-01-secret-plans"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "discussions", "2026-09-01-secret-plans", "metadata.json"),
		[]byte(`{"title":"Secret plans"}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(teamDir, "agent-context"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "agent-context", "distilled-discussions.md"),
		[]byte("TEAM-ONLY distilled content\n"), 0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{
		TeamID:   gateTestTeam,
		TeamName: "Gate Team",
		Endpoint: ep,
	}))
	toml := fmt.Sprintf("\n[[team_contexts]]\nteam_id = %q\nteam_name = %q\nslug = \"gate\"\npath = %q\nlast_sync = 0001-01-01T00:00:00Z\n",
		gateTestTeam, "Gate Team", teamDir)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.local.toml"), []byte(toml), 0o600))
	t.Setenv(config.EnvProjectRoot, root)
	return root
}

// forbidReaderOpen replaces the post-gate open. When allowed is false, any
// call fails the test; otherwise it serves the read fixture corpus. Returns a
// counter of opens.
func forbidReaderOpen(t *testing.T, allowed bool) *atomic.Int32 {
	t.Helper()
	var opens atomic.Int32
	orig := openConversationReaderAt
	t.Cleanup(func() { openConversationReaderAt = orig })
	openConversationReaderAt = func(string) (*read.Reader, *read.Error) {
		opens.Add(1)
		if !allowed {
			t.Errorf("a refused caller reached the reader: team files would have been read")
		}
		return read.New(repoPath("..", "..", "internal", "conversation", "read", "testdata", "discussions"),
			time.Date(2026, 8, 20, 17, 41, 0, 0, time.UTC)), nil
	}
	return &opens
}

// scriptTeamAccess replaces the check with a fixed verdict.
func scriptTeamAccess(t *testing.T, status teamaccess.Status) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	orig := checkTeamAccess
	t.Cleanup(func() { checkTeamAccess = orig })
	checkTeamAccess = func(_ context.Context, ep, teamID string) teamaccess.Verdict {
		calls.Add(1)
		return teamaccess.Verdict{Status: status, Endpoint: ep, TeamID: teamID, Detail: "test"}
	}
	return &calls
}

// gateInvocations is every way to ask for team content: the listing, each
// id form, a pasted link, and a share link.
var gateInvocations = []struct {
	name string
	sub  string
	args []string
}{
	{"list", "list", nil},
	{"show cnv id", "show", []string{convTestFullCnv}},
	{"show rec id", "show", []string{convTestFullRec}},
	{"show pasted link", "show", []string{"https://sageox.ai/c/" + convTestFullRec}},
	{"show share link", "show", []string{shareTestLink}},
	{"transcript citation", "transcript", []string{convTestCueURI}},
	{"transcript share link", "transcript", []string{shareTestLink, "--cues", "1-2"}},
	{"topics", "topics", []string{convTestFullCnv}},
	{"topic", "topic", []string{convTestFullCnv, convTestTopicID}},
}

// TestConversationGate_RefusesBeforeAnyRead: each refusal verdict maps to its
// code for every invocation, the reader is never opened, and a share link is
// never looked up. Failure prevented: a leaked id or link giving an AI
// coworker team content without authentication or membership.
func TestConversationGate_RefusesBeforeAnyRead(t *testing.T) {
	verdicts := []struct {
		status    teamaccess.Status
		code      string
		retryable bool
	}{
		{teamaccess.NotSignedIn, read.ErrCodeNotAuthenticated, false},
		{teamaccess.Expired, read.ErrCodeNotAuthenticated, false},
		{teamaccess.EnvTokenMalformed, read.ErrCodeNotAuthenticated, false},
		{teamaccess.NoAccess, read.ErrCodeNoTeamAccess, false},
		{teamaccess.Unverified, read.ErrCodeAccessUnverified, true},
	}
	for _, v := range verdicts {
		for _, inv := range gateInvocations {
			t.Run(v.code+"/"+inv.name, func(t *testing.T) {
				isolateConversationAuth(t)
				saveShareTestLogin(t, shareTestEndpoint, "test-env-access")
				stageGateProject(t, "https://test.sageox.ai")
				opens := forbidReaderOpen(t, false)
				calls := scriptTeamAccess(t, v.status)
				srv := newShareLookupServer(t, func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("share lookup ran for a refused caller: %s", r.URL.Path)
				})

				stdout, _, err := runConversationInProc(t, inv.sub, inv.args...)
				assert.Equal(t, 1, exitCodeOf(t, err), "refusals are runtime errors")
				env := decodeConvEnvelope(t, stdout)
				require.NotNil(t, env.Error, stdout)
				assert.False(t, env.Success)
				assert.Equal(t, v.code, env.Error.Code)
				assert.Equal(t, v.retryable, env.Error.Retryable)
				assert.NotEmpty(t, env.Guidance)
				assert.Nil(t, env.Data, "no payload on a refusal")
				assert.NotContains(t, stdout, "Secret plans")
				assert.Equal(t, int32(1), calls.Load())
				assert.Zero(t, opens.Load())
				reqs, _ := srv.requests()
				assert.Empty(t, reqs)
			})
		}
	}
}

// TestConversationGate_AllowedReachesReader: an allowed verdict opens the
// reader once and serves as before.
func TestConversationGate_AllowedReachesReader(t *testing.T) {
	isolateConversationAuth(t)
	stageGateProject(t, "https://test.sageox.ai")
	opens := forbidReaderOpen(t, true)
	calls := scriptTeamAccess(t, teamaccess.Allowed)

	stdout, _, err := runConversationInProc(t, "show", convTestFullCnv)
	require.NoError(t, err, stdout)
	assert.True(t, decodeConvEnvelope(t, stdout).Success, stdout)
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, int32(1), opens.Load())
}

// TestConversationGate_ChecksTheReposTeamAndEndpoint: the gate asks about
// the team this repo is bound to, on this repo's endpoint.
func TestConversationGate_ChecksTheReposTeamAndEndpoint(t *testing.T) {
	isolateConversationAuth(t)
	stageGateProject(t, "https://test.sageox.ai")
	forbidReaderOpen(t, true)
	var gotEP, gotTeam string
	orig := checkTeamAccess
	t.Cleanup(func() { checkTeamAccess = orig })
	checkTeamAccess = func(_ context.Context, ep, teamID string) teamaccess.Verdict {
		gotEP, gotTeam = ep, teamID
		return teamaccess.Verdict{Status: teamaccess.Allowed}
	}

	_, _, err := runConversationInProc(t, "list")
	require.NoError(t, err)
	assert.Equal(t, gateTestTeam, gotTeam)
	assert.Contains(t, gotEP, "test.sageox.ai")
}

// fakeMemberships serves GET /api/v1/cli/repos with the given teams and
// counts calls.
func fakeMemberships(t *testing.T, teams ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli/repos" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rows := make([]api.TeamMembership, 0, len(teams))
		for _, id := range teams {
			rows = append(rows, api.TeamMembership{ID: id, Name: id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"teams": rows})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestConversationGate_RealCheck runs the production teamaccess.Check (real
// auth store, real API client) against a fake SageOx: logged out, a
// non-member, and a member.
func TestConversationGate_RealCheck(t *testing.T) {
	t.Run("logged out", func(t *testing.T) {
		isolateConversationAuth(t)
		srv, calls := fakeMemberships(t, gateTestTeam)
		stageGateProject(t, srv.URL)
		opens := forbidReaderOpen(t, false)

		stdout, _, err := runConversationInProc(t, "show", convTestFullCnv)
		assert.Equal(t, 1, exitCodeOf(t, err))
		env := decodeConvEnvelope(t, stdout)
		require.NotNil(t, env.Error, stdout)
		assert.Equal(t, read.ErrCodeNotAuthenticated, env.Error.Code)
		assert.Contains(t, env.Error.Message, "ox login")
		assert.Zero(t, calls.Load(), "no server call without a credential")
		assert.Zero(t, opens.Load())
	})
	t.Run("not a member", func(t *testing.T) {
		isolateConversationAuth(t)
		srv, _ := fakeMemberships(t, "team_someone_else")
		stageGateProject(t, srv.URL)
		saveShareTestLogin(t, srv.URL, "tok-outsider")
		opens := forbidReaderOpen(t, false)

		stdout, _, err := runConversationInProc(t, "list")
		assert.Equal(t, 1, exitCodeOf(t, err))
		env := decodeConvEnvelope(t, stdout)
		require.NotNil(t, env.Error, stdout)
		assert.Equal(t, read.ErrCodeNoTeamAccess, env.Error.Code)
		assert.Contains(t, env.Error.Message, "Gate Team")
		assert.Zero(t, opens.Load())
	})
	t.Run("member, then cached", func(t *testing.T) {
		isolateConversationAuth(t)
		srv, calls := fakeMemberships(t, gateTestTeam)
		stageGateProject(t, srv.URL)
		saveShareTestLogin(t, srv.URL, "tok-member")
		opens := forbidReaderOpen(t, true)

		for range 2 {
			stdout, _, err := runConversationInProc(t, "show", convTestFullCnv)
			require.NoError(t, err, stdout)
		}
		assert.Equal(t, int32(2), opens.Load())
		assert.Equal(t, int32(1), calls.Load(), "the second read is served from the hour-long cache")
	})
}

// runTeamCtxInProc runs `ox agent team-ctx` in-process.
func runTeamCtxInProc(t *testing.T) (string, string, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "team-ctx", SilenceUsage: true, SilenceErrors: true, RunE: runAgentTeamCtx}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

// TestAgentTeamCtx_Gated: team-ctx prints nothing from the team when refused,
// and serves as before when allowed. Failure prevented: the same local
// checkout read through a second, ungated door.
func TestAgentTeamCtx_Gated(t *testing.T) {
	refusals := []struct {
		status teamaccess.Status
		code   string
	}{
		{teamaccess.NotSignedIn, read.ErrCodeNotAuthenticated},
		{teamaccess.NoAccess, read.ErrCodeNoTeamAccess},
		{teamaccess.Unverified, read.ErrCodeAccessUnverified},
	}
	for _, r := range refusals {
		t.Run(r.code, func(t *testing.T) {
			isolateConversationAuth(t)
			t.Setenv("SAGEOX_AGENT_ID", "")
			stageGateProject(t, "https://test.sageox.ai")
			scriptTeamAccess(t, r.status)

			stdout, stderr, err := runTeamCtxInProc(t)
			require.Error(t, err)
			assert.Equal(t, 1, exitCodeOf(t, err))
			assert.Empty(t, stdout, "nothing from the team is printed")
			assert.Contains(t, stderr, r.code)
			assert.NotContains(t, stdout+stderr, "TEAM-ONLY")
			assert.NotContains(t, stdout+stderr, "Secret plans")
		})
	}

	t.Run("allowed", func(t *testing.T) {
		isolateConversationAuth(t)
		t.Setenv("SAGEOX_AGENT_ID", "")
		stageGateProject(t, "https://test.sageox.ai")
		scriptTeamAccess(t, teamaccess.Allowed)

		stdout, _, err := runTeamCtxInProc(t)
		require.NoError(t, err)
		assert.True(t, strings.Contains(stdout, "TEAM-ONLY distilled content"), stdout)
	})
}
