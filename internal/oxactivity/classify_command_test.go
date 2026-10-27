package oxactivity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassify_CommandAndKind pins the command words and kind the usage trace
// records for each ox call. Failure prevented: the usage report groups calls
// under the wrong command, counts a murmur post as a retrieval, or loses the
// lifecycle calls that bracket a session.
func TestClassify_CommandAndKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command   string
		label     string
		wantCmd   string
		wantKind  Kind
		wantQuery string
	}{
		{"ox agent prime", "loaded team context", "agent prime", KindLifecycle, ""},
		{"ox agent OxA1b2 prime --json", "loaded team context", "agent prime", KindLifecycle, ""},
		{"ox agent OxA1b2 team-ctx", "loaded team context", "agent team-ctx", KindRetrieval, ""},
		{"ox agent OxA1b2 session stop", "stopped recording", "agent session stop", KindLifecycle, ""},
		{"ox agent OxA1b2 session start", "ran ox agent", "agent session start", KindLifecycle, ""},
		{"ox agent OxA1b2 session pause", "ran ox agent", "agent session pause", KindLifecycle, ""},
		{"ox agent OxA1b2 session resume", "ran ox agent", "agent session resume", KindLifecycle, ""},
		{"ox agent OxA1b2 session context-trace", "reported context use", "agent session context-trace", KindWrite, ""},
		{"ox agent OxA1b2 context used Ox7f2a --why \"kept retry budget\"", "reported context use", "agent context used", KindWrite, ""},
		{"ox agent OxA1b2 session score", "scored session", "agent session score", KindWrite, ""},
		{"ox session score", "scored session", "session score", KindWrite, ""},
		{"ox brief --json", "checked ox brief", "brief", KindOther, ""},
		{"ox usage --json", "checked ox brief", "usage", KindOther, ""},
		{"ox murmur list --json", "checked murmurs", "murmur list", KindRetrieval, ""},
		{"ox agent OxA1b2 murmur list --json", "checked murmurs", "murmur list", KindRetrieval, ""},
		{`ox agent OxA1b2 query "auth"`, "searched team memory", "query", KindRetrieval, "auth"},
		{`ox agent OxA1b2 murmur --topic wip "hello"`, "posted murmur", "murmur", KindWrite, ""},
		{`ox murmur --topic=wip "refactoring auth"`, "posted murmur", "murmur", KindWrite, ""},
		{"ox murmur status", "ran ox murmur", "murmur status", KindOther, ""},
		{"ox plan save --file p.html", "saved plan", "plan save", KindWrite, ""},
		{"ox plan render --file p.html", "rendered plan", "plan render", KindWrite, ""},
		{"ox plan enrich", "enriched plan", "plan enrich", KindWrite, ""},
		{"ox plan view my-plan", "pulled plan", "plan view", KindRetrieval, ""},
		{"ox plan get my-plan", "pulled plan", "plan get", KindRetrieval, ""},
		{`ox query "how do we handle auth?"`, "searched team memory", "query", KindRetrieval, "how do we handle auth?"},
		{`ox query --limit 5 "auth"`, "searched team memory", "query", KindRetrieval, "auth"},
		{`ox query --json "auth"`, "searched team memory", "query", KindRetrieval, "auth"},
		{`ox query auth --json`, "searched team memory", "query", KindRetrieval, "auth"},
		{`ox code search "func Load" type:symbol`, "searched code index", "code search", KindRetrieval, "func Load"},
		{"ox code defs Classify", "searched code index", "code defs", KindRetrieval, ""},
		{"ox code insights", "checked code hotspots", "code insights", KindRetrieval, ""},
		{"ox session list", "read sessions", "session list", KindRetrieval, ""},
		{"ox session view 2026-10-01-foo", "read sessions", "session view", KindRetrieval, ""},
		{"ox conversation search pricing", "read discussion", "conversation search", KindRetrieval, ""},
		{"ox decision enrich --topic auth", "checked decisions", "decision enrich", KindRetrieval, ""},
		{"ox coworker load reviewer", "loaded coworker reviewer", "coworker load", KindOther, ""},
		{"ox doctor", "ran ox doctor", "doctor", KindOther, ""},
		{"ox agent OxA1b2 session import", "ran ox agent", "agent session", KindOther, ""},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()
			got, ok := Classify("Bash", bashInput(tt.command))
			require.True(t, ok)
			assert.Equal(t, tt.label, got.Label)
			assert.Equal(t, tt.wantCmd, got.Command)
			assert.Equal(t, tt.wantKind, got.Kind)
			assert.Equal(t, tt.wantQuery, got.Query)
		})
	}
}

