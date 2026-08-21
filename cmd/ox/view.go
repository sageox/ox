package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

var viewEndpointFlag string

var viewCmd = &cobra.Command{
	Use:   "view",
	Short: "Open SageOx dashboard in browser",
	Long: `Open the SageOx web dashboard.

To open your team dashboard, use 'ox team open'.`,
	// A bare `ox view` prints help. A stray token like `ox view team` returns an
	// error in cobra's native "unknown command" form so the friction catalog
	// recognizes it and redirects to `ox team open` — a parent with subcommands
	// but no RunE would instead swallow the arg into Help and never redirect
	// (the same trap `ox teams members` used to hit).
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
	},
}

var viewRepoCmd = &cobra.Command{
	Use:    "repo",
	Short:  "Open repo dashboard in browser",
	Long:   `Opens the SageOx dashboard for the current repository.`,
	Hidden: true, // hidden until individual repo pages exist
	RunE: func(cmd *cobra.Command, args []string) error {
		// normalize endpoint flag before use
		normalizedEndpoint := endpoint.NormalizeEndpoint(viewEndpointFlag)

		// find git root
		gitRoot, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository: %w", err)
		}

		// load project config
		cfg, err := config.LoadProjectConfig(gitRoot)
		if err != nil {
			return fmt.Errorf("failed to load project config: %w", err)
		}

		// check for repo_id
		if cfg.RepoID == "" {
			fmt.Println("No repo_id found. Run 'ox init' first to register this repository.")
			return nil
		}

		// get endpoint, potentially prompting for selection if multiple exist
		endpointURL, err := resolveEndpoint(gitRoot, cfg.GetEndpoint(), normalizedEndpoint)
		if err != nil {
			return err
		}

		// build URL and open
		url := fmt.Sprintf("%s/repo/%s", endpointURL, cfg.RepoID)
		fmt.Printf("Opening %s\n", url)

		if err := cli.OpenInBrowser(url); err != nil {
			if errors.Is(err, cli.ErrHeadless) {
				fmt.Printf("Visit: %s\n", url)
				return nil
			}
			fmt.Printf("%s Could not open browser. Visit: %s\n", cli.StyleWarning.Render("!"), url)
		}

		return nil
	},
}

// resolveEndpoint handles endpoint selection when multiple endpoints exist in .sageox/.
// If flagEndpoint is provided, it is used directly (after validation).
// If multiple endpoints exist and no flag is provided, prompts the user to select.
// Otherwise, returns the defaultEndpoint from config.
func resolveEndpoint(gitRoot, defaultEndpoint, flagEndpoint string) (string, error) {
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// discover all endpoints from marker files
	endpoints, err := cli.DiscoverEndpoints(sageoxDir)
	if err != nil {
		// if we can't discover endpoints, fall back to config
		return defaultEndpoint, nil
	}

	// if no endpoints discovered or only one, use default behavior
	if len(endpoints) <= 1 {
		if flagEndpoint != "" {
			// validate flag against discovered endpoint or config
			if len(endpoints) == 1 && endpoints[0].Endpoint != flagEndpoint {
				return "", fmt.Errorf("endpoint %q not found in .sageox/ markers", flagEndpoint)
			}
			return flagEndpoint, nil
		}
		return defaultEndpoint, nil
	}

	// multiple endpoints exist - need selection
	return cli.SelectEndpoint(endpoints, defaultEndpoint, flagEndpoint)
}

func init() {
	// add --endpoint flag to view subcommands
	viewRepoCmd.Flags().StringVar(&viewEndpointFlag, "endpoint", "", "SageOx endpoint URL (for multi-endpoint repos)")

	viewCmd.AddCommand(viewRepoCmd)
	rootCmd.AddCommand(viewCmd)
}
