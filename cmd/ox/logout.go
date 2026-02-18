package main

import (
	"fmt"
	"log/slog"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/tips"
	"github.com/spf13/cobra"
)

var (
	logoutForce    bool
	logoutAll      bool
	logoutEndpoint string
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out of SageOx",
	Long:  "Remove local authentication token and log out of SageOx.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// get all logged-in endpoints
		loggedInEndpoints := auth.GetLoggedInEndpoints()
		if len(loggedInEndpoints) == 0 {
			fmt.Println("You are not currently logged in to any endpoints.")
			return nil
		}

		// determine which endpoint(s) to logout from
		var endpointsToLogout []string

		if logoutAll {
			// logout from all endpoints
			endpointsToLogout = loggedInEndpoints
		} else if logoutEndpoint != "" {
			// use specified endpoint
			found := false
			for _, ep := range loggedInEndpoints {
				if ep == logoutEndpoint || endpoint.NormalizeSlug(ep) == endpoint.NormalizeSlug(logoutEndpoint) {
					endpointsToLogout = []string{ep}
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("not logged in to endpoint: %s", logoutEndpoint)
			}
		} else if len(loggedInEndpoints) == 1 {
			// only one endpoint, use it
			endpointsToLogout = loggedInEndpoints
		} else {
			// multiple endpoints - prompt for selection
			fmt.Println()
			fmt.Println(cli.StyleDim.Render("You are logged into multiple SageOx endpoints."))
			fmt.Println()

			// display options
			for i, ep := range loggedInEndpoints {
				fmt.Printf("  %d. %s\n", i+1, ep)
			}
			fmt.Printf("  %d. All endpoints\n", len(loggedInEndpoints)+1)
			fmt.Println()

			// prompt for selection
			var selection int
			maxSelection := len(loggedInEndpoints) + 1
			for {
				fmt.Print("Select endpoint to logout (1-", maxSelection, "): ")
				var input string
				fmt.Scanln(&input)
				n, err := fmt.Sscanf(input, "%d", &selection)
				if err == nil && n == 1 && selection >= 1 && selection <= maxSelection {
					break
				}
				fmt.Println(cli.StyleDim.Render("Invalid selection. Please enter a number."))
			}

			if selection == len(loggedInEndpoints)+1 {
				// all endpoints selected
				endpointsToLogout = loggedInEndpoints
			} else {
				endpointsToLogout = []string{loggedInEndpoints[selection-1]}
			}
		}

		// confirm before logging out (skip if --force)
		if !logoutForce {
			var confirmMsg string
			if len(endpointsToLogout) == 1 {
				confirmMsg = fmt.Sprintf("Log out from %s?", endpointsToLogout[0])
			} else {
				confirmMsg = fmt.Sprintf("Log out from %d endpoints?", len(endpointsToLogout))
			}
			if !cli.ConfirmYesNo(confirmMsg, false) {
				fmt.Println("Logout canceled.")
				return nil
			}
		}

		// logout from each endpoint
		var anyServerSuccess bool
		for _, ep := range endpointsToLogout {
			client := auth.NewAuthClient().WithEndpoint(ep)
			serverSuccess, err := client.RevokeToken()
			if err != nil {
				fmt.Printf("Failed to logout from %s: %v\n", ep, err)
				continue
			}
			if serverSuccess {
				anyServerSuccess = true
			}
			fmt.Printf("Logged out from %s\n", ep)
		}

		if !anyServerSuccess && len(endpointsToLogout) > 0 {
			fmt.Println(cli.StyleDim.Render("Note: server-side sessions may still be active."))
		}

		// strip PATs from git remote URLs for logged-out endpoints
		for _, ep := range endpointsToLogout {
			stripExistingRemotes(ep)
		}

		// show contextual tip
		userCfg, _ := config.LoadUserConfig("")
		tips.MaybeShow("logout", tips.AlwaysShow, false, !userCfg.AreTipsEnabled(), false)

		return nil
	},
}

func init() {
	logoutCmd.Flags().BoolVarP(&logoutForce, "force", "f", false, "skip confirmation prompt (for scripting)")
	logoutCmd.Flags().BoolVar(&logoutAll, "all", false, "logout from all endpoints")
	logoutCmd.Flags().StringVar(&logoutEndpoint, "endpoint", "", "endpoint to logout from")
}

// stripExistingRemotes removes PATs from all known ledger/team-context remote URLs.
// Called after logout to prevent stale credentials from lingering in .git/config.
func stripExistingRemotes(ep string) {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil || localCfg == nil {
		return
	}

	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		if err := gitserver.StripRemoteCredentials(localCfg.Ledger.Path); err != nil {
			slog.Debug("failed to strip ledger remote credentials", "error", err)
		}
	}

	for _, tc := range localCfg.TeamContexts {
		if tc.Path == "" {
			continue
		}
		if err := gitserver.StripRemoteCredentials(tc.Path); err != nil {
			slog.Debug("failed to strip team context remote credentials", "team", tc.TeamName, "error", err)
		}
	}
}
