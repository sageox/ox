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
