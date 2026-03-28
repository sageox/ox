package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
)

// viewAsWeb opens a session in the web viewer.
// Requires the session to be pushed to the ledger (meta.json committed).
// Takes only a session name — does not need full session content.
func viewAsWeb(sessionName string, projectRoot string) error {
	if sessionName == "" {
		return fmt.Errorf("no session name provided")
	}

	if projectRoot == "" {
		return fmt.Errorf("not in a project directory")
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || cfg.RepoID == "" || cfg.GetEndpoint() == "" {
		return fmt.Errorf("project not configured for web viewing (missing repo ID or endpoint)")
	}

	// verify the session's meta.json exists in the ledger
	ledgerPath, ledgerErr := resolveLedgerPath()
	if ledgerErr != nil {
		return fmt.Errorf("ledger not available: %w", ledgerErr)
	}
	metaPath := filepath.Join(ledgerPath, "sessions", sessionName, "meta.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		return fmt.Errorf("session %q has not been pushed to the ledger yet\n\nRun 'ox session upload %s' to push existing content, or 'ox agent <id> session stop' if recording is still active", sessionName, sessionName)
	}

	url := buildSessionURL(cfg, sessionName)
	if url == "" {
		return fmt.Errorf("could not build session URL (missing repo ID or endpoint)")
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
