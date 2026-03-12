package main

import (
	"fmt"
	"os"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/ui"
)

var (
	viewTextRecordingBanner = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#000000")).
		Background(cli.ColorWarning).
		Padding(0, 1)
)

// viewAsText renders a session as markdown in the terminal.
func viewAsText(_ *session.Store, storedSession *session.StoredSession, projectRoot string) error {
	// check if recording is in progress
	isRecording := false
	if projectRoot != "" {
		// first-found variant: display check for any active recording, no agent context
		isRecording = session.IsRecording(projectRoot)
	}

	// determine markdown path (same directory, -session.md suffix)
	mdPath := strings.TrimSuffix(storedSession.Info.FilePath, ".jsonl") + "-session.md"

	// check if markdown exists and is up-to-date
	needsGeneration := false
	mdInfo, err := os.Stat(mdPath)
	if os.IsNotExist(err) {
		needsGeneration = true
	} else if err == nil {
		// markdown exists - check if it's stale (JSONL is newer)
		jsonlInfo, jsonlErr := os.Stat(storedSession.Info.FilePath)
		if jsonlErr == nil && jsonlInfo.ModTime().After(mdInfo.ModTime()) {
			needsGeneration = true
			fmt.Println(cli.StyleDim.Render("  Markdown is stale, regenerating..."))
		}
	}

	if needsGeneration {
		// show recording warning if applicable
		if isRecording {
			fmt.Println(viewTextRecordingBanner.Render(" RECORDING IN PROGRESS "))
			fmt.Println(cli.StyleDim.Render("  Session may be incomplete."))
			fmt.Println()
		}

		fmt.Println(cli.StyleDim.Render("  Generating markdown..."))

		// generate markdown
		mdGen := session.NewMarkdownGenerator()
		if err := mdGen.GenerateToFile(storedSession, mdPath); err != nil {
			return fmt.Errorf("generate markdown: %w", err)
		}
	}

	// read the markdown file
	mdContent, err := os.ReadFile(mdPath)
	if err != nil {
		return fmt.Errorf("read markdown file: %w", err)
	}

	// render with glamour and display
	rendered := ui.RenderMarkdown(string(mdContent))
	fmt.Print(rendered)

	return nil
}
