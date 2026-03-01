package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/spf13/cobra"
)

var (
	dataPutDomain string
	dataPutFile   string
)

var domainPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

var dataPutCmd = &cobra.Command{
	Use:   "put",
	Short: "Upload structured data to team context",
	Long: `Upload structured data from external sources into the team context repository.

Data is written to the data/<domain>/ directory and backed by Git LFS.
The sync daemon pushes to the server on the next cycle.

Examples:
  ox data put --domain=jira --file sprint-metrics.jsonl
  cat slack-export.json | ox data put --domain=slack
  ox data put --domain=recordings --file transcript.json`,
	RunE: runDataPut,
}

func init() {
	dataPutCmd.Flags().StringVar(&dataPutDomain, "domain", "", "data domain (lowercase kebab-case, e.g. 'slack', 'google-meet')")
	dataPutCmd.Flags().StringVar(&dataPutFile, "file", "", "path to file to upload (reads stdin if omitted)")
	_ = dataPutCmd.MarkFlagRequired("domain")
}

func runDataPut(cmd *cobra.Command, _ []string) error {
	if !domainPattern.MatchString(dataPutDomain) {
		return fmt.Errorf("domain must be lowercase kebab-case (e.g., 'slack', 'google-meet'), got %q", dataPutDomain)
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return fmt.Errorf("no team context configured — run 'ox init' first")
	}

	// read content
	var content []byte
	var filename string

	if dataPutFile != "" {
		content, err = os.ReadFile(dataPutFile)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("file not found: %s", dataPutFile)
			}
			return fmt.Errorf("read file: %w", err)
		}
		filename = filepath.Base(dataPutFile)
	} else {
		content, err = io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		id, _ := uuid.NewV7()
		filename = id.String() + ".dat"
	}

	if len(content) == 0 {
		return fmt.Errorf("no data provided")
	}

	// write to data/<domain>/
	domainDir := filepath.Join(tc.Path, "data", dataPutDomain)
	if err := os.MkdirAll(domainDir, 0o755); err != nil {
		return fmt.Errorf("create domain directory: %w", err)
	}

	destPath := filepath.Join(domainDir, filename)
	if err := os.WriteFile(destPath, content, 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// check .gitattributes for LFS tracking
	checkLFSConfig(tc.Path)

	// git add + commit
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relPath := filepath.Join("data", dataPutDomain, filename)

	if _, err := gitutil.RunGit(ctx, tc.Path, "add", relPath); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	commitMsg := fmt.Sprintf("data: add %s to %s", filename, dataPutDomain)
	if _, err := gitutil.RunGit(ctx, tc.Path, "commit", "-m", commitMsg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	slog.Info("data uploaded", "domain", dataPutDomain, "file", filename, "path", relPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Uploaded %s to data/%s/\n", filename, dataPutDomain)
	return nil
}

func checkLFSConfig(teamContextDir string) {
	gitattrs := filepath.Join(teamContextDir, ".gitattributes")
	content, err := os.ReadFile(gitattrs)
	if err != nil {
		slog.Warn("data: .gitattributes not found — data/ may not be LFS-tracked", "path", gitattrs)
		return
	}
	if !strings.Contains(string(content), "data/**") {
		slog.Warn("data: .gitattributes does not include 'data/**' LFS tracking")
	}
}
