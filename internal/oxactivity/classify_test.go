package oxactivity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func bashInput(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

// --- A. ox CLI through shell tools ---
// Failure prevented: the activity line names the wrong action (or none) for
// the ox commands AI coworkers actually run, in the shapes they actually type.
func TestClassify_OxCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		want    string
	}{
		{"ox agent prime", "loaded team context"},
		{"ox agent team-ctx", "loaded team context"},
		{`ox murmur --topic=wip "refactoring auth middleware"`, "posted murmur"},
		{`ox murmur --topic wip "refactoring auth"`, "posted murmur"},
		{"ox murmur list", "checked murmurs"},
		{"ox murmur list wip --json", "checked murmurs"},
		{"ox plan save --file plan.html", "saved plan"},
		{"ox plan render --file plan.html", "rendered plan"},
		{"ox plan view my-plan", "pulled plan"},
		{"ox plan enrich", "enriched plan"},
		{`ox query "how do we handle auth?"`, "searched team memory"},
		{`ox code search "func Load"`, "searched code index"},
		{"ox code defs Classify", "searched code index"},
		{"ox code callers Classify", "searched code index"},
		{"ox code callees Classify --depth 2", "searched code index"},
		{"ox code refs Classify", "searched code index"},
		{"ox code log internal/config", "searched code index"},
		{"ox code insights", "checked code hotspots"},
		{"ox session list", "read sessions"},
		{"ox session view 2026-10-01-foo", "read sessions"},
		{"ox conversation show cnv_123", "read discussion"},
		{`ox conversation search "pricing"`, "read discussion"},
		{"ox conversation transcript cnv_123", "read discussion"},
		{"ox conversation topics cnv_123", "read discussion"},
		{"ox coworker load reviewer", "loaded coworker reviewer"},
		{"ox decision enrich --topic auth", "checked decisions"},
		{"ox agent session stop", "stopped recording"},
		{"ox agent OxA1b2 session stop", "stopped recording"},
		{"ox doctor", "ran ox doctor"},
		{"ox status --json", "ran ox status"},
		{"ox murmur status", "ran ox murmur"},
		{"ox agent OxA1b2 session import", "ran ox agent"},
		{"/usr/local/bin/ox query x", "searched team memory"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()
			got, ok := Classify("Bash", bashInput(tt.command))
			assert.True(t, ok)
			assert.Equal(t, tt.want, got.Label)
		})
	}
}

// --- B. Shell prefixes and tails ---
// Failure prevented: an AI coworker's habitual `cd x &&`, env prefix, or
// `2>&1 | head` hides ox work from the line — or a pipe into ox is credited.
func TestClassify_ShellPrefixesAndTails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    string // "" means not an activity
	}{
		{"cd prefix", "cd /repo && ox agent prime", "loaded team context"},
		{"quoted cd", `cd "/my repo" && ox query auth`, "searched team memory"},
		{"two cds", "cd a; cd b && ox code insights", "checked code hotspots"},
		{"env assignment", "OX_DEBUG=1 ox query auth", "searched team memory"},
		{"env command", "env NO_COLOR=1 FOO=bar ox code search x", "searched code index"},
		{"cd then env", "cd /repo && NO_COLOR=1 ox murmur list", "checked murmurs"},
		{"stderr merge and pipe", "ox agent prime 2>&1 | head -50", "loaded team context"},
		{"redirect to file", "ox code insights > /tmp/out.txt", "checked code hotspots"},
		{"redirect before subcommand", "ox 2>/dev/null query auth", "searched team memory"},
		{"&> redirect", "ox doctor &> log.txt", "ran ox doctor"},
		{"quoted operators stay content", `ox murmur "fixing a && b | c"`, "posted murmur"},
		{"line continuation", "ox plan \\\n save --file p.html", "saved plan"},
		{"pipe into ox is not ox first", "echo hi | ox murmur", ""},
		{"other command first", "make build && ox doctor", ""},
		{"not ox", "go test ./...", ""},
		{"ox-like binary", "oxide build", ""},
		{"bare ox", "ox", ""},
		{"only flags", "ox --version", ""},
		{"only cd", "cd /repo", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Classify("Bash", bashInput(tt.command))
			if tt.want == "" {
				assert.False(t, ok, "got %q", got.Label)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, tt.want, got.Label)
		})
	}
}

// --- C. ox plumbing is not an activity ---
// Failure prevented: every heartbeat and hook dispatch overwrites the real
// action, so the line only ever says "ran ox agent".
func TestClassify_PlumbingExcluded(t *testing.T) {
	t.Parallel()

	for _, cmd := range []string{
		"ox agent OxA1b2 heartbeat",
		"ox agent heartbeat",
		"ox agent hook PostToolUse",
		"ox agent OxA1b2 hook Stop",
		"cd /repo && ox agent OxA1b2 heartbeat 2>&1",
	} {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			_, ok := Classify("Bash", bashInput(cmd))
			assert.False(t, ok)
		})
	}
}