// TestClassify_CommandNeverCarriesArguments posts murmurs and runs unknown
// subcommands with distinctive text. Failure prevented: a murmur body, a plan
// path, or any other argument leaks into the recorded command words or query
// (ADR-027 rule 4) — the trace syncs to the whole team.
func TestClassify_CommandNeverCarriesArguments(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-BODY-xyz"
	for _, command := range []string{
		`ox murmur --topic=wip "` + secret + `"`,
		`ox murmur ` + secret,
		`ox murmur --topic wip ` + secret,
		`ox agent OxA1b2 murmur "` + secret + `"`,
		`ox plan save --file ` + secret + `.html`,
		`ox doctor ` + secret,
		`ox ` + secret,
		`ox session view ` + secret,
		`ox coworker status ` + secret,
	} {
		got, ok := Classify("Bash", bashInput(command))
		require.True(t, ok, command)
		for field, v := range map[string]string{"command": got.Command, "query": got.Query, "label": got.Label} {
			assert.NotContains(t, strings.ToLower(v), strings.ToLower(secret), "%s leaked into %s", command, field)
		}
	}
}

// TestClassify_QueryIsCapped keeps a pasted wall of text out of the trace.
func TestClassify_QueryIsCapped(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 300) // multi-byte: the cap must not split a rune
	got, ok := Classify("Bash", bashInput(`ox query "`+long+`"`))
	require.True(t, ok)
	assert.LessOrEqual(t, len(got.Query), MaxQueryLen)
	assert.True(t, strings.HasPrefix(long, got.Query))
	assert.NotEmpty(t, got.Query)
}

// TestClassify_MCPCommandAndKind gives SageOx MCP tools the same kinds as
// their CLI twins. Failure prevented: an AI coworker on the MCP surface shows
// up in the usage report with no command or the wrong kind.
func TestClassify_MCPCommandAndKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tool     string
		label    string
		wantCmd  string
		wantKind Kind
	}{
		{"mcp__claude_ai_sageox__ListMurmurs", "checked murmurs", "mcp ListMurmurs", KindRetrieval},
		{"mcp__claude_ai_sageox__PublishMurmur", "posted murmur", "mcp PublishMurmur", KindWrite},
		{"mcp__sageox__Prime", "loaded team context", "mcp Prime", KindLifecycle},
		{"mcp__sageox__Search", "searched team memory", "mcp Search", KindRetrieval},
		{"mcp__sageox__ConversationChapters", "read discussion", "mcp ConversationChapters", KindRetrieval},
		{"mcp__sageox__ExpertiseMap", "used SageOx ExpertiseMap", "mcp ExpertiseMap", KindOther},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			t.Parallel()
			got, ok := Classify(tt.tool, []byte(`{"body":"SECRET-BODY-xyz"}`))
			require.True(t, ok)
			assert.Equal(t, tt.label, got.Label)
			assert.Equal(t, tt.wantCmd, got.Command)
			assert.Equal(t, tt.wantKind, got.Kind)
			assert.Empty(t, got.Query, "MCP arguments are never inspected")
		})
	}
}

// TestClassify_Ref names the item an item read pulled, and nothing else.
// Failure prevented: the status line counting one plan read twice as two
// plans (no ref), counting a search as an item, or copying free text (a
// murmur body, a query) into the per-session state.
func TestClassify_Ref(t *testing.T) {
	t.Parallel()
	const cnv = "cnv_019ffe10-0000-7000-8000-000000000011"
	tests := []struct {
		command string
		want    string
	}{
		{"ox plan view my-plan", "my-plan"},
		{"ox plan view --json my-plan", "my-plan"},
		{"ox agent OxA1b2 plan view my-plan", "my-plan"},
		{"ox plan show 2026-10-01-auth.v2", "2026-10-01-auth.v2"},
		{"ox plan view", ""},
		{"ox conversation show " + cnv, cnv},
		{"ox conversation topics " + cnv, cnv},
		{"ox conversation topic " + cnv + " tp_1", cnv},
		{`ox conversation transcript "sageox://` + cnv + `/transcript@3#cue=4-9"`, cnv},
		{"ox conversation show https://sageox.ai/c/rec_019ffe10-0000-7000-8000-000000000012", "cnv_019ffe10-0000-7000-8000-000000000012"},
		{"ox conversation show rec_019FFE10-0000-7000-8000-000000000012", "cnv_019ffe10-0000-7000-8000-000000000012"},
		{`ox conversation show "not an id at all"`, ""},
		{"ox conversation search pricing", ""},
		{`ox query "auth"`, ""},
		{"ox murmur list", ""},
		{`ox murmur --topic wip "my-plan"`, ""},
		{"ox session view 2026-10-01-foo", ""},
		{"ox agent OxA1b2 team-ctx", ""},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()
			got, ok := ClassifyCommand(tt.command)
			require.True(t, ok)
			assert.Equal(t, tt.want, got.Ref)
		})
	}
}
