package main

// agent_prime_current_kb_test.go — tests for the resolveCurrentKBEntry
// helper extracted from runAgentPrime. The helper is the unit boundary that
// runAgentPrime calls to populate output.CurrentKB — testing it directly
// avoids spinning up the full prime pipeline (auth, daemon, instance store)
// just to assert one field is populated.

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/prime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeKBBindingYAML writes a minimal .sageox/config.yaml carrying the given
// kb_id. Returns the project root so the caller can pass it as cwd.
func writeKBBindingYAML(t *testing.T, kbID string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	body := "kb_id: " + kbID + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.yaml"), []byte(body), 0o644))
	return root
}

func TestResolveCurrentKBEntry_Table(t *testing.T) {
	tests := []struct {
		name         string
		cwd          func(t *testing.T) string
		kbList       []prime.KBInfo
		wantNil      bool
		wantKBID     string
		wantSlug     string
		wantType     string
		wantLogParts []string
	}{
		{
			name: "current kb present",
			cwd: func(t *testing.T) string {
				return writeKBBindingYAML(t, "kb_test123")
			},
			kbList: []prime.KBInfo{
				{KBID: "kb_other", Type: "team", Slug: "platform"},
				{KBID: "kb_test123", Type: "personal", Slug: "personal-abc", Name: "Ryan's Personal"},
			},
			wantKBID: "kb_test123",
			wantSlug: "personal-abc",
			wantType: "personal",
		},
		{
			name: "outside tree",
			cwd: func(t *testing.T) string {
				return t.TempDir()
			},
			kbList:  []prime.KBInfo{{KBID: "kb_anything", Type: "team", Slug: "platform"}},
			wantNil: true,
		},
		{
			name: "binding not in list warns",
			cwd: func(t *testing.T) string {
				return writeKBBindingYAML(t, "kb_revoked")
			},
			kbList:       []prime.KBInfo{{KBID: "kb_other", Type: "team", Slug: "platform"}},
			wantNil:      true,
			wantLogParts: []string{"current_kb_not_in_list", "kb_revoked"},
		},
		{
			name:    "empty cwd",
			cwd:     func(t *testing.T) string { return "" },
			kbList:  []prime.KBInfo{{KBID: "kb_x"}},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := slog.Default()
			t.Cleanup(func() { slog.SetDefault(prev) })

			buf := &bytes.Buffer{}
			if len(tt.wantLogParts) > 0 {
				slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			}

			got := resolveCurrentKBEntry(tt.cwd(t), tt.kbList)
			if tt.wantNil {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, tt.wantKBID, got.KBID)
				assert.Equal(t, tt.wantSlug, got.Slug)
				assert.Equal(t, tt.wantType, got.Type)
			}
			for _, want := range tt.wantLogParts {
				assert.Contains(t, buf.String(), want)
			}
		})
	}
}
