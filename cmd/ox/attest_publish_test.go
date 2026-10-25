package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
)

func TestRunAttestPublishUsesCommandContext(t *testing.T) {
	serverRequests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		serverRequests <- struct{}{}
	}))
	t.Cleanup(server.Close)

	t.Setenv("SAGEOX_TOKEN", "")
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	projectRoot := testGitRepo(t)
	requireSageoxDir(t, projectRoot)
	if err := config.SaveProjectConfig(projectRoot, &config.ProjectConfig{RepoID: "repo_attest_context", Endpoint: server.URL}); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{AccessToken: "test-access-token", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	export := filepath.Join(t.TempDir(), "frozen-export")
	if err := os.MkdirAll(filepath.Join(export, "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := repoPath("..", "..", "internal", "attestpublication", "contract", "testdata", "conformance", "valid", "run-all-outcomes.json")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(export, "run.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(export, "report", "index.html"), []byte("<h1>frozen</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.Flags().String("from", export, "")
	cmd.Flags().String("out", filepath.Join(t.TempDir(), "publication"), "")
	if err := runAttestPublish(cmd, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("runAttestPublish() error = %v, want context canceled", err)
	}
	select {
	case <-serverRequests:
		t.Fatal("publish control request ignored the canceled command context")
	default:
	}
}

// Retrying a journaled publication must use the original bytes even if the export disappeared or changed.
func TestRunAttestPublishResumesOriginalArchive(t *testing.T) {
	for _, change := range []string{"removed", "changed"} {
		t.Run(change, func(t *testing.T) {
			source := repoPath("..", "..", "internal", "attestpublication", "contract", "testdata", "conformance", "valid", "run-all-outcomes.json")
			raw, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			export := t.TempDir()
			if err := os.WriteFile(filepath.Join(export, "run.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			pkg, err := attestpublication.BuildPackage(export, filepath.Join(out, "attest-run.zip"))
			if err != nil {
				t.Fatal(err)
			}
			archiveBefore, err := os.ReadFile(pkg.ArchivePath)
			if err != nil {
				t.Fatal(err)
			}
			journal, err := attestpublication.NewPendingJournal(pkg)
			if err != nil {
				t.Fatal(err)
			}
			journal.RunID = "bdd_00000000-0000-7000-8000-000000000001"
			journal.Completed = true
			if err := attestpublication.SaveJournal(filepath.Join(out, "attest-upload.json"), journal); err != nil {
				t.Fatal(err)
			}
			if change == "removed" {
				if err := os.RemoveAll(export); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(export, "new-evidence.txt"), []byte("later bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet {
					t.Errorf("resume issued %s, want GET", r.Method)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"run_id": journal.RunID, "status": "published", "source_run_id": pkg.Run.SourceRunID, "corpus_key": pkg.Run.Corpus.Key, "created_at": "2026-01-01T00:00:00Z", "diagnostics": []any{}}})
			}))
			defer server.Close()
			t.Setenv("SAGEOX_TOKEN", "")
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			project := testGitRepo(t)
			requireSageoxDir(t, project)
			if err := config.SaveProjectConfig(project, &config.ProjectConfig{RepoID: "repo_attest_resume", Endpoint: server.URL}); err != nil {
				t.Fatal(err)
			}
			if err := auth.SaveTokenForEndpoint(server.URL, &auth.StoredToken{AccessToken: "test-access-token", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			t.Chdir(project)
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			cmd.SetOut(io.Discard)
			cmd.Flags().String("from", export, "")
			cmd.Flags().String("out", out, "")
			if err := runAttestPublish(cmd, nil); err != nil {
				t.Errorf("resume failed after export %s: %v", change, err)
			}
			archiveAfter, err := os.ReadFile(pkg.ArchivePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(archiveBefore, archiveAfter) {
				t.Error("resume overwrote the journal-bound archive")
			}
			if requests != 1 {
				t.Errorf("resume requests = %d, want one status read", requests)
			}
		})
	}
}
