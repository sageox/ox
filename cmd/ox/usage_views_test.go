package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/usage"
)

// usageLedger points the project at a fresh Ledger checkout and returns it.
func usageLedger(t *testing.T, root string) string {
	t.Helper()
	ledgerPath := t.TempDir()
	require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledgerPath}}))
	return ledgerPath
}

// writeLedgerUsageSession writes sessions/<name>/meta.json into the Ledger.
func writeLedgerUsageSession(t *testing.T, ledgerPath, name string, mutate func(*lfs.SessionMeta)) string {
	t.Helper()
	dir := filepath.Join(ledgerPath, "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	meta := &lfs.SessionMeta{
		Version: "1.0", SessionName: name, Username: "Devon", UserID: "user_devon", AgentID: "OxDv01",
		AgentType: "codex", SessionID: "ses_" + name, CreatedAt: time.Now().Add(-time.Hour), Files: map[string]lfs.FileRef{},
	}
	if mutate != nil {
		mutate(meta)
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))
	return dir
}

// runOxUsage runs the real `ox session report` with args as the calling AI coworker.
func runOxUsage(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetUsageFlags(t)
	t.Setenv("SAGEOX_AGENT_ID", usageAgentID)
	return executeRootCLI(t, append([]string{"session", "report"}, args...)...)
}

// TestUsage_LedgerSessionView drives `ox session report --session <name> --json` over a
// real Ledger checkout. Failure prevented: a coworker reviewing a past session
// gets a report that hides why there is no trace (still recording, stub only,
// none written) — or one that silently hydrates the trace or drops its score.
func TestUsage_LedgerSessionView(t *testing.T) {
	const name = "2026-10-20T10-00-devon-OxDv01"
	tests := []struct {
		name          string
		setup         func(t *testing.T, dir string)
		meta          func(*lfs.SessionMeta)
		wantAvailable bool
		wantReason    string
	}{
		{
			name: "finished session with a trace reports its calls and score",
			meta: func(m *lfs.SessionMeta) {
				m.SageoxScoreCategory, m.SageoxScoreReason = "moderate", "followed the plan"
				m.ProducedCommits = []string{"abc123", "def456"}
			},
			setup: func(t *testing.T, dir string) {
				require.NoError(t, contexttrace.NewWriter(dir).AppendAll([]contexttrace.Event{
					{Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI, Label: "checked murmurs", Command: "murmur list", InlineTokens: 100},
				}))
			},
			wantAvailable: true,
		},
		{
			name:       "draft session is still recording",
			meta:       func(m *lfs.SessionMeta) { m.Draft = true },
			wantReason: "still recording",
		},
		{
			name: "stub-only trace is reported as not hydrated",
			setup: func(t *testing.T, dir string) {
				pointer := lfs.FormatPointer("sha256:"+strings.Repeat("a", 64), 1234)
				require.NoError(t, os.WriteFile(filepath.Join(dir, contexttrace.FileName), []byte(pointer), 0o644))
			},
			wantReason: "ox session view " + name + " --context",
		},
		{
			name:       "session without a trace says so",
			wantReason: "no context trace",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := usageTraceEnv(t, nil)
			dir := writeLedgerUsageSession(t, usageLedger(t, root), name, tt.meta)
			if tt.setup != nil {
				tt.setup(t, dir)
			}

			r := runOxUsageJSON(t, "--session", name)

			assert.Equal(t, name, r.Session.Name)
			assert.Equal(t, tt.wantAvailable, r.Trace.Available)
			if tt.wantReason != "" {
				assert.Contains(t, r.Trace.Reason, tt.wantReason)
				return
			}
			assert.Equal(t, usage.TraceSourceSession, r.Trace.Source)
			assert.Equal(t, "OxDv01", r.Session.AgentID)
			assert.Equal(t, "codex", r.Session.Harness)
			assert.Equal(t, 1, r.Calls.Total)
			assert.Equal(t, 2, r.Used.ProducedCommits)
			require.NotNil(t, r.Used.SageoxScore)
			assert.Equal(t, "moderate", r.Used.SageoxScore.Category)
		})
	}
}

