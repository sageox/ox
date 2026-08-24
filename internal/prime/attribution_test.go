package prime

import (
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
