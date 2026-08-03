package prime

import (
	"testing"

	"github.com/sageox/ox/internal/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGuidance(t *testing.T) {
	tests := []struct {
		name           string
		params         GuidanceParams
		wantCommands   []string // partial command strings we expect to find
		noCommands     []string // command strings we expect NOT to find
		wantMinEntries int
	}{
		{
			name: "minimal — no team context, no ledger, no code db",
			params: GuidanceParams{
				AgentID:  "test-1",
				RepoSlug: "test/repo",
			},
			wantCommands:   []string{"ox doctor", "ox status", "ox teams", "ox code index"},
			noCommands:     []string{"ox session list", "ox agent team-ctx", "ox code search"},
			wantMinEntries: 4,
		},
		{
			name: "with team context and ledger",
			params: GuidanceParams{
				AgentID:  "test-2",
				RepoSlug: "org/project",
				TeamCtx:  &TeamContextInfo{TeamID: "team-1", TeamName: "My Team"},
				Ledger:   &LedgerInfo{Exists: true, Path: "/tmp/ledger"},
			},
			wantCommands:   []string{"ox agent team-ctx", "ox session list", "ox query"},
			wantMinEntries: 6,
		},
		{
			name: "with code db available",
			params: GuidanceParams{
				AgentID:      "test-3",
				RepoSlug:     "org/repo",
				CodeDBExists: true,
			},
			wantCommands: []string{"ox code search", "ox code insights"},
			noCommands:   []string{"ox code index"},
		},
		{
			name: "with coworkers",
			params: GuidanceParams{
				AgentID:  "test-4",
				RepoSlug: "org/repo",
				TeamCtx: &TeamContextInfo{
					TeamID: "team-1",
					Coworkers: []claude.Agent{
						{Name: "reviewer"},
						{Name: "writer"},
					},
				},
			},
			wantCommands: []string{"ox coworker load", "ox coworker list"},
		},
		{
			name: "memory enabled with guide",
			params: GuidanceParams{
				AgentID:       "test-5",
				RepoSlug:      "org/repo",
				MemoryEnabled: true,
				TeamCtx: &TeamContextInfo{
					TeamID:               "team-1",
					ObservationGuideHint: "/path/to/GUIDE.md",
				},
			},
			wantCommands: []string{"ox memory put"},
		},
		{
			name: "memory disabled — no memory command",
			params: GuidanceParams{
				AgentID:       "test-6",
				RepoSlug:      "org/repo",
				MemoryEnabled: false,
				TeamCtx: &TeamContextInfo{
					TeamID:               "team-1",
					ObservationGuideHint: "/path/to/GUIDE.md",
				},
			},
			noCommands: []string{"ox memory put"},
		},
		{
			name: "murmuring enabled — murmur command included",
			params: GuidanceParams{
				AgentID:          "test-7",
				RepoSlug:         "org/repo",
				MurmuringEnabled: true,
			},
			wantCommands: []string{"ox murmur"},
		},
		{
			name: "murmuring disabled — no murmur command",
			params: GuidanceParams{
				AgentID:          "test-8",
				RepoSlug:         "org/repo",
				MurmuringEnabled: false,
			},
			noCommands: []string{"ox murmur"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := BuildGuidance(tt.params)
			require.NotNil(t, g)
			assert.NotEmpty(t, g.Hint)

			if tt.wantMinEntries > 0 {
				assert.GreaterOrEqual(t, len(g.Commands), tt.wantMinEntries)
			}

			commandStrs := make([]string, len(g.Commands))
			for i, c := range g.Commands {
				commandStrs[i] = c.Command
			}

			for _, wc := range tt.wantCommands {
				found := false
				for _, cs := range commandStrs {
					if cs == wc || len(cs) > len(wc) && cs[:len(wc)] == wc {
						found = true
						break
					}
				}
				if !found {
					// try contains match
					for _, cs := range commandStrs {
						if containsStr(cs, wc) {
							found = true
							break
						}
					}
				}
				assert.True(t, found, "expected command containing %q in %v", wc, commandStrs)
			}

			for _, nc := range tt.noCommands {
				for _, cs := range commandStrs {
					assert.NotContains(t, cs, nc, "unexpected command containing %q", nc)
				}
			}
		})
	}
}

