package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/tips"
	"github.com/spf13/cobra"
)

// loginSpinnerModel is the bubbletea model for the login spinner
type loginSpinnerModel struct {
	spinner  spinner.Model
	message  string
	done     bool
	canceled bool
	err      error
	result   chan error
}

// loginResultMsg is sent when the login completes or fails
type loginResultMsg struct {
	err error
}

func initialLoginSpinnerModel() loginSpinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	// note: s.Style not set - uses default; lipgloss v2 migration pending for bubbles
	return loginSpinnerModel{
		spinner: s,
		message: "Waiting for authorization...",
		result:  make(chan error, 1),
	}
}

func (m loginSpinnerModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.waitForLogin(),
	)
}

func (m loginSpinnerModel) waitForLogin() tea.Cmd {
	return func() tea.Msg {
		err := <-m.result
		return loginResultMsg{err: err}
	}
}

func (m loginSpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.done = true
			m.canceled = true
			return m, tea.Quit
		}
	case loginResultMsg:
		m.done = true
		m.err = msg.err
		return m, tea.Quit
	default:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m loginSpinnerModel) View() tea.View {
	if m.done {
		if m.canceled {
			return tea.NewView(lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("⊘ Authentication canceled\n"))
		}
		if m.err != nil {
			return tea.NewView(lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("✗ Authentication failed\n"))
		}
		return tea.NewView(lipgloss.NewStyle().Foreground(lipgloss.Color("46")).Render("✓ Successfully authenticated\n"))
	}
	return tea.NewView(fmt.Sprintf("\n%s %s\n\n", m.spinner.View(), m.message))
}

// isNetworkError checks if an error is a network-related error (DNS, connection refused, etc.)
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// check for net.Error (includes DNS errors, connection refused, timeouts)
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// check error message for common network error patterns
	errMsg := err.Error()
	networkPatterns := []string{
		"no such host",
		"connection refused",
		"dial tcp",
		"network is unreachable",
		"i/o timeout",
	}
	for _, pattern := range networkPatterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}
	return false
}

// getAlternativeEndpoints returns endpoints configured in the project that differ from the current one
func getAlternativeEndpoints(currentEndpoint string) []string {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return nil
	}

	configuredEndpoints := config.GetConfiguredEndpoints(gitRoot)
	normalizedCurrent := endpoint.NormalizeEndpoint(currentEndpoint)
	var alternatives []string
	for _, ep := range configuredEndpoints {
		if endpoint.NormalizeEndpoint(ep) != normalizedCurrent && ep != "" {
			alternatives = append(alternatives, ep)
		}
	}
	return alternatives
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with sageox.ai",
	Long:  "Authenticate with sageox.ai to access premium features and sync your configuration.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// if endpoint flag provided, skip selection
		if loginEndpointFlag != "" {
			return runLoginFlow(cmd, endpoint.NormalizeEndpoint(loginEndpointFlag))
		}

		// if SAGEOX_ENDPOINT env var is set, use it directly (no interactive picker)
		if envEP := os.Getenv(endpoint.EnvVar); envEP != "" {
			return runLoginFlow(cmd, endpoint.NormalizeEndpoint(envEP))
		}

		// build list of endpoints to show
		selectedEndpoint, err := selectLoginEndpoint()
		if err != nil {
			return err
		}
		if selectedEndpoint == "" {
			fmt.Println("Login canceled.")
			return nil
		}

		return runLoginFlow(cmd, selectedEndpoint)
	},
}

// endpointInfo holds display information for an endpoint
type endpointInfo struct {
	URL       string
	Slug      string
	Email     string
	ExpiresAt time.Time
	IsExpired bool
	IsValid   bool
}

// selectLoginEndpoint shows endpoint selection UI and returns the selected endpoint URL
func selectLoginEndpoint() (string, error) {
	// get all endpoints with stored tokens
	storedEndpoints, err := auth.ListEndpoints()
	if err != nil {
		storedEndpoints = []string{} // continue with empty list
	}

	// build endpoint info list
	var endpoints []endpointInfo
	seenSlugs := make(map[string]bool)

	for _, ep := range storedEndpoints {
		token, _ := auth.GetTokenForEndpoint(ep)
		info := endpointInfo{
			URL:  ep,
			Slug: endpoint.NormalizeSlug(ep),
		}
		if token != nil {
			info.Email = token.UserInfo.Email
			info.ExpiresAt = token.ExpiresAt
			info.IsExpired = time.Now().After(token.ExpiresAt)
			info.IsValid = !info.IsExpired && token.AccessToken != ""
		}
		endpoints = append(endpoints, info)
		seenSlugs[info.Slug] = true
	}

	// add production endpoint if not already present
	prodSlug := endpoint.NormalizeSlug(endpoint.Default)
	if !seenSlugs[prodSlug] {
		endpoints = append(endpoints, endpointInfo{
			URL:  endpoint.Default,
			Slug: prodSlug,
		})
	}

	// if only one endpoint and it's not authenticated, just use it
	if len(endpoints) == 1 && !endpoints[0].IsValid {
		return endpoints[0].URL, nil
	}

	// show endpoint selection
	fmt.Println("Select endpoint to authenticate:")
	fmt.Println()

	options := make([]string, len(endpoints))
	for i, ep := range endpoints {
		status := ""
		if ep.IsValid {
			status = fmt.Sprintf(" (✓ %s)", ep.Email)
		} else if ep.Email != "" {
			status = fmt.Sprintf(" (expired: %s)", ep.Email)
		}
		options[i] = fmt.Sprintf("%s%s", ep.Slug, status)
	}

	selected, err := cli.SelectOne("Endpoint:", options, 0)
	if err != nil {
		return "", nil // canceled
	}
	if selected < 0 || selected >= len(endpoints) {
		return "", nil
	}

	return endpoints[selected].URL, nil
}

