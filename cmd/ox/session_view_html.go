package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
)

var (
	viewHTMLRecordingBanner = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#000000")).
		Background(cli.ColorWarning).
		Padding(0, 1)
)

// viewAsHTML renders a session as HTML and opens in browser.
func viewAsHTML(_ *session.Store, storedSession *session.StoredSession, projectRoot string) error {
	// check if recording is in progress
	isRecording := false
	if projectRoot != "" {
		isRecording = session.IsRecording(projectRoot)
	}

	// determine HTML path (session.html in the session directory)
	htmlPath := filepath.Join(filepath.Dir(storedSession.Info.FilePath), "session.html")

	// check if HTML exists and is up-to-date
	needsGeneration := false
	htmlInfo, err := os.Stat(htmlPath)
	if os.IsNotExist(err) {
		needsGeneration = true
	} else if err == nil {
		// HTML exists - check if it's stale (JSONL is newer)
		jsonlInfo, jsonlErr := os.Stat(storedSession.Info.FilePath)
		if jsonlErr == nil && jsonlInfo.ModTime().After(htmlInfo.ModTime()) {
			needsGeneration = true
			fmt.Println(cli.StyleDim.Render("  HTML is stale, regenerating..."))
		}
	}

	if needsGeneration {
		// show recording warning if applicable
		if isRecording {
			fmt.Println(viewHTMLRecordingBanner.Render(" RECORDING IN PROGRESS "))
			fmt.Println(cli.StyleDim.Render("  HTML may be incomplete. Final version generated when recording stops."))
			fmt.Println()
		}

		fmt.Println(cli.StyleDim.Render("  Generating HTML viewer..."))

		if err := generateHTML(storedSession, htmlPath); err != nil {
			return fmt.Errorf("generate HTML: %w", err)
		}

		cli.PrintSuccess(fmt.Sprintf("Generated: %s", cli.StyleFile.Render(htmlPath)))
	}

	// open in browser
	if err := cli.OpenInBrowser("file://" + htmlPath); err != nil {
		if errors.Is(err, cli.ErrHeadless) {
			cli.PrintSuccess(fmt.Sprintf("Generated: %s", cli.StyleFile.Render(htmlPath)))
			fmt.Println()
			cli.PrintWarning("Cannot open browser in this environment (SSH/headless)")
			fmt.Println()
			cli.PrintHint("To view online, upload the session first: ox session stop")
			cli.PrintHint("Or copy the HTML file to a machine with a browser:")
			cli.PrintHint(fmt.Sprintf("  scp %s <local-machine>:", htmlPath))
			return nil
		}
		return fmt.Errorf("open browser: %w", err)
	}

	cli.PrintSuccess(fmt.Sprintf("Opened in browser: %s", cli.StyleFile.Render(filepath.Base(htmlPath))))
	return nil
}

// viewAsWeb opens a session in the web viewer.
// Requires the session to be pushed to the ledger (meta.json committed).
// Takes only a session name — does not need full session content.
func viewAsWeb(sessionName string, projectRoot string) error {
	if sessionName == "" {
		return fmt.Errorf("no session name provided\n\nUse --html to view a local session file")
	}

	if projectRoot == "" {
		return fmt.Errorf("not in a project directory\n\nUse --html to view locally")
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || cfg.RepoID == "" || cfg.GetEndpoint() == "" {
		return fmt.Errorf("project not configured for web viewing (missing repo ID or endpoint)\n\nUse --html to view locally")
	}

	// verify the session's meta.json exists in the ledger
	ledgerPath, ledgerErr := resolveLedgerPath()
	if ledgerErr != nil {
		return fmt.Errorf("ledger not available: %w\n\nUse --html to view locally", ledgerErr)
	}
	metaPath := filepath.Join(ledgerPath, "sessions", sessionName, "meta.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		return fmt.Errorf("session %q has not been pushed to the ledger yet\n\nRun 'ox session stop' to finalize, or use --html to view locally", sessionName)
	}

	url := buildSessionURL(cfg, sessionName)
	if url == "" {
		return fmt.Errorf("could not build session URL (missing repo ID or endpoint)\n\nUse --html to view locally")
	}

	if err := cli.OpenInBrowser(url); err != nil {
		if errors.Is(err, cli.ErrHeadless) {
			fmt.Printf("View session at: %s\n", url)
			return nil
		}
		fmt.Printf("%s Could not open browser. Visit: %s\n", cli.StyleWarning.Render("!"), url)
		return nil
	}

	fmt.Printf("Opening %s\n", url)
	return nil
}