// planCommandFor returns the Command string of the plan-enrichment
// IntentCommand BuildGuidance emits for a given agent type, or "" if absent.
func planCommandFor(t *testing.T, agentType string) string {
	t.Helper()
	g := BuildGuidance(GuidanceParams{AgentID: "p", RepoSlug: "org/repo", AgentType: agentType})
	require.NotNil(t, g)
	for _, c := range g.Commands {
		if c.Command == "ox plan" || c.Command == "ox plan list" {
			return c.Command
		}
	}
	return ""
}

// TestBuildGuidance_PlanEnrichmentTiering verifies the plan IntentCommand is
// tier-aware: Gold/Silver and the unknown baseline route to the active
// `ox plan`; Bronze routes to the lighter `ox plan list`.
// Failure prevented: a Bronze agent being promised a real-time enrichment
// nudge its lifecycle can't deliver, or an unknown agent losing the command.
func TestBuildGuidance_PlanEnrichmentTiering(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		want      string
	}{
		{"gold claude-code", "claude-code", "ox plan"},
		{"silver codex", "codex", "ox plan"},
		{"silver gemini", "gemini", "ox plan"},
		{"bronze amp", "amp", "ox plan list"},
		{"bronze opencode", "opencode", "ox plan list"},
		{"bronze pi", "pi", "ox plan list"},
		{"unknown baseline", "some-future-agent", "ox plan"},
		{"empty baseline", "", "ox plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, planCommandFor(t, tt.agentType),
				"agentType %q should route to %q", tt.agentType, tt.want)
		})
	}
}

func containsStr(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(haystack) > len(needle) && (haystack[:len(needle)] == needle ||
			haystack[len(haystack)-len(needle):] == needle ||
			findSubstr(haystack, needle)))
}

func findSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestBuildCapturePriorGuidance(t *testing.T) {
	agentID := "test-agent-42"
	g := BuildCapturePriorGuidance(agentID)

	require.NotNil(t, g)
	assert.Equal(t, "capture_prior_history", g.Action)
	assert.NotEmpty(t, g.Description)
	assert.NotEmpty(t, g.Instructions)
	assert.Contains(t, g.Example, agentID)
	assert.Contains(t, g.Example, "capture-prior")

	// verify instructions mention the agent ID
	found := false
	for _, instr := range g.Instructions {
		if containsStr(instr, agentID) {
			found = true
			break
		}
	}
	assert.True(t, found, "instructions should contain agent ID")
}

// TestBuildGuidance_KBRowsGatedOnHasKB verifies the `ox kb` rows appear only
// when the caller actually has a mounted bubble — an account with the KB
// feature off must pay zero tokens for commands that would return nothing.
//
// Failure prevented: ungating the rows (every prime carries dead commands)
// or dropping them (agents never learn `ox kb describe` exists, and the
// <knowledge-bubbles> block's instruction to run it dangles).
func TestBuildGuidance_KBRowsGatedOnHasKB(t *testing.T) {
	base := GuidanceParams{AgentID: "a1", RepoSlug: "test/repo"}

	off := BuildGuidance(base)
	for _, c := range off.Commands {
		assert.NotContains(t, c.Command, "ox kb",
			"no ox kb row expected when the caller has no bubbles")
	}

	withKB := base
	withKB.HasKB = true
	on := BuildGuidance(withKB)

	var cmds []string
	for _, c := range on.Commands {
		cmds = append(cmds, c.Command)
	}
	assert.Contains(t, cmds, "ox kb list")
	// quoted on purpose — an unquoted leading # is a shell comment
	assert.Contains(t, cmds, "ox kb describe '#<slug>'")
	for _, c := range cmds {
		assert.NotContains(t, c, "describe <#slug>",
			"the published describe command must quote the slug; a bare # is a shell comment")
	}
}
