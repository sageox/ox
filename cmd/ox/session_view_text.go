package main

import (
	"fmt"
	"os"
	"os/exec"
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

	// prepend Commits section if SessionMeta carries a ProducedCommits index.
	// Each SHA is resolved to "short message" via git log; SHAs no longer
	// reachable (closed-session post-rewrite case from D3) render as
	// "<unreachable>" so the section stays informative without lying about
	// what's in the current history.
	commitsSection := renderProducedCommits(storedSession, projectRoot)
	if commitsSection != "" {
		mdContent = append([]byte(commitsSection), mdContent...)
	}

	// render with glamour and display
	rendered := ui.RenderMarkdown(string(mdContent))
	fmt.Print(rendered)

	return nil
}

// renderProducedCommits returns a markdown "## Commits" section listing the
// SHAs in SessionMeta.ProducedCommits, or "" if there are none. Resolves
// each SHA against the project repo via `git log -1 --format=%h %s` so the
// reader sees the short hash plus commit subject rather than just an opaque
// 40-char hex string.
func renderProducedCommits(storedSession *session.StoredSession, projectRoot string) string {
	if storedSession == nil {
		return ""
	}
	shas := readProducedCommits(storedSession.Info.FilePath)
	if len(shas) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Commits\n\n")
	for _, sha := range shas {
		display := resolveCommitDisplay(projectRoot, sha)
		b.WriteString("- ")
		b.WriteString(display)
		b.WriteString("\n")
	}
	b.WriteString("\n")
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
