package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

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
		endpointsToLogout, selErr := selectLogoutEndpoints(loggedInEndpoints, logoutAll, logoutEndpoint, logoutForce)
		if selErr != nil {
			return selErr
		}

		// confirm before logging out (skip if --force)
		if !logoutForce {
			var confirmMsg string
			if len(endpointsToLogout) == 1 {
				confirmMsg = fmt.Sprintf("Log out from %s?", endpointsToLogout[0])
			} else {
				confirmMsg = fmt.Sprintf("Log out from %d endpoints?", len(endpointsToLogout))
			}
			confirmed, confirmErr := cli.ConfirmYesNoRequired(confirmMsg, false, false)
			if confirmErr != nil {
				return confirmErr
			}
			if !confirmed {
				fmt.Println("Logout canceled.")
				return nil
			}
		}

		// logout from each endpoint
		var anyServerFailed bool
		for _, ep := range endpointsToLogout {
			client := auth.NewAuthClient().WithEndpoint(ep)
			status, err := client.RevokeTokenStatus()
			if err != nil {
				fmt.Printf("Failed to logout from %s: %v\n", ep, err)
				anyServerFailed = true
				continue
			}
			switch status {
			case auth.RevocationSuccess, auth.RevocationNoToken:
				fmt.Printf("Logged out from %s\n", ep)
			case auth.RevocationNetworkError:
				anyServerFailed = true
				fmt.Printf("Logged out locally from %s (could not reach server)\n", ep)
			case auth.RevocationServerFailed:
				anyServerFailed = true
				fmt.Printf("Logged out locally from %s (server did not confirm revocation)\n", ep)
			}
		}

		if anyServerFailed {
			fmt.Println(cli.StyleDim.Render("Note: server-side sessions may still be active — try again when online."))
		}

		// strip PATs from git remote URLs and clear the stored git credential
		// (keychain + file) for each logged-out endpoint. Revoking the OAuth
		// token alone leaves the separately-minted git PAT live on disk and
		// server-side — a decommissioned/shared machine would keep push access.
		for _, ep := range endpointsToLogout {
			stripExistingRemotes(ep)
			if err := gitserver.RemoveCredentialsForEndpoint(ep); err != nil {
				slog.Debug("logout: failed to remove git credentials", "endpoint", ep, "error", err)
			}
		}

		// show contextual tip
		userCfg, _ := config.LoadUserConfig()
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

// selectLogoutEndpoints resolves which endpoint(s) `ox logout` should act on.
//
// When several endpoints are logged in and none was named, this asks. If the
// answer cannot be read it returns an error rather than canceling: canceling
// exited 0 having revoked nothing and removed no local credentials, which is a
// logout that silently did not happen. Which endpoint to log out of is a
// selection, not a yes/no, so --yes cannot answer it either — the error names
// the deterministic alternatives instead of guessing one.
func selectLogoutEndpoints(loggedInEndpoints []string, all bool, specified string, force bool) ([]string, error) {
	switch {
	case all:
		return loggedInEndpoints, nil

	case specified != "":
		for _, ep := range loggedInEndpoints {
			if ep == specified || endpoint.NormalizeSlug(ep) == endpoint.NormalizeSlug(specified) {
				return []string{ep}, nil
			}
		}
		return nil, fmt.Errorf("not logged in to endpoint: %s", specified)

	case len(loggedInEndpoints) == 1:
		return loggedInEndpoints, nil

	case force:
		// --force with multiple endpoints: log out from all (non-interactive)
		return loggedInEndpoints, nil
	}

	// multiple endpoints - prompt for selection
	fmt.Println()
	fmt.Println(cli.StyleDim.Render("You are logged into multiple SageOx endpoints."))
	fmt.Println()

	for i, ep := range loggedInEndpoints {
		fmt.Printf("  %d. %s\n", i+1, ep)
	}
	fmt.Printf("  %d. All endpoints\n", len(loggedInEndpoints)+1)
	fmt.Println()

	maxSelection := len(loggedInEndpoints) + 1
	for {
		fmt.Print("Select endpoint to logout (1-", maxSelection, "): ")
		var input string
		if _, err := fmt.Scanln(&input); err != nil {
			return nil, fmt.Errorf("logged into %d endpoints and no selection could be read from stdin; "+
				"pass --endpoint <endpoint> to choose one, or --all to log out of every endpoint",
				len(loggedInEndpoints))
		}
		// strconv.Atoi, not fmt.Sscanf("%d"): Sscanf stops at the first
		// non-digit, so "1abc" would parse as 1 and silently log out of an
		// endpoint the operator never typed.
		selection, err := strconv.Atoi(strings.TrimSpace(input))
		if err == nil && selection >= 1 && selection <= maxSelection {
			if selection == maxSelection {
				return loggedInEndpoints, nil
			}
			return []string{loggedInEndpoints[selection-1]}, nil
		}
		fmt.Println(cli.StyleDim.Render("Invalid selection. Please enter a number."))
	}
}
