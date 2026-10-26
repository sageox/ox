package lfs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/secretguard"
	"github.com/stretchr/testify/require"
)

func TestUploadSessionFilesMandatorySecretGuard(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		refreshFails  bool
	}{
		{"capture bypass", `{"content":"` + "AKIA" + "QWERTYUIOPASDFGH" + `"}`, false},
		{"split helm import", `helm template --set extraEnv[0].name=AWS_SECRET_ACCESS_KEY --set extraEnv[0].value=fabricated-session-secret-value`, false},
		{"fresh catalog required", `{"content":"ordinary notes"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useBundledSessionRules(t)
			if tc.refreshFails {
				currentSessionRules = func(context.Context) (*secretguard.RuleSet, error) { return nil, errors.New("catalog unavailable") }
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
			defer server.Close()
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(tc.content), 0600))
			refs, err := UploadSessionFilesContext(context.Background(), NewClient(server.URL, "testuser", "testtoken"), dir, nil)
			require.Error(t, err)
			require.Nil(t, refs)
			require.Zero(t, requests, "no upload action may be requested before a complete clean scan")
			data, readErr := os.ReadFile(filepath.Join(dir, "raw.jsonl"))
			require.NoError(t, readErr)
			require.Equal(t, tc.content, string(data), "held content must remain recoverable locally")
		})
	}
}

func TestUploadSessionFilesEnforcesNewUpstreamRule(t *testing.T) {
	useBundledSessionRules(t)
	rules, err := secretguard.NewRules([]byte("[[rules]]\nid='future-format'\nregex='future_[a-z]+'\n"))
	require.NoError(t, err)
	currentSessionRules = func(context.Context) (*secretguard.RuleSet, error) { return rules, nil }
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.md"), []byte("future_synthetic"), 0600))
	_, err = UploadSessionFilesContext(context.Background(), nil, dir, nil)
	require.ErrorContains(t, err, "publication refused")
}
