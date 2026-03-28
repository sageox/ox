package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionExportCmd = &cobra.Command{
	Use:   "export [session-name]",
	Short: "Export session to markdown file",
	Long: `Export a session to a standalone markdown file.

Without arguments, exports the most recent session.

Examples:
  ox session export
  ox session export 2026-01-05T10-30-user-Oxa7b3
  ox session export --output report.md
  ox session export --input /path/to/session.jsonl`,
	RunE: runSessionExport,
}

func init() {
	sessionCmd.AddCommand(sessionExportCmd)
	sessionExportCmd.Flags().StringP("input", "i", "", "input JSONL file path (bypasses managed store)")
	sessionExportCmd.Flags().StringP("output", "o", "", "output file path")
}

func runSessionExport(cmd *cobra.Command, args []string) error {
	inputPath, _ := cmd.Flags().GetString("input")
	outputPath, _ := cmd.Flags().GetString("output")

	// resolve session
	var storedSession *session.StoredSession
	var err error

	if inputPath != "" {
		storedSession, err = session.ReadSessionFromPath(inputPath)
		if err != nil {
			if errors.Is(err, session.ErrSessionNotFound) {
				return fmt.Errorf("file not found: %s", inputPath)
			}
			return fmt.Errorf("read session: %w", err)
		}

		if outputPath == "" {
			baseName := strings.TrimSuffix(storedSession.Info.Filename, ".jsonl")
			outputPath = filepath.Join(filepath.Dir(storedSession.Info.FilePath), baseName+".md")
		}
	} else {
		projectRoot := config.FindProjectRoot()
		if projectRoot == "" {
			return fmt.Errorf("not in a SageOx project (no .sageox directory found)")
		}

		cfg, _, err := config.GetProjectContext()
		if err != nil {
			return fmt.Errorf("load project config: %w", err)
		}

		repoID := ""
		if cfg != nil {
			repoID = cfg.RepoID
		}
		if repoID == "" {
			return fmt.Errorf("no repo ID configured (run 'ox init' first)")
		}

		contextPath := session.GetContextPath(repoID)
		if contextPath == "" {
			return fmt.Errorf("cannot determine session storage path")
		}

		store, err := session.NewStore(contextPath)
		if err != nil {
			return fmt.Errorf("create session store: %w", err)
		}

		var sessionInfo *session.SessionInfo
		var filename string

		if len(args) > 0 {
			filename = args[0]
			if !strings.HasSuffix(filename, ".jsonl") {
				filename += ".jsonl"
			}
			t, err := store.ReadSession(filename)
			if err != nil {
				if errors.Is(err, session.ErrSessionNotFound) {
					return fmt.Errorf("session not found: %s", filename)
				}
				return fmt.Errorf("read session: %w", err)
			}
			sessionInfo = &t.Info
		} else {
			latest, err := store.GetLatest()
			if err != nil {
				if errors.Is(err, session.ErrNoSessions) {
					fmt.Println()
					fmt.Println(cli.StyleDim.Render("  No sessions found."))
					fmt.Println()
					cli.PrintHint("Start a recording with 'ox agent <id> session start' to capture your development session.")
					return nil
				}
				return fmt.Errorf("get latest session: %w", err)
			}
			sessionInfo = latest
			filename = latest.Filename
		}

		storedSession, err = store.ReadSession(filename)
		if err != nil {
			return fmt.Errorf("read session %s: %w", filename, err)
		}

		if outputPath == "" {
			baseName := strings.TrimSuffix(sessionInfo.Filename, ".jsonl")
			outputPath = filepath.Join(filepath.Dir(sessionInfo.FilePath), baseName+".md")
		}
	}

	// generate markdown output
	gen := session.NewMarkdownGenerator()
	if err := gen.GenerateToFile(storedSession, outputPath); err != nil {
		return fmt.Errorf("generate markdown: %w", err)
	}
	cli.PrintSuccess(fmt.Sprintf("Exported markdown: %s", cli.StyleFile.Render(outputPath)))

	return nil
}
