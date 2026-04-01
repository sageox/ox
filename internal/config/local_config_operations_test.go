package config

import (
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
)

func TestDefaultLedgerPath(t *testing.T) {
	dataDir := paths.DataDir()
	tests := []struct {
		name        string
		repoID      string
		endpointURL string
		want        string
	}{
		{
			name:        "production endpoint",
			repoID:      "repo_01jfk3mab",
			endpointURL: "https://api.sageox.ai",
			want:        filepath.Join(dataDir, "sageox.ai", "ledgers", "repo_01jfk3mab"),
		},
		{
			name:        "staging endpoint",
			repoID:      "repo_01jfk3mab",
			endpointURL: "https://staging.sageox.ai",
			want:        filepath.Join(dataDir, "staging.sageox.ai", "ledgers", "repo_01jfk3mab"),
		},
		{
			name:        "empty repo ID",
			repoID:      "",
			endpointURL: "https://sageox.ai",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultLedgerPath(tt.repoID, tt.endpointURL)
			assert.Equal(t, tt.want, got, "DefaultLedgerPath(%q, %q)", tt.repoID, tt.endpointURL)
		})
	}
}
