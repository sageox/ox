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
	Short: "Export session to HTML or markdown file",
	Long: `Export a session to a standalone file.

By default exports as a self-contained HTML viewer with message display,
collapsible tool calls, timestamps, and SageOx branding.

Use --markdown for a plain markdown export instead.

Without arguments, exports the most recent session.

Examples:
  ox session export
  ox session export 2026-01-05T10-30-user-Oxa7b3
  ox session export --markdown
  ox session export --output report.html
  ox session export --open
  ox session export --input /path/to/session.jsonl`,
	RunE: runSessionExport,
}

func init() {
	sessionCmd.AddCommand(sessionExportCmd)
	sessionExportCmd.Flags().Bool("html", false, "export as HTML (default)")
	sessionExportCmd.Flags().Bool("markdown", false, "export as markdown")
	sessionExportCmd.Flags().StringP("input", "i", "", "input JSONL file path (bypasses managed store)")
	sessionExportCmd.Flags().StringP("output", "o", "", "output file path")
	sessionExportCmd.Flags().Bool("open", false, "open in browser after generation (HTML only)")
}

func runSessionExport(cmd *cobra.Command, args []string) error {
	htmlFlag, _ := cmd.Flags().GetBool("html")
	markdownFlag, _ := cmd.Flags().GetBool("markdown")
	inputPath, _ := cmd.Flags().GetString("input")
	outputPath, _ := cmd.Flags().GetString("output")
	openBrowser, _ := cmd.Flags().GetBool("open")

	// mutual exclusion
	if htmlFlag && markdownFlag {
		return fmt.Errorf("specify only one of --html or --markdown")
	}

	// default to HTML
	format := "html"
	if markdownFlag {
		format = "markdown"
	}

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
			ext := ".html"
			if format == "markdown" {
				ext = ".md"
			}
			outputPath = filepath.Join(filepath.Dir(storedSession.Info.FilePath), baseName+ext)
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
			ext := ".html"
			if format == "markdown" {
				ext = ".md"
			}
			outputPath = filepath.Join(filepath.Dir(sessionInfo.FilePath), baseName+ext)
		}
	}

	// generate output
	switch format {
	case "html":
		if err := generateHTML(storedSession, outputPath); err != nil {
			return fmt.Errorf("generate HTML: %w", err)
		}
		cli.PrintSuccess(fmt.Sprintf("Exported HTML: %s", cli.StyleFile.Render(outputPath)))

		if openBrowser {
			if err := cli.OpenInBrowser("file://" + outputPath); err != nil {
				cli.PrintWarning(fmt.Sprintf("Could not open browser: %v", err))
			}
		}

	case "markdown":
		gen := session.NewMarkdownGenerator()
		if err := gen.GenerateToFile(storedSession, outputPath); err != nil {
			return fmt.Errorf("generate markdown: %w", err)
		}
		cli.PrintSuccess(fmt.Sprintf("Exported markdown: %s", cli.StyleFile.Render(outputPath)))
	}

	return nil
}
