package prime

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestWithAttributionGuidance(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		loggedIn bool
		attr     config.ResolvedAttribution
		contains []string
		absent   []string
	}{
		{
			name:     "logged in with commit attribution",
			content:  "base content",
			loggedIn: true,
			attr: config.ResolvedAttribution{
				Commit: "Co-Authored-By: SageOx <ox@sageox.ai>",
				PR:     "Co-Authored-By: [SageOx](https://github.com/SageOx)",
			},
			contains: []string{
				"base content",
				"SageOx Attribution",
				"Attribution is **conditional**",
				"Real-Time Insight Attribution",
				"Plan Footer",
				"SageOx Contribution Score (report only when commit attribution is configured; `none` is a valid, common answer)",
				"Commit Attribution (conditional — the hook adds it only when your reported score meets the threshold)",
				"PR Attribution (Conditional)",
				"ox session score",
				// the Co-Authored-By trailer must slot ABOVE the per-session
				// SageOx-Session last line (emitted in <session-context>)
				"above the SageOx-Session line when present",
			},
			// the old markdown-section instruction is gone: a templated
			// "<session_url>" placeholder was the confabulation vector, and a
			// mid-body section dies on squash-merge — replaced by the
			// exact-literal Session.PRDirective in prime output
			absent: []string{"Not Logged In", "Session Recording in PRs", "<session_url>"},
		},
		{
			name:     "not logged in shows warning",
			content:  "",
			loggedIn: false,
			attr:     config.ResolvedAttribution{},
			contains: []string{"Not Logged In", "not logged in to SageOx"},
			absent:   []string{"Commit Attribution", "Contribution Score"},
		},
		{
			name:     "no commit attribution omits config-gated blocks",
			content:  "",
			loggedIn: true,
			attr:     config.ResolvedAttribution{},
			contains: []string{"Real-Time Insight Attribution", "Plan Footer"},
			absent:   []string{"Commit Attribution", "Contribution Score", "PR Attribution"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WithAttributionGuidance(tt.content, tt.loggedIn, tt.attr)
			for _, c := range tt.contains {
				assert.Contains(t, got, c)
			}
			for _, a := range tt.absent {
				assert.NotContains(t, got, a)
			}
		})
	}
}

func TestBuildAttributionTextSection(t *testing.T) {
	tests := []struct {
		name     string
		attr     config.ResolvedAttribution
		contains []string
		absent   []string
	}{
		{
			name: "all fields populated",
			attr: config.ResolvedAttribution{
				Plan:   "Guided by SageOx",
				Commit: "Co-Authored-By: SageOx <ox@sageox.ai>",
				PR:     "Co-Authored-By: SageOx",
			},
			contains: []string{"Plans", "Commits", "PRs", "Co-Authored-By: SageOx <ox@sageox.ai>"},
		},
		{
			name:     "empty attribution",
			attr:     config.ResolvedAttribution{},
			contains: []string{"Attribution", "When this guidance influences your work"},
			absent:   []string{"Plans", "Commits", "PRs"},
		},
		{
			name: "only commit",
			attr: config.ResolvedAttribution{
				Commit: "Co-Authored-By: SageOx <ox@sageox.ai>",
			},
			contains: []string{"Commits"},
			absent:   []string{"Plans", "PRs"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildAttributionTextSection(tt.attr)
			for _, c := range tt.contains {
				assert.Contains(t, got, c)
			}
			for _, a := range tt.absent {
				assert.NotContains(t, got, a)
			}
		})
	}
}

// TestAttributionGuidanceStatesBothHalvesOfThePRContract pins the drift that
// shipped a PR with the SageOx-Session trailer and no `ox pr header` credit
// line: the markdown guidance here gained the header directive while the XML
// renderer's hand-maintained sibling block (cmd/ox/agent_prime_xml.go) did not,
// and XML is what a Claude Code agent actually reads.
//
// A PR body has two attribution ends. The trailer rides session state and is
// re-emitted on every prime; the header is taught once, in guidance. If only
// one end is stated, only one end ships — which is exactly what happened.
func TestAttributionGuidanceStatesBothHalvesOfThePRContract(t *testing.T) {
	t.Parallel()

	got := WithAttributionGuidance("", true, config.ResolvedAttribution{
		Commit: "Co-Authored-By: SageOx <ox@sageox.ai>",
		PR:     "Co-Authored-By: SageOx",
	})

	for _, want := range []string{
		"ox pr header",    // the top-of-body half
		"SageOx-Session:", // the bottom-of-body half
		"--plan",          // how a saved plan gets linked from the header
	} {
		if !strings.Contains(got, want) {
			t.Errorf("attribution guidance is missing %q — a PR authored from it can only carry half the contract", want)
		}
	}
}
