package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importCommand is `ox session import` as a coworker runs it: the production
// RunE and flags, under a root that carries --json as ox's does.
func importCommand(args ...string) (*cobra.Command, *bytes.Buffer) {
	root := &cobra.Command{Use: "ox", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("json", false, "")
	cmd := &cobra.Command{Use: "import", Args: cobra.NoArgs, RunE: runSessionImport}
	addSessionImportFlags(cmd.Flags())
	root.AddCommand(cmd)
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetArgs(append([]string{"import"}, args...))
	return root, out
}

// importCmdProject is a logged-in coworker's project with a cloned Ledger and
// a SageOx API that answers for the repo, and empty native stores.
type importCmdProject struct {
	root, ledger string
	api          *httptest.Server
	detail       string // the repo detail the API returns; "" answers 500
}

func newImportCmdProject(t *testing.T) *importCmdProject {
	t.Helper()
	if testing.Short() {
		t.Skip("short: initializes git repositories")
	}
	p := &importCmdProject{detail: `{"visibility":"private","access_level":"member"}`}
	p.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli/repos/"+e2eRepoID || p.detail == "" {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(p.detail))
	}))
	t.Cleanup(p.api.Close)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "HOME"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("SAGEOX_ENDPOINT", p.api.URL)
	t.Setenv("SAGEOX_DAEMON", "false")
	t.Setenv("OX_SESSION_RECORDING", "")

	p.root = t.TempDir()
	runGit(t, p.root, "init", "-q")
	require.NoError(t, config.SaveProjectConfig(p.root, &config.ProjectConfig{RepoID: e2eRepoID, Endpoint: p.api.URL, TeamName: e2eTeam}))
	p.ledger = t.TempDir()
	runGit(t, p.ledger, "init", "-q")
	require.NoError(t, config.SaveLocalConfig(p.root, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: p.ledger}}))
	require.NoError(t, auth.SaveTokenForEndpoint(p.api.URL, &auth.StoredToken{
		AccessToken: "test-token", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour),
	}))
	t.Chdir(p.root)
	return p
}

func decodeRefusal(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
	return got
}

// Every refusal stops the run before a single session is read or summarized,
// and names itself with a code an AI coworker can act on.
//
// Failure prevented: an import that half-runs against a Ledger it cannot
// use, under rules it cannot apply, or for a coworker who turned recording
// off; or a refusal an AI coworker cannot tell apart from success.
func TestImportCommandRefusesBeforeReadingSessions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, p *importCmdProject)
		args  []string
		want  string
	}{
		{name: "an unknown tool", args: []string{"--agent", "gemini"}, want: importErrBadFlag},
		{name: "an unknown summarizer", args: []string{"--summarizer", "gpt"}, want: importErrBadFlag},
		{name: "a window that is not one", args: []string{"--since", "yesterday"}, want: importErrBadFlag},
		{name: "a session prefix too short to be unique", args: []string{"--session", "5b1d"}, want: importErrBadFlag},
		{name: "a directory that is not a SageOx project", setup: func(t *testing.T, _ *importCmdProject) {
			other := t.TempDir()
			runGit(t, other, "init", "-q")
			t.Chdir(other)
		}, want: importErrNotInitialized},
		{name: "a Ledger that is not cloned here", setup: func(t *testing.T, p *importCmdProject) {
			require.NoError(t, os.RemoveAll(filepath.Join(p.ledger, ".git")))
		}, want: importErrNoLedger},
		{name: "a coworker who is not logged in", setup: func(t *testing.T, _ *importCmdProject) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		}, want: importErrNotLoggedIn},
		{name: "recording turned off", setup: func(t *testing.T, _ *importCmdProject) {
			t.Setenv("OX_SESSION_RECORDING", "disabled")
		}, want: importErrRecordingDisabled},
		{name: "redaction rules that do not compile", setup: func(t *testing.T, p *importCmdProject) {
			require.NoError(t, os.WriteFile(filepath.Join(p.root, ".sageox", "REDACT.md"), []byte("```redact\nregex \"ACME-[\" -> [X]\n```\n"), 0o644))
		}, want: importErrRedactionRules},
		{name: "a destination SageOx cannot confirm", setup: func(_ *testing.T, p *importCmdProject) { p.detail = "" }, want: importErrUnverified},
		{name: "read-only access to the Ledger", setup: func(_ *testing.T, p *importCmdProject) {
			p.detail = `{"visibility":"private","access_level":"viewer"}`
		}, want: importErrReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newImportCmdProject(t)
			if tt.setup != nil {
				tt.setup(t, p)
			}
			root, out := importCommand(append([]string{"--json"}, tt.args...)...)
			err := root.Execute()
			require.ErrorIs(t, err, cli.ErrSilent)
			got := decodeRefusal(t, out)
			assert.Equal(t, "refused", got["status"])
			assert.Equal(t, tt.want, got["error"], got["message"])
			assert.Equal(t, tt.want, errkind.DetailOf(err), "usage telemetry learns the same code")
		})
	}
}