// runLoginFlow executes the login flow for a specific endpoint
func runLoginFlow(cmd *cobra.Command, currentEndpoint string) error {
	// check if already authenticated for this endpoint
	authenticated, err := auth.IsAuthenticatedForEndpoint(currentEndpoint)
	if err != nil {
		return fmt.Errorf("failed to check authentication status: %w", err)
	}

	if authenticated {
		// get current user info
		token, err := auth.GetTokenForEndpoint(currentEndpoint)
		if err != nil {
			return fmt.Errorf("failed to get token: %w", err)
		}

		fmt.Printf("Already authenticated as %s on %s\n", token.UserInfo.Email, endpoint.NormalizeSlug(currentEndpoint))
		if !cli.ConfirmYesNo("Do you want to re-authenticate?", false) {
			fmt.Println("Authentication canceled.")
			return nil
		}
	}

	authClient := auth.NewAuthClient().WithEndpoint(currentEndpoint)

	// request device code
	fmt.Println("Initiating device authentication flow...")
	deviceCode, err := authClient.RequestDeviceCode()
	if err != nil {
		// check if this is a network error and offer alternatives
		if isNetworkError(err) {
			alternatives := getAlternativeEndpoints(currentEndpoint)
			if len(alternatives) > 0 {
				fmt.Printf("\nCould not reach %s\n", currentEndpoint)
				fmt.Println("This project is configured for a different endpoint.")

				var selectedEndpoint string
				if len(alternatives) == 1 {
					// single alternative - ask yes/no
					if cli.ConfirmYesNo(fmt.Sprintf("Would you like to authenticate to %s instead?", alternatives[0]), true) {
						selectedEndpoint = alternatives[0]
					}
				} else {
					// multiple alternatives - show selection
					selected, selectErr := cli.SelectOne("Select endpoint to authenticate to:", alternatives, 0)
					if selectErr == nil && selected >= 0 && selected < len(alternatives) {
						selectedEndpoint = alternatives[selected]
					}
				}

				if selectedEndpoint != "" {
					// retry with selected endpoint
					authClient = authClient.WithEndpoint(selectedEndpoint)
					fmt.Printf("\nAuthenticating to %s...\n", selectedEndpoint)
					deviceCode, err = authClient.RequestDeviceCode()
					if err != nil {
						return fmt.Errorf("failed to request device code: %w", err)
					}
				} else {
					return fmt.Errorf("failed to request device code: %w", err)
				}
			} else {
				return fmt.Errorf("failed to request device code: %w", err)
			}
		} else {
			return fmt.Errorf("failed to request device code: %w", err)
		}
	}

	// display verification instructions
	fmt.Println()
	if deviceCode.VerificationURIComplete != "" {
		fmt.Printf("Visit: %s\n", deviceCode.VerificationURIComplete)
	} else {
		fmt.Printf("Visit: %s\n", deviceCode.VerificationURI)
		fmt.Printf("Enter code: %s\n", deviceCode.UserCode)
	}
	fmt.Println()

	// Open browser to the verification URL so the user can authorize.
	// SKIP_BROWSER and headless detection are handled inside cli.OpenInBrowser.
	browserURL := deviceCode.VerificationURI
	if deviceCode.VerificationURIComplete != "" {
		browserURL = deviceCode.VerificationURIComplete
	}
	if err := cli.OpenInBrowser(browserURL); err != nil {
		slog.Debug("failed to open browser", "error", err)
	}

	// poll for authorization with spinner
	ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(deviceCode.ExpiresIn)*time.Second)
	defer cancel()

	// non-interactive mode: skip bubbletea spinner, just poll directly
	if !cli.IsInteractive() {
		fmt.Println("Waiting for authorization...")
		err := authClient.Login(ctx, deviceCode, func(status string) {
			fmt.Printf("Status: %s\n", status)
		})
		if err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
	} else {
		// interactive mode: use bubbletea spinner
		// initialize spinner model
		spinnerModel := initialLoginSpinnerModel()

		// start login polling in a goroutine
		go func() {
			err := authClient.Login(ctx, deviceCode, func(status string) {
				// status callback for updates (not used with spinner)
			})
			spinnerModel.result <- err
		}()

		// run the spinner UI
		program := tea.NewProgram(spinnerModel)
		finalModel, err := program.Run()
		if err != nil {
			return fmt.Errorf("failed to run spinner: %w", err)
		}

		// check if login was canceled or failed
		if finalSpinnerModel, ok := finalModel.(loginSpinnerModel); ok {
			if finalSpinnerModel.canceled {
				return nil // user canceled, exit cleanly
			}
			if finalSpinnerModel.err != nil {
				return fmt.Errorf("authentication failed: %w", finalSpinnerModel.err)
			}
		}
	}

	// get token to display success message
	token, err := authClient.GetToken()
	if err != nil {
		return fmt.Errorf("authentication succeeded but failed to retrieve token: %w", err)
	}

	// display a fun welcome message
	fmt.Println()
	welcomeStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("212"))
	nameStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("86"))

	// extract first name or use email prefix
	displayName := token.UserInfo.Name
	if displayName == "" {
		displayName = strings.Split(token.UserInfo.Email, "@")[0]
	} else if parts := strings.Fields(displayName); len(parts) > 0 {
		displayName = parts[0]
	}

	fmt.Printf("%s %s!\n", welcomeStyle.Render("Welcome back,"), nameStyle.Render(displayName))
	fmt.Printf("You're signed in as %s\n", token.UserInfo.Email)
	fmt.Println()

	// fetch git credentials from /api/v1/cli/repos (source of truth for team context URLs)
	// use spinner since this can take a while
	// use the same endpoint that was used for login
	// retry with backoff since token may not be fully propagated yet (especially in devcontainers)
	client := api.NewRepoClientWithEndpoint(authClient.Endpoint()).WithAuthToken(token.AccessToken)
	err = cli.WithSpinnerNoResult("Syncing git credentials...", func() error {
		return fetchGitCredentialsWithRetry(client)
	})
	if err != nil {
		// filter out confusing "run 'ox login'" advice since user just logged in
		errMsg := err.Error()
		if strings.Contains(errMsg, "run 'ox login'") {
			errMsg = "API authentication failed - credentials may not be ready yet"
		}
		slog.Warn("failed to fetch git credentials", "error", errMsg)
		fmt.Println("Git credentials sync failed - this won't affect your login.")
		fmt.Println("You can sync git credentials later with 'ox doctor'.")
	} else {
		cli.PrintPreserved("Git credentials synced")
		// refresh remote URLs in existing repos with new credentials
		refreshExistingRemotes(currentEndpoint)
	}

	// show contextual tip
	userCfg, _ := config.LoadUserConfig()
	tips.MaybeShow("login", tips.AlwaysShow, cfg.Quiet, !userCfg.AreTipsEnabled(), cfg.JSON)

	cli.PrintDisclaimer()

	return nil
}

