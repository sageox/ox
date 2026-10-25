package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/attestpublication"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func attestBoundaryCommand(from, out string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String("from", from, "")
	cmd.Flags().String("out", out, "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("open", false, "")
	return cmd
}

func TestAttestPublishRejectsUnsafePathsAndMissingFlags(t *testing.T) {
	base := t.TempDir()
	for _, tc := range []struct{ name, from, out, message string }{
		{"missing source", "", base, "are required"},
		{"missing destination", base, "", "are required"},
		{"same directory", base, base, "must be outside the frozen export"},
		{"nested destination", base, filepath.Join(base, "output"), "must be outside the frozen export"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, runAttestPublish(attestBoundaryCommand(tc.from, tc.out), nil), tc.message)
		})
	}
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	require.Empty(t, entries, "invalid arguments must not create publication state")
}

func TestAttestPublishRequiresRepositoryAndAuthentication(t *testing.T) {
	for _, state := range []string{"outside repository", "not initialized", "signed out", "expired without refresh"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("SAGEOX_TOKEN", "")
			t.Setenv("SAGEOX_ENDPOINT", "https://attest-cli.invalid")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			project := t.TempDir()
			want := "find repository"
			if state != "outside repository" {
				project = testGitRepo(t)
				want = "run ox init first"
			}
			if state == "signed out" || state == "expired without refresh" {
				requireSageoxDir(t, project)
				require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{RepoID: "repo_attest_auth", Endpoint: "https://attest-cli.invalid"}))
				want = "sign in with ox login"
			}
			if state == "expired without refresh" {
				require.NoError(t, auth.SaveTokenForEndpoint("https://attest-cli.invalid", &auth.StoredToken{AccessToken: "expired-test-token", ExpiresAt: time.Now().Add(-time.Hour)}))
				want = "authenticate publication request"
			}
			t.Chdir(project)
			out := filepath.Join(t.TempDir(), "publication")
			require.ErrorContains(t, runAttestPublish(attestBoundaryCommand(t.TempDir(), out), nil), want)
			_, err := os.Stat(out)
			require.ErrorIs(t, err, os.ErrNotExist, "failed prerequisites must not create publication state")
		})
	}
}

func TestAttestPublishResultOutput(t *testing.T) {
	source := repoPath("..", "..", "internal", "attestpublication", "contract", "testdata", "conformance", "valid", "run-all-outcomes.json")
	raw, err := os.ReadFile(source)
	require.NoError(t, err)
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonOutput), func(t *testing.T) {
			export, out := t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(export, "run.json"), raw, 0o600))
			pkg, err := attestpublication.BuildPackage(export, filepath.Join(out, "attest-run.zip"))
			require.NoError(t, err)
			journal, err := attestpublication.NewPendingJournal(pkg)
			require.NoError(t, err)
			journal.RunID = "bdd_00000000-0000-7000-8000-000000000001"
			journal.Completed = true
			journalPath := filepath.Join(out, "attest-upload.json")
			require.NoError(t, attestpublication.SaveJournal(journalPath, journal))
			reportURL := "https://attest-cli.invalid/attest/" + journal.RunID
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/repos/repo_attest_output/attest/runs/"+journal.RunID {
					t.Errorf("unexpected publication request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"run_id": journal.RunID, "status": "published", "url": reportURL,
					"source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key,
					"created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{},
				}})
			}))
			t.Cleanup(server.Close)
			t.Setenv("SAGEOX_TOKEN", "")
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			// Keep --open deterministic and prevent a test from launching a browser.
			t.Setenv("SKIP_BROWSER", "1")
			project := testGitRepo(t)
			requireSageoxDir(t, project)
			require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{RepoID: "repo_attest_output", Endpoint: server.URL}))
			require.NoError(t, auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{AccessToken: "private-test-token", ExpiresAt: time.Now().Add(time.Hour)}))
			t.Chdir(project)
			cmd := attestBoundaryCommand(export, out)
			require.NoError(t, cmd.Flags().Set("json", fmt.Sprint(jsonOutput)))
			require.NoError(t, cmd.Flags().Set("open", "true"))
			var output bytes.Buffer
			cmd.SetOut(&output)
			require.NoError(t, runAttestPublish(cmd, nil))
			require.NotContains(t, output.String(), "private-test-token")
			if jsonOutput {
				var result map[string]any
				require.NoError(t, json.Unmarshal(output.Bytes(), &result))
				require.Equal(t, map[string]any{"run_id": journal.RunID, "status": "published", "url": reportURL, "archive_sha256": pkg.ArchiveSHA256, "journal": journalPath}, result)
			} else {
				require.Equal(t, fmt.Sprintf("Attest publication %s is published\nReport: %s\nResume journal: %s\n", journal.RunID, reportURL, journalPath), output.String())
			}
		})
	}
}

func TestAttestPublishErrorGuidancePreservesCause(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		cause         error
	}{
		{"conflict", "conflicts with immutable frozen package", &attestpublication.APIError{Status: 409}},
		{"deleted", "identity was deleted", &attestpublication.APIError{Status: 410}},
		{"unavailable", "preserve the resume journal and retry", &attestpublication.APIError{Status: 503}},
		{"missing archive", "local packaging or resume state is unavailable", os.ErrNotExist},
		{"transfer", "S3 transfer or processing failed", errors.New("transfer interrupted")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := renderAttestPublishError(fmt.Errorf("publication: %w", tc.cause))
			require.ErrorContains(t, err, tc.message)
			require.ErrorIs(t, err, tc.cause)
		})
	}
}