// A preview needs no confirmed destination and never uploads; it reports
// what it could learn about where the sessions would go.
func TestImportCommandPreviews(t *testing.T) {
	t.Run("with the destination confirmed", func(t *testing.T) {
		p := newImportCmdProject(t)
		p.detail = `{"visibility":"public","access_level":"member"}`
		root, out := importCommand("--json", "--dry-run")
		require.NoError(t, root.Execute())
		var got importJSONOutput
		require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
		assert.Equal(t, "preview", got.Status)
		assert.Equal(t, e2eTeam, got.Destination.Team)
		assert.Equal(t, "public", got.Destination.Visibility)
		assert.Empty(t, got.Sessions)
	})
	t.Run("when SageOx cannot be reached", func(t *testing.T) {
		p := newImportCmdProject(t)
		p.detail = ""
		root, out := importCommand("--json", "--dry-run")
		require.NoError(t, root.Execute())
		var got importJSONOutput
		require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
		assert.Equal(t, "unknown", got.Destination.Visibility)
	})
	t.Run("a refusal in text goes to stderr, with what to do", func(t *testing.T) {
		newImportCmdProject(t)
		t.Setenv("OX_SESSION_RECORDING", "disabled")
		root, out := importCommand("--json=false")
		var err error
		stderr := captureStderr(t, func() { err = root.Execute() })
		require.ErrorIs(t, err, cli.ErrSilent)
		assert.Contains(t, stderr, "session recording is disabled for this repo")
		assert.Contains(t, stderr, "ox config set session_recording auto")
		assert.Empty(t, out.String(), "stdout stays clean")
	})
	t.Run("an AI coworker gets JSON without asking for it", func(t *testing.T) {
		newImportCmdProject(t)
		t.Setenv("CLAUDECODE", "1")
		root, out := importCommand("--dry-run", "--since", "7d")
		require.NoError(t, root.Execute())
		var got importJSONOutput
		require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
		assert.Equal(t, "preview", got.Status)
	})
}