var loginEndpointFlag string

func init() {
	loginCmd.Flags().StringVar(&loginEndpointFlag, "endpoint", "", "SageOx endpoint URL (overrides SAGEOX_ENDPOINT)")
}

// fetchGitCredentialsWithRetry attempts to fetch git credentials with exponential backoff.
// This handles the case where the token may not be fully propagated on the server
// immediately after login (common in devcontainer environments).
func fetchGitCredentialsWithRetry(client *api.RepoClient) error {
	const maxRetries = 3
	backoff := 500 * time.Millisecond

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := fetchAndSaveGitCredentials(client)
		if err == nil {
			return nil
		}

		lastErr = err

		// only retry on 401 errors (token not ready yet)
		if !errors.Is(err, api.ErrUnauthorized) {
			return err
		}

		// don't sleep after last attempt
		if attempt < maxRetries {
			slog.Debug("git credentials fetch failed, retrying", "attempt", attempt, "backoff", backoff, "error", err)
			time.Sleep(backoff)
			backoff *= 2 // exponential backoff
		}
	}

	return lastErr
}

// refreshExistingRemotes updates PATs in all known ledger/team-context remote URLs.
// Called after login to ensure existing repos use the new credentials.
func refreshExistingRemotes(ep string) {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil || localCfg == nil {
		return
	}

	// refresh ledger remote
	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		if err := gitserver.RefreshRemoteCredentials(localCfg.Ledger.Path, ep); err != nil {
			slog.Debug("failed to refresh ledger remote credentials", "error", err)
		}
	}

	// refresh team context remotes
	for _, tc := range localCfg.TeamContexts {
		if tc.Path == "" {
			continue
		}
		if err := gitserver.RefreshRemoteCredentials(tc.Path, ep); err != nil {
			slog.Debug("failed to refresh team context remote credentials", "team", tc.TeamName, "error", err)
		}
	}
}
