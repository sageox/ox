package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: an import replays months of history in bulk. If the
// team's REDACT.md is unreadable, or the team context is not on this machine,
// the strict writer carries on without those rules; the import must refuse.
func TestValidateRedactPolicy(t *testing.T) {
	tests := []struct {
		name    string
		config  string // .sageox/config.json, empty for none
		redact  string // .sageox/REDACT.md, empty for none
		wantErr string
	}{
		{name: "no team and no rules is fine"},
		{name: "valid repo rules are fine", redact: "```redact\nregex \"ACME-[a-f0-9]{32}\" -> [REDACTED_ACME_KEY]\n```\n"},
		{name: "a rule that does not compile", redact: "```redact\nregex \"ACME-[\" -> [X]\n```\n", wantErr: "invalid regex"},
		{
			name:    "a team context that is not synced",
			config:  `{"repo_id":"repo_x","team_id":"team_x","team_name":"Acme Engineering"}`,
			wantErr: "team context for Acme Engineering is not synced",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no user-level REDACT.md from the real machine
			t.Setenv("SAGEOX_ENDPOINT", "")
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
			if tt.config != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(tt.config), 0o644))
			}
			if tt.redact != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "REDACT.md"), []byte(tt.redact), 0o644))
			}
			err := ValidateRedactPolicy(root)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrRedactPolicyUnusable)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