func TestParseImportSince(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{in: "7d", want: now.Add(-7 * 24 * time.Hour)},
		{in: "48h", want: now.Add(-48 * time.Hour)},
		{in: "90m", want: now.Add(-90 * time.Minute)},
		{in: "2026-09-01", want: time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)},
		{in: "0d", wantErr: true},
		{in: "-5h", wantErr: true},
		{in: "yesterday", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseImportSince(tt.in, now)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.True(t, tt.want.Equal(got), "got %s want %s", got, tt.want)
		})
	}
	for in, want := range map[string]nativeimport.Agent{"claude-code": nativeimport.AgentClaude, " CODEX ": nativeimport.AgentCodex, "": ""} {
		got, ok := parseImportAgent(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
}

// A session the Ledger holds only in part stays grouped under its label, and
// the text preview still says, per session, what the Ledger lacks.
//
// Failure prevented: the text preview said "already imported, continued since"
// without saying whether the continuation is anywhere in the Ledger.
func TestImportTextPreviewSaysWhatTheLedgerLacks(t *testing.T) {
	cand := func(id string, state importState, reason string) *importCandidate {
		return &importCandidate{Session: nativeimport.Session{Agent: nativeimport.AgentCodex, NativeID: id}, State: state, Reason: reason}
	}
	cands := []*importCandidate{
		cand(e2eCodexA, stateAlreadyImported, "continued after it was imported; the later part is not in the Ledger"),
		cand(e2eCodexB, stateRecordedLive, "ox recorded it only from 2026-09-10 10:00 UTC; the part before that is not in the Ledger"),
		cand(e2eClaudeA, stateAlreadyImported, ""),
	}
	var out bytes.Buffer
	require.NoError(t, renderImportPreview(&out, importOptions{}, importDestination{Team: e2eTeam}, cands, importIgnored{}, true))
	text := out.String()
	assert.Contains(t, text, "  already imported, continued since (1)\n    "+nativeShortID(e2eCodexA)+": continued after it was imported; the later part is not in the Ledger\n")
	assert.Contains(t, text, "  recorded live by ox, not from its start (1)\n    "+nativeShortID(e2eCodexB)+": ox recorded it only from")
	assert.Contains(t, text, "  already imported (1)\n", "a session held in full needs no note")
	assert.NotContains(t, text, nativeShortID(e2eClaudeA)+":")
}

// The preview's skip labels and counts are what a coworker reads to decide;
// each state must say what it means.
func TestImportPreviewLabels(t *testing.T) {
	label := func(state importState, reason string) string {
		return skipLabel(&importCandidate{State: state, Reason: reason})
	}
	assert.Equal(t, "already imported", label(stateAlreadyImported, ""))
	assert.Equal(t, "already imported, continued since", label(stateAlreadyImported, "continued after it was imported"))
	assert.Equal(t, "recorded live by ox", label(stateRecordedLive, ""))
	assert.Equal(t, "recorded live by ox, not from its start", label(stateRecordedLive, "ox recorded it only from …"))
	assert.Equal(t, "in progress", label(stateInProgress, "changed in the last 30 minutes"))
	assert.Equal(t, "needs summarizer", label(stateNeedsSummarizer, ""))
	assert.Equal(t, "no conversation", label(stateIneligible, "no conversation"))
	assert.Equal(t, "kept local", label(stateNotShared, "kept local: exploratory"))
	assert.Equal(t, "not selected", label(stateReady, ""))
	assert.Equal(t, "someday", label(importState("someday"), ""))

	assert.Equal(t, "1 other folders · 2 ox runs · 3 subagent threads · 4 Codex internal threads · 5 unreadable",
		ignoredLine(importIgnored{OtherFolders: 1, OxRuns: 2, SubagentThreads: 3, InternalThreads: 4, Unreadable: 5}))
	assert.Equal(t, "none needed", summarizerLine(nil))
	assert.Equal(t, "the team's Ledger", ledgerLabel(importDestination{}))
	assert.Equal(t, e2eTeam+"'s Ledger", ledgerLabel(importDestination{Team: e2eTeam}))
	assert.Equal(t, "default model", summarizerModelLabel(nativeimport.AgentCodex))
	assert.Equal(t, "abc", nativeShortID("abc"))
	assert.Equal(t, "invalid_flag: bad", importFailure{Code: importErrBadFlag, Message: "bad"}.Error())
}

// Failure prevented: the import writing to a Ledger path derived from the
// working directory instead of the project's configured one.
func TestConfiguredLedgerPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	assert.Empty(t, configuredLedgerPath(root), "no project, no Ledger")

	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: e2eRepoID, Endpoint: "https://sageox.ai"}))
	pctx, err := config.LoadProjectContext(root)
	require.NoError(t, err)
	assert.Equal(t, pctx.DefaultLedgerPath(), configuredLedgerPath(root), "the project's default without a local setting")

	custom := t.TempDir()
	require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: custom}}))
	assert.Equal(t, custom, configuredLedgerPath(root))
}
