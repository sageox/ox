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
				"Real-Time Insight Attribution",
				"Plan Footer",
				"Contribution Score (Required)",
				"Commit Attribution (Automatic)",
				"PR Attribution (Conditional)",
				"ox session score",
				"Session Recording in PRs",
			},
			absent: []string{"Not Logged In"},
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
