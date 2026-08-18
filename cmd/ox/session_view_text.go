package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/theme"
	"github.com/sageox/ox/internal/ui"
)

var (
	viewTextRecordingBanner = lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Color("#000000")).
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

	// prepend linkage sections (Pull Requests, Issues, Commits) when the
	// session's meta.json carries them. Order: PRs, then Issues, then
	// Commits — coarsest-to-finest granularity reads naturally for a
	// reviewer scanning "what did this session touch?".
	linkageSections := renderSessionLinkage(storedSession, projectRoot)
	if linkageSections != "" {
		mdContent = append([]byte(linkageSections), mdContent...)
	}

	// render with glamour and display
	rendered := ui.RenderMarkdown(string(mdContent))
	fmt.Print(rendered)

	return nil
}

// renderSessionLinkage returns the combined markdown for the linkage
// sections (Pull Requests, Issues, Commits) prepended to a session's
// rendered view. Reads the session's meta.json once and emits whichever
// sections are populated, in coarse-to-fine order. Returns "" when none
// are present.
func renderSessionLinkage(storedSession *session.StoredSession, projectRoot string) string {
	if storedSession == nil {
		return ""
	}
	meta := readSessionMetaForView(storedSession.Info.FilePath)
	if meta == nil {
		return ""
	}

	var b strings.Builder
	if len(meta.LinkedPRs) > 0 {
		b.WriteString("## Pull Requests\n\n")
		for _, pr := range meta.LinkedPRs {
			b.WriteString("- ")
			b.WriteString(pr)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(meta.LinkedIssues) > 0 {
		b.WriteString("## Issues\n\n")
		for _, issue := range meta.LinkedIssues {
			b.WriteString("- ")
			b.WriteString(issue)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(meta.ProducedCommits) > 0 {
		b.WriteString("## Commits\n\n")
		for _, sha := range meta.ProducedCommits {
			b.WriteString("- ")
			b.WriteString(resolveCommitDisplay(projectRoot, sha))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// resolveCommitDisplay returns "<shorthash> <subject>" for a reachable SHA
// or "<sha> <unreachable>" otherwise. projectRoot may be empty when the
// view is reading a foreign session (no project context); in that case we
// fall back to the full SHA without a lookup attempt.
func resolveCommitDisplay(projectRoot, sha string) string {
	if projectRoot == "" {
		return sha
	}
	cmd := exec.Command("git", "-C", projectRoot, "log", "-1", "--format=%h %s", sha)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("`%s` <unreachable>", sha)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return fmt.Sprintf("`%s` <unreachable>", sha)
	}
	return line
}