// TestUsage_CommandErrors drives the real command's usage errors. Failure
// prevented: a bad flag or a missing session yields a confusing success, or a
// path-like --session name escapes the Ledger's sessions directory.
func TestUsage_CommandErrors(t *testing.T) {
	tests := []struct {
		name    string
		ledger  string // "" = a real Ledger; "missing" = configured but absent
		args    []string
		wantErr string
	}{
		{"session name with a path separator", "", []string{"--session", "../etc"}, "invalid session name"},
		{"session name dot-dot", "", []string{"--session", ".."}, "invalid session name"},
		{"session name dot", "", []string{"--session", "."}, "invalid session name"},
		{"session not in the Ledger", "", []string{"--session", "nope"}, `session "nope" not found`},
		{"session with no Ledger on disk", "missing", []string{"--session", "x"}, "ledger not found"},
		{"rollup with no Ledger on disk", "missing", []string{"--me"}, "ledger not found"},
		{"invalid --since", "", []string{"--me", "--since", "yesterday-ish"}, "invalid --since"},
		{"non-positive --limit", "", []string{"--me", "--limit", "0"}, "--limit must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := usageTraceEnv(t, nil)
			if tt.ledger == "missing" {
				require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{
					Ledger: &config.LedgerConfig{Path: filepath.Join(t.TempDir(), "gone")},
				}))
			} else {
				usageLedger(t, root)
			}

			_, err := runOxUsage(t, append([]string{"--json"}, tt.args...)...)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestUsage_OutsideProjectFails: with no resolvable project root, ox session report
// says so instead of reporting an empty session.
func TestUsage_OutsideProjectFails(t *testing.T) {
	usageTraceEnv(t, nil)
	chdirIntoRemovedDir(t)

	_, err := runOxUsage(t, "--json")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in a SageOx project")
}

// TestUsage_HumanAndAgentOutput drives every view in both output modes.
// Failure prevented: a coworker at a terminal gets JSON (or nothing), or an AI
// coworker that omits --json gets text it cannot parse.
func TestUsage_HumanAndAgentOutput(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantText string
	}{
		{"session view", nil, "no usage trace for this session"},
		{"team view", []string{"--team"}, ""},
		{"me view", []string{"--me"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name+" human", func(t *testing.T) {
			root := usageTraceEnv(t, nil)
			usageLedger(t, root)

			out, err := runOxUsage(t, append([]string{"--json=false"}, tt.args...)...)

			require.NoError(t, err)
			assert.NotEmpty(t, out)
			assert.False(t, json.Valid([]byte(out)), "a terminal gets text, not JSON: %s", out)
			if tt.wantText != "" {
				assert.Contains(t, out, tt.wantText)
			}
		})
		t.Run(tt.name+" json", func(t *testing.T) {
			root := usageTraceEnv(t, nil)
			usageLedger(t, root)

			out, err := runOxUsage(t, append([]string{"--json"}, tt.args...)...)

			require.NoError(t, err)
			assert.True(t, json.Valid([]byte(out)), "stdout must be one JSON document: %s", out)
		})
	}
}

// TestUsage_AgentContextDefaultsToJSON: an AI coworker that runs plain
// `ox session report` gets the JSON report without asking for it.
func TestUsage_AgentContextDefaultsToJSON(t *testing.T) {
	usageTraceEnv(t, nil)
	isolateAgentDetection(t)
	t.Setenv("CLAUDECODE", "1")

	out, err := runOxUsage(t)

	require.NoError(t, err)
	var r usage.Report
	require.NoError(t, json.Unmarshal([]byte(out), &r), out)
	assert.Equal(t, usage.ViewSession, r.View)
}

// TestUsage_CurrentSessionScoreAndIdentity covers the current-session inputs
// that do not come from the trace. Failure prevented: the SageOx score an AI
// coworker recorded is dropped from its report, or a coworker with no
// identity gets an empty report with no reason.
func TestUsage_CurrentSessionScoreAndIdentity(t *testing.T) {
	root := usageTraceEnv(t, nil)

	in := usageInputForCurrentSession(root, "")
	assert.Equal(t, usage.TraceSourceNone, in.TraceSource)
	assert.Contains(t, in.TraceReason, "no AI coworker identity")

	require.NoError(t, session.WriteSageoxScoreCategory(usageAgentID, session.ScoreSignificant, "used the plan"))
	r := runOxUsageJSON(t)
	require.NotNil(t, r.Used.SageoxScore)
	assert.Equal(t, "significant", r.Used.SageoxScore.Category)
	assert.Equal(t, "used the plan", r.Used.SageoxScore.Reason)
}

// TestWriteUsageReport_Errors: a payload that cannot be encoded, or a stdout
// that cannot be written, is an error — never a silent empty report.
func TestWriteUsageReport_Errors(t *testing.T) {
	tests := []struct {
		name    string
		w       failingWriter
		json    bool
		payload any
		wantErr string
	}{
		{"unencodable payload", failingWriter{}, true, map[string]any{"c": make(chan int)}, "unsupported type"},
		{"json write fails", failingWriter{}, true, map[string]int{"a": 1}, "write failed"},
		{"text write fails", failingWriter{}, false, nil, "write failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writeUsageReport(tt.w, tt.json, tt.payload, func() string { return "report\n" })
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestInvokedRefType names the kind of team knowledge each retrieval pulled.
// Failure prevented: `ox session report` lumps a code search or a plan read under the
// wrong kind, so the coworker misreads what ox put in front of the model.
func TestInvokedRefType(t *testing.T) {
	tests := []struct {
		command string
		kind    oxactivity.Kind
		want    string
	}{
		{"query", oxactivity.KindRetrieval, "query"},
		{"mcp Search", oxactivity.KindRetrieval, "query"},
		{"code search", oxactivity.KindRetrieval, "code"},
		{"murmur list", oxactivity.KindRetrieval, "murmur"},
		{"mcp ListMurmurs", oxactivity.KindRetrieval, "murmur"},
		{"plan show", oxactivity.KindRetrieval, "plan"},
		{"mcp GetPlan", oxactivity.KindRetrieval, "plan"},
		{"session list", oxactivity.KindRetrieval, "session"},
		{"conversation show", oxactivity.KindRetrieval, "discussion"},
		{"mcp ConversationDigest", oxactivity.KindRetrieval, "discussion"},
		{"decision enrich", oxactivity.KindRetrieval, "decision"},
		{"agent team-ctx", oxactivity.KindRetrieval, "team-context"},
		{"something else", oxactivity.KindRetrieval, ""},
		{"query", oxactivity.Kind(""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.command+"/"+string(tt.kind), func(t *testing.T) {
			assert.Equal(t, tt.want, invokedRefType(oxactivity.Activity{Command: tt.command, Kind: tt.kind}))
		})
	}
}

// TestUsageTraceWriter_Unwritable: when the trace destination cannot be
// resolved or created, the writer is nil and nothing panics — the hook must
// never fail an AI coworker's tool call over the usage trace.
func TestUsageTraceWriter_Unwritable(t *testing.T) {
	t.Run("no agent id", func(t *testing.T) {
		root := usageTraceEnv(t, nil)
		w, state := usageTraceWriter(root, "")
		assert.Nil(t, w)
		assert.Nil(t, state)
	})
	t.Run("cache dir blocked by a file", func(t *testing.T) {
		root := usageTraceEnv(t, nil)
		cache := filepath.Join(root, ".sageox", "cache")
		require.NoError(t, os.RemoveAll(cache))
		require.NoError(t, os.WriteFile(cache, []byte("not a dir"), 0o644))
		w, state := usageTraceWriter(root, usageAgentID)
		assert.Nil(t, w)
		assert.Nil(t, state)
	})
}

// TestPruneAgentTraceCache_Errors: pruning a missing or read-only cache is a
// no-op, never a panic or a hook failure.
func TestPruneAgentTraceCache_Errors(t *testing.T) {
	pruneAgentTraceCache(filepath.Join(t.TempDir(), "missing"), time.Now())

	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	old := filepath.Join(dir, usageAgentID+".jsonl")
	require.NoError(t, os.WriteFile(old, []byte("{}\n"), 0o644))
	stale := time.Now().Add(-2 * agentTraceCacheMaxAge)
	require.NoError(t, os.Chtimes(old, stale, stale))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	pruneAgentTraceCache(dir, time.Now())

	assert.FileExists(t, old, "a file the OS refuses to remove stays put")
}

// TestRecordPrimeInvocation_OutsideProject: a prime with no resolvable
// project root records nothing and never fails prime.
func TestRecordPrimeInvocation_OutsideProject(t *testing.T) {
	root := usageTraceEnv(t, nil)
	chdirIntoRemovedDir(t)

	recordPrimeInvocation(usageAgentID, &prime.ContextBudget{}, 100, 0, nil)

	assert.NoFileExists(t, filepath.Join(agentTraceCacheDir(root), usageAgentID+".jsonl"))
}

// chdirIntoRemovedDir leaves the process in a directory that no longer
// exists, so no project root can be resolved from the working directory.
func chdirIntoRemovedDir(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvProjectRoot, "")
	dir := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, os.Mkdir(dir, 0o755))
	t.Chdir(dir)
	require.NoError(t, os.Remove(dir))
	if _, err := os.Getwd(); err == nil {
		t.Skip("this platform still resolves a removed working directory")
	}
}

// TestPrimeFullBytes_MissingBundle: a deferred prime whose full bundle is gone
// reports no untrimmed size rather than a wrong one.
func TestPrimeFullBytes_MissingBundle(t *testing.T) {
	doc := "<ox-prime>\n\n<deferred path=\"/x\">\n- commands\n</deferred>\n</ox-prime>\n"
	assert.Equal(t, 0, primeFullBytes(doc, filepath.Join(t.TempDir(), "gone.xml")))
}