// --- D. Harness tool shapes ---
// Failure prevented: Codex calls (exec_command/cmd, argv command arrays) are
// never recognized, so Codex coworkers see nothing.
func TestClassify_HarnessToolShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{"claude bash", "Bash", `{"command":"ox agent prime","description":"prime"}`, "loaded team context"},
		{"codex exec_command", "exec_command", `{"cmd":"ox agent prime","workdir":"/tmp"}`, "loaded team context"},
		{"codex shell argv", "shell", `{"command":["bash","-lc","cd /r && ox query auth"]}`, "searched team memory"},
		{"codex local_shell argv direct", "local_shell", `{"command":["ox","code","insights"]}`, "checked code hotspots"},
		{"cursor shell", "Shell", `{"command":"ox murmur list"}`, "checked murmurs"},
		{"non-shell tool", "Read", `{"command":"ox agent prime"}`, ""},
		{"malformed json", "Bash", `{"command":`, ""},
		{"json not object", "Bash", `"ox agent prime"`, ""},
		{"empty input", "Bash", ``, ""},
		{"missing command", "Bash", `{"description":"x"}`, ""},
		{"command wrong type", "Bash", `{"command":42}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Classify(tt.tool, json.RawMessage(tt.input))
			if tt.want == "" {
				assert.False(t, ok, "got %q", got.Label)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, tt.want, got.Label)
		})
	}
}

// --- E. SageOx MCP tools ---
// Failure prevented: MCP-driven ox work (claude.ai connector, Codex MCP) is
// invisible, or another vendor's MCP tool is credited to ox.
func TestClassify_MCPTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tool string
		want string
	}{
		{"mcp__claude_ai_sageox__Prime", "loaded team context"},
		{"mcp__claude_ai_sageox__PublishMurmur", "posted murmur"},
		{"mcp__claude_ai_sageox__ListMurmurs", "checked murmurs"},
		{"mcp__claude_ai_sageox__SavePlan", "saved plan"},
		{"mcp__claude_ai_sageox__RevisePlan", "saved plan"},
		{"mcp__claude_ai_sageox__GetPlan", "pulled plan"},
		{"mcp__claude_ai_sageox__EnrichPlan", "enriched plan"},
		{"mcp__claude_ai_sageox__Search", "searched team memory"},
		{"mcp__claude_ai_sageox__Conversation", "read discussion"},
		{"mcp__claude_ai_sageox__ConversationDigest", "read discussion"},
		{"mcp__claude_ai_sageox__ConversationTranscript", "read discussion"},
		{"mcp__sageox__Prime", "loaded team context"},
		{"mcp__SageOx__Search", "searched team memory"},
		{"mcp__claude_ai_sageox__ListBubbles", "used SageOx ListBubbles"},
		{"mcp__github__search_code", ""},
		{"mcp__notsage__Prime", ""},
		{"mcp__sageox__", ""},
		{"mcp__sageox", ""},
		{"Prime", ""},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			t.Parallel()
			got, ok := Classify(tt.tool, json.RawMessage(`{}`))
			if tt.want == "" {
				assert.False(t, ok, "got %q", got.Label)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, tt.want, got.Label)
		})
	}
}

// TestClassify_UntrustedNamesAreSanitized: a coworker name or subcommand is
// echoed into the label, so it must not carry escapes or unbounded length.
// Failure prevented: `ox coworker load $'\e[2J'` clears the harness screen.
func TestClassify_UntrustedNamesAreSanitized(t *testing.T) {
	t.Parallel()

	got, ok := Classify("Bash", bashInput("ox coworker load \"a\x1b[2Jb\""))
	assert.True(t, ok)
	assert.Equal(t, "loaded coworker a[2Jb", got.Label)
	assert.NotContains(t, got.Label, "\x1b")

	got, ok = Classify("Bash", bashInput("ox \"WEIRD\x1bsub\""))
	assert.True(t, ok)
	assert.Equal(t, "ran ox", got.Label)

	got, ok = Classify("mcp__sageox__Bad\x1bTool", json.RawMessage(`{}`))
	assert.True(t, ok)
	assert.NotContains(t, got.Label, "\x1b")
}

// TestClassify_ShellQuotingAndRedirects: the shell splitter has to honor
// quoting and redirections the way a shell does, or operators inside an
// argument split the line and a redirect target is read as a subcommand.
func TestClassify_ShellQuotingAndRedirects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    string // "" means not an activity
	}{
		{"single-quoted operators stay content", `ox murmur 'a; b && c | d'`, "posted murmur"},
		{"escaped quote inside double quotes", `ox murmur "say \"hi\" && go"`, "posted murmur"},
		{"escaped space joins a word", `ox murmur fixing\ things`, "posted murmur"},
		{"stderr to file", "ox doctor 2>err.txt", "ran ox doctor"},
		{"append redirect", "ox code insights >> out.txt", "checked code hotspots"},
		{"stdout to stderr", "ox doctor >&2", "ran ox doctor"},
		{"or chain", "false || ox doctor", ""},
		{"and chain after ox", "ox doctor && make build", "ran ox doctor"},
		{"assignment-only segment", "FOO=bar; ox doctor", "ran ox doctor"},
		{"pushd prefix", "pushd /repo && ox doctor", "ran ox doctor"},
		{"background then ox", "sleep 1 & ox doctor", ""},
		{"newline separated", "cd /repo\nox doctor", "ran ox doctor"},
		{"unterminated single quote", "ox murmur 'oops", "posted murmur"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Classify("Bash", bashInput(tt.command))
			if tt.want == "" {
				assert.False(t, ok, "got %q", got.Label)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, tt.want, got.Label)
		})
	}
}

// TestClassify_MCPToolNameIsOnlyEscapes: a SageOx tool name made of nothing
// but control characters must not become an empty "used SageOx " label.
func TestClassify_MCPToolNameIsOnlyEscapes(t *testing.T) {
	t.Parallel()
	_, ok := Classify("mcp__sageox__\x1b\x07\u200b", json.RawMessage(`{}`))
	assert.False(t, ok)
}
