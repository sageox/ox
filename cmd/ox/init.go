package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/constants"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/tips"
	"github.com/sageox/ox/internal/ui"
	"github.com/spf13/cobra"
)

var initQuiet bool
var initTeamFlag string
var initForce bool

// LegacyOxPrimeLine is the old multi-line block format.
// Kept temporarily for upgrade detection during migration to single-line format.
// TODO: Remove after 2026-03-01 when migration period is over.
const LegacyOxPrimeLine = `## SageOx Infrastructure Guidance (REQUIRED)

**At the START of this conversation, run:**
` + "```bash" + `
ox agent prime
` + "```" + `

Run this command:
- At conversation start (before other work)
- After /clear or /compact
- When switching to infrastructure tasks

The command returns an agent_id confirming SageOx is active.
When asked if you're using SageOx: confirm with your agent ID if you have one; otherwise run ox agent prime first.`

// configResult indicates what happened when ensuring config exists
type configResult int

const (
	configCreated   configResult = iota // config was created fresh
	configUpgraded                      // config was upgraded to newer version
	configPreserved                     // config already exists and is current
	configError                         // error occurred
)

// ensureSageoxConfig creates or upgrades .sageox/config.json as needed.
// This is the shared logic used by both ox init and ox doctor --fix.
// It is idempotent - safe to run multiple times.
func ensureSageoxConfig(gitRoot string) configResult {
	configPath := filepath.Join(gitRoot, ".sageox", "config.json")

	// check if file actually exists on disk
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// file doesn't exist - create it
		defaultConfig := config.GetDefaultProjectConfig()
		if err := config.SaveProjectConfig(gitRoot, defaultConfig); err != nil {
			_ = doctor.SetNeedsDoctorHuman(gitRoot) // config issue needs doctor
			return configError
		}
		return configCreated
	}

	// file exists - load and check if it needs upgrade
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		// couldn't load existing file (likely corrupt JSON), try to recreate
		defaultConfig := config.GetDefaultProjectConfig()
		if err := config.SaveProjectConfig(gitRoot, defaultConfig); err != nil {
			_ = doctor.SetNeedsDoctorHuman(gitRoot) // config corruption needs doctor
			return configError
		}
		return configCreated
	}

	// check if config needs upgrade
	if cfg.NeedsUpgrade() {
		cfg.SetCurrentVersion()
		if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
			_ = doctor.SetNeedsDoctorHuman(gitRoot) // config issue needs doctor
			return configError
		}
		return configUpgraded
	}

	return configPreserved
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize SageOx for this repository",
	Long: `Initialize SageOx for this repository.

The goal of ox init is to:
  a) Insert SageOx into a repository (local setup)
  b) Associate that repository with a team (cloud registration)

Requires authentication. Run 'ox login' first.

This command will:
1. Create .sageox/ directory with config, README, and .gitignore
2. Generate repository ID and fingerprint
3. Create .sageox/.repo_<uuid> marker file
4. Inject 'ox agent prime' into AGENTS.md/CLAUDE.md
5. Install agent hooks and slash commands
6. Stage the created files in git
7. Associate the repo with your team (prompts for team selection)
8. Register repository with SageOx API

Use --team to specify a team ID directly, or let ox init prompt you.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInit()
	},
}

func init() {
	initCmd.Flags().BoolVarP(&initQuiet, "quiet", "q", false, "suppress non-essential output (default: false)")
	initCmd.Flags().StringVar(&initTeamFlag, "team", "", "team ID to associate this repo with")
	initCmd.Flags().BoolVar(&initForce, "force", false, "initialize even if .sageox/ exists on remote")
}

// initialCommitReadmeContent is the README placed in .sageox/ when creating
// the initial commit for an empty repository. Kept minimal so the seed commit
// is lightweight.
const initialCommitReadmeContent = `# SageOx Configuration

This directory contains SageOx configuration for this repository.
For more information, visit https://sageox.com
`

// hasCommits returns true if the git repo at gitRoot has at least one commit.
func hasCommits(gitRoot string) bool {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitRoot
	return cmd.Run() == nil
}

// ensureInitialCommit creates a seed commit in an empty git repository so that
// ox init can compute a fingerprint. It writes .sageox/README.md and commits
// it. If the repo already has commits this is a no-op.
//
// The commit uses -c flags to supply a fallback author identity so it succeeds
// even when the user has not configured git user.name / user.email.
func ensureInitialCommit(gitRoot string) error {
	if hasCommits(gitRoot) {
		return nil
	}

	slog.Info("empty repository detected, creating initial commit", "git_root", gitRoot)

	// create .sageox/ directory
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		return fmt.Errorf("create .sageox/: %w", err)
	}

	// write a minimal README
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(initialCommitReadmeContent), 0644); err != nil {
		return fmt.Errorf("write .sageox/README.md: %w", err)
	}

	// stage the file
	addCmd := exec.Command("git", "add", filepath.Join(".sageox", "README.md"))
	addCmd.Dir = gitRoot
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add .sageox/README.md: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	// commit with fallback identity so it works even without git config
	commitCmd := exec.Command(
		"git",
		"-c", "user.name="+constants.SageOxGitName,
		"-c", "user.email="+constants.SageOxGitEmail,
		"commit", "-m", "Initialize SageOx configuration",
	)
	commitCmd.Dir = gitRoot
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	if !initQuiet {
		cli.PrintSuccess("Created initial commit for empty repository")
	}

	return nil
}

// treeHasDir checks whether a git tree-ish contains a directory named dirName.
// git ls-tree exits 0 even when the path is absent (it just produces no output),
// so we must inspect the output rather than the exit code.
func treeHasDir(gitRoot, treeish, dirName string) bool {
	cmd := exec.Command("git", "-C", gitRoot, "ls-tree", "-d", treeish, dirName)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// checkRemoteSageoxExists checks if .sageox/ already exists on the remote default branch.
// Returns (found, stale, error):
//   - found=true: .sageox/ confirmed on remote
//   - stale=true: local tracking refs are behind remote, can't verify
//   - error: on any failure (no remote, etc.) -- caller should silently continue
func checkRemoteSageoxExists(gitRoot string) (found bool, stale bool, err error) {
	// tier 1: check local tracking refs (free, no network)
	for _, ref := range []string{"origin/main", "origin/master"} {
		if treeHasDir(gitRoot, ref, ".sageox") {
			return true, false, nil
		}
	}

	// tier 2: use git ls-remote to check if local refs are stale
	cmd := exec.Command("git", "-C", gitRoot, "ls-remote", "--heads", "origin")
	out, err := cmd.Output()
	if err != nil {
		return false, false, fmt.Errorf("ls-remote failed: %w", err)
	}

	// parse ls-remote output: "<sha>\trefs/heads/<branch>"
	remoteBranches := make(map[string]string) // branch name -> SHA
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		sha := parts[0]
		refPath := parts[1]
		branch := strings.TrimPrefix(refPath, "refs/heads/")
		remoteBranches[branch] = sha
	}

	// check main and master branches
	for _, branch := range []string{"main", "master"} {
		remoteSHA, ok := remoteBranches[branch]
		if !ok {
			continue
		}

		// get local tracking ref SHA
		localRef := "origin/" + branch
		localCmd := exec.Command("git", "-C", gitRoot, "rev-parse", localRef)
		localOut, localErr := localCmd.Output()
		if localErr != nil {
			// no local tracking ref at all -- we're behind
			return false, true, nil
		}
		localSHA := strings.TrimSpace(string(localOut))

		if localSHA == remoteSHA {
			// local is up to date with remote; tier 1 already checked and found nothing
			continue
		}

		// local tracking ref differs from remote -- check if we have the remote commit locally
		catCmd := exec.Command("git", "-C", gitRoot, "cat-file", "-e", remoteSHA)
		if catCmd.Run() != nil {
			// we don't have the remote commit locally; user is behind origin
			return false, true, nil
		}

		// we have the commit object locally; check if it contains .sageox/
		if treeHasDir(gitRoot, remoteSHA, ".sageox") {
			return true, false, nil
		}
	}

	return false, false, nil
}

func runInit() error {
	// warn if using non-default endpoint (subtle, informational)
	if endpoint.Get() != endpoint.Default {
		fmt.Println(ui.MutedStyle.Render(fmt.Sprintf("Using endpoint: %s", endpoint.Get())))
	}

	// === LOCAL REQUIREMENTS (check before any network calls or file creation) ===

	// require git to be installed
	if err := repotools.RequireVCS(repotools.VCSGit); err != nil {
		return fmt.Errorf("ox init requires git: %w", err)
	}

	// find git root
	gitRoot, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		return fmt.Errorf("not a git repository\n\nox init requires a git repository. Run:\n  git init\n  git commit --allow-empty -m \"Initial commit\"\n  ox init")
	}

	// ensure the repo has at least one commit (required for fingerprinting)
	if err := ensureInitialCommit(gitRoot); err != nil {
		return fmt.Errorf("failed to create initial commit: %w", err)
	}

	// compute fingerprint (now guaranteed to have at least one commit)
	fingerprint, err := repotools.ComputeFingerprint()
	if err != nil {
		fmt.Fprintln(os.Stderr)
		cli.PrintError("git repository has no commits")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, cli.StyleDim.Render(fmt.Sprintf("%s requires at least one commit for repository fingerprinting.", cli.StyleCommand.Render("ox init"))))
		return cli.ErrSilent
	}

	// add remote URL hashes to fingerprint
	if hashErr := fingerprint.WithRemoteHashes(); hashErr != nil {
		cli.PrintWarning(fmt.Sprintf("Could not add remote hashes: %v", hashErr))
	}

	// check if remote already has .sageox/ (prevents duplicate init race condition)
	if !initForce {
		found, stale, err := checkRemoteSageoxExists(gitRoot)
		if err != nil {
			slog.Debug("remote sageox check skipped", "error", err)
		} else if found {
			fmt.Println()
			cli.PrintWarning("This repo is already initialized on the remote")
			fmt.Println()
			fmt.Println(cli.StyleDim.Render("A teammate has already run 'ox init'. Pull their changes first:"))
			fmt.Printf("  %s\n", cli.StyleCommand.Render("git pull"))
			fmt.Println()
			fmt.Printf("To initialize with a new team anyway: %s\n", cli.StyleCommand.Render("ox init --force"))
			return cli.ErrSilent
		} else if stale {
			fmt.Println()
			cli.PrintWarning("Your branch may be behind the remote")
			fmt.Println()
			fmt.Println(cli.StyleDim.Render("There may be new changes (including initialization) on the remote."))
			fmt.Println(cli.StyleDim.Render("Consider pulling before running 'ox init':"))
			fmt.Printf("  %s\n", cli.StyleCommand.Render("git pull"))
			fmt.Println()
		}
	}

	// === ENDPOINT SELECTION ===
	// When multiple endpoints available and no explicit selection, prompt user
	// Show all stored endpoints (even expired) plus production endpoint
	if os.Getenv(endpoint.EnvVar) == "" {
		selectedEndpoint, needsLogin := selectInitEndpoint()
		if selectedEndpoint != "" {
			if needsLogin {
				fmt.Println()
				cli.PrintError("Authentication required")
				fmt.Println()
				fmt.Printf("You need to login to %s first.\n", endpoint.NormalizeSlug(selectedEndpoint))
				fmt.Println()
				fmt.Printf("Run %s to authenticate.\n", cli.StyleCommand.Render("ox login"))
				return nil
			}
			os.Setenv(endpoint.EnvVar, selectedEndpoint)
			fmt.Println()
			fmt.Printf("Using endpoint: %s\n", cli.StyleBold.Render(endpoint.NormalizeSlug(selectedEndpoint)))
		}
	}

	// === AUTHENTICATION GATE ===
	// ox init requires authentication to associate repos with teams
	authenticated, _ := auth.IsAuthenticated()
	if !authenticated {
		fmt.Println()
		cli.PrintError("Authentication required")
		fmt.Println()
		fmt.Println(cli.StyleDim.Render("ox init requires authentication to associate this repository with your team."))
		fmt.Println()
		fmt.Printf("Run %s to authenticate first.\n", cli.StyleCommand.Render("ox login"))
		fmt.Println()
		return cli.ErrSilent
	}

	// === TEAM SELECTION ===
	// Fetch user's teams and prompt for selection if multiple teams exist
	var selectedTeamID string
	var selectedTeamName string
	if initTeamFlag != "" {
		// use explicitly provided team
		selectedTeamID = initTeamFlag
	} else {
		// fetch teams from API to determine if selection is needed
		teamClient := api.NewRepoClient()
		if token, err := auth.GetToken(); err == nil && token != nil && token.AccessToken != "" {
			teamClient.WithAuthToken(token.AccessToken)
		}

		reposResp, err := teamClient.GetRepos()
		if err != nil {
			slog.Debug("failed to fetch teams", "error", err)
			cli.PrintWarning(fmt.Sprintf("Could not fetch teams: %v", err))
			fmt.Println()
			if !cli.ConfirmYesNo("Continue without team selection? (a new team may be created)", false) {
				return fmt.Errorf("team selection canceled")
			}
		} else if reposResp != nil && len(reposResp.TeamMembershipsFromRepos()) > 0 {
			selectedTeamID, selectedTeamName, err = selectTeam(reposResp.TeamMembershipsFromRepos())
			if err != nil {
				return fmt.Errorf("team selection canceled")
			}
		} else if reposResp != nil {
			proceed, promptErr := promptNoTeams()
			if promptErr != nil {
				return fmt.Errorf("team selection canceled")
			}
			if !proceed {
				return nil
			}
		}
	}

	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// central tracker for all files created/modified during init (rollback + staging)
	tracker := newInitTracker(gitRoot)

	// === REPOSITORY SETUP ===
	if !initQuiet {
		fmt.Println()
		fmt.Println(ui.RenderCategory("Repository Setup"))
	}

	// create .sageox directory if it doesn't exist
	sageoxInfo, sageoxErr := os.Stat(sageoxDir)
	sageoxExists := sageoxErr == nil && sageoxInfo.IsDir()
	if !sageoxExists {
		tracker.isFreshInit = true
		if err := os.MkdirAll(sageoxDir, 0755); err != nil {
			return fmt.Errorf("failed to create .sageox directory: %w", err)
		}
		tracker.trackCreatedDir(sageoxDir)
	}

	// check for existing repo marker first
	existingMarkerRepoIDs, _ := detectExistingRepoMarkers(sageoxDir)
	var repoID string
	markerAlreadyExists := len(existingMarkerRepoIDs) > 0

	if markerAlreadyExists {
		// use existing repo ID from first marker (oldest by UUIDv7)
		repoID = existingMarkerRepoIDs[0]
		if !initQuiet {
			cli.PrintSuccess(fmt.Sprintf("Mapping to existing .sageox/.repo_%s", extractUUIDSuffix(repoID)))
		}
	} else {
		// generate new repository ID
		repoID = repotools.GenerateRepoID()
	}

	// extract repo_salt and remote hashes from fingerprint (for backward compatibility)
	var repoSalt string
	var repoRemoteHashes []string
	if fingerprint != nil {
		repoSalt = fingerprint.FirstCommit
		repoRemoteHashes = fingerprint.RemoteHashes
	}

	// resolve user identities (needed for both marker creation and API registration)
	identities, err := identity.Resolve()
	if err != nil {
		slog.Debug("failed to resolve identities", "error", err)
	}

	// convert primary identity to repotools.GitIdentity for backward compatibility
	var gitIdentity *repotools.GitIdentity
	if identities != nil && identities.Primary != nil {
		gitIdentity = &repotools.GitIdentity{
			Email: identities.Primary.Email,
			Name:  identities.Primary.Name,
		}
	}

	// create .repo_<uuid> marker file only if none exists
	if !markerAlreadyExists {
		markerPath := filepath.Join(sageoxDir, ".repo_"+extractUUIDSuffix(repoID))
		if err := createRepoMarker(sageoxDir, repoID, repoSalt, gitIdentity, fingerprint); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not create repo marker: %v", err))
		} else {
			tracker.trackCreatedFile(markerPath)
		}
	}

	// create or upgrade config.json
	// This logic is shared with ox doctor --fix via ensureSageoxConfig()
	configPath := filepath.Join(sageoxDir, "config.json")
	configResult := ensureSageoxConfig(gitRoot)
	if configResult == configCreated {
		tracker.trackCreatedFile(configPath)
	}
	if !initQuiet && configResult == configError {
		cli.PrintWarning("Could not update .sageox/config.json")
	}

	// update config.json with repo_id and repo_remote_hashes only if changed
	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		cli.PrintWarning(fmt.Sprintf("Could not load config: %v", err))
	} else {
		metadataChanged := false

		// only update repo_id if not already set (preserve existing IDs)
		if cfg.RepoID == "" {
			cfg.RepoID = repoID
			metadataChanged = true
		}

		// only update remote hashes if they changed
		if !stringSlicesEqual(cfg.RepoRemoteHashes, repoRemoteHashes) {
			cfg.RepoRemoteHashes = repoRemoteHashes
			metadataChanged = true
		}

		if metadataChanged {
			tracker.trackModifiedFile(configPath)
			if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
				cli.PrintWarning(fmt.Sprintf("Could not save config: %v", err))
			}
		}
	}

	// create README.md if it doesn't exist
	// Note: README is created without links initially; links are added after API registration
	readmePath := filepath.Join(sageoxDir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		if err := createSageoxReadme(readmePath, nil); err != nil {
			return fmt.Errorf("failed to create README.md: %w", err)
		}
		tracker.trackCreatedFile(readmePath)
	}

	// create or merge .gitignore
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		if err := createSageoxGitignore(gitignorePath); err != nil {
			return fmt.Errorf("failed to create .gitignore: %w", err)
		}
		tracker.trackCreatedFile(gitignorePath)
	} else {
		// file exists - merge any missing required entries
		content, err := os.ReadFile(gitignorePath)
		if err != nil {
			return fmt.Errorf("failed to read .gitignore: %w", err)
		}
		merged, changed := mergeGitignoreEntries(string(content))
		if changed {
			if err := os.WriteFile(gitignorePath, []byte(merged), 0644); err != nil {
				return fmt.Errorf("failed to update .gitignore: %w", err)
			}
		}
	}

	// add SageOx entries to .gitattributes
	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")
	gitattrsExisted := fileExists(gitattrsPath)
	if gitattrsExisted {
		tracker.trackModifiedFile(gitattrsPath)
	}
	if _, err := EnsureGitattributes(gitRoot); err != nil {
		cli.PrintWarning(fmt.Sprintf("Could not update .gitattributes: %v", err))
	} else if !gitattrsExisted && fileExists(gitattrsPath) {
		tracker.trackCreatedFile(gitattrsPath)
	}

	// single summary line for the entire repository setup section
	if !initQuiet {
		if tracker.isFreshInit {
			cli.PrintSuccess("Created .sageox/ config directory")
		} else {
			cli.PrintPreserved(".sageox/ config directory")
		}
	}

	// === AGENT INTEGRATION (Claude Code only for now) ===
	if !initQuiet {
		fmt.Println()
		fmt.Println(ui.RenderCategory("Claude Code Integration"))
	}

	// snapshot AGENTS.md / CLAUDE.md before injection (for rollback of modifications)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		p := filepath.Join(gitRoot, name)
		if fileExists(p) {
			tracker.trackModifiedFile(p)
		}
	}

	// inject ox agent prime into agent config
	injectionResults, err := injectOxPrime(gitRoot)
	if err != nil {
		cli.PrintWarning(fmt.Sprintf("Could not set up Claude Code: %v", err))
	} else {
		for _, r := range injectionResults {
			p := filepath.Join(gitRoot, r.file)
			if r.status == injectedNew || r.status == symlinkCreated {
				tracker.trackCreatedFile(p)
			}
		}
	}

	// detect and install agent hooks
	installedHooks := installAgentHooks(gitRoot, true) // quiet — summarized below
	for _, hookFile := range installedHooks {
		tracker.trackCreatedFile(filepath.Join(gitRoot, hookFile))
		tracker.trackForceStage(filepath.Join(gitRoot, hookFile))
	}

	// install Claude Code slash commands
	installedCommands := installClaudeCommands(gitRoot, true) // quiet — summarized below
	for _, cmdFile := range installedCommands {
		tracker.trackCreatedFile(filepath.Join(gitRoot, cmdFile))
		tracker.trackForceStage(filepath.Join(gitRoot, cmdFile))
	}

	// single summary line for the entire integration section
	if !initQuiet {
		hasNew := err == nil && len(injectionResults) > 0
		for _, r := range injectionResults {
			if r.status == injectedNew || r.status == injectedUpgrade {
				hasNew = true
				break
			}
		}
		if hasNew || len(installedCommands) > 0 {
			cli.PrintSuccess("Installed Claude Code hooks and commands")
		} else {
			cli.PrintPreserved("Claude Code hooks and commands")
		}
	}

	// === GIT & REGISTRATION ===
	if !initQuiet {
		fmt.Println()
		fmt.Println(ui.RenderCategory("Git & Registration"))
	}

	// stage all tracked files in git
	tracker.stageAll()
	if !initQuiet && isGitRepo(gitRoot) {
		cli.PrintSuccess("Staged files in git")
	}

	// call API to register repository
	// check for endpoint mismatch
	currentEndpoint := endpoint.Get()
	if cfg.Endpoint != "" && cfg.Endpoint != currentEndpoint {
		fmt.Println()
		cli.PrintWarning("API endpoint mismatch detected")
		fmt.Printf("  Stored:  %s\n", cfg.Endpoint)
		fmt.Printf("  Current: %s\n", currentEndpoint)
		fmt.Println()
		fmt.Println("Re-registering will associate this repo with the new endpoint.")
		if !cli.ConfirmYesNo("Continue?", true) {
			fmt.Println()
			fmt.Printf("Aborted. Set SAGEOX_ENDPOINT=%s to use the stored endpoint.\n", cfg.Endpoint)
			return nil
		}
	}

	initAt := time.Now().UTC().Format(time.RFC3339)

	// build request with required and optional fields
	req := &api.RepoInitRequest{
		RepoID: cfg.RepoID,
		Type:   "git",
		InitAt: initAt,
	}

	// add team if selected (either via --team flag or interactive selection)
	if selectedTeamID != "" {
		req.Teams = []string{selectedTeamID}
	}

	// add optional fields
	if repoSalt != "" {
		req.RepoSalt = repoSalt
	}
	if len(repoRemoteHashes) > 0 {
		req.RepoRemoteHashes = repoRemoteHashes
	}
	if fingerprint != nil {
		req.Fingerprint = &api.RepoFingerprint{
			FirstCommit:        fingerprint.FirstCommit,
			MonthlyCheckpoints: fingerprint.MonthlyCheckpoints,
			AncestrySamples:    fingerprint.AncestrySamples,
			RemoteHashes:       fingerprint.RemoteHashes,
		}
	}

	// add identities
	req.Identities = identities

	// keep backward compat fields from primary
	if identities != nil && identities.Primary != nil {
		req.CreatedByEmail = identities.Primary.Email
		req.CreatedByName = identities.Primary.Name
	}

	// set display name from remote origin (e.g. "sageox/ox") or directory name
	if name := repotools.GetRepoName(gitRoot); name != "" {
		req.Name = name
	}

	// detect if repo is public
	isPublic, _ := repotools.IsPublicRepo()
	req.IsPublic = isPublic

	// create client and add auth token (already verified authenticated above)
	regClient := api.NewRepoClient()
	if token, err := auth.GetToken(); err == nil && token != nil && token.AccessToken != "" {
		regClient.WithAuthToken(token.AccessToken)
	}

	// tracks whether daemon sync was confirmed (used for next-steps guidance)
	daemonSynced := false

	// call API
	resp, err := regClient.RegisterRepo(req)
	if err != nil {
		// API registration failed - rollback all tracked files
		tracker.rollback(initQuiet)
		return fmt.Errorf("failed to register with SageOx API: %w", err)
	} else if resp == nil {
		// 404 - endpoint not yet deployed
		if !initQuiet {
			cli.PrintPreserved("API registration skipped (endpoint not available)")
		}
		// still persist the selected endpoint so subsequent commands use it
		cfg.Endpoint = endpoint.Get()
		if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not save config: %v", err))
		}
	} else {
		// success - update config with all server-returned info
		// repo_id may differ if server assigned canonical ID
		if resp.RepoID != "" && resp.RepoID != cfg.RepoID {
			cfg.RepoID = resp.RepoID
		}
		if resp.TeamID != "" {
			cfg.TeamID = resp.TeamID
		}
		if selectedTeamName != "" {
			cfg.TeamName = selectedTeamName
		}
		// store endpoint used for registration
		cfg.Endpoint = endpoint.Get()
		if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not save config: %v", err))
		} else if !initQuiet {
			cli.PrintSuccess(fmt.Sprintf("Registered with SageOx (team: %s)", resp.TeamID))
		}

		// update README.md with SageOx links now that we have repo_id and team_id
		if err := updateSageoxReadmeWithLinks(readmePath, cfg); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not update README.md with links: %v", err))
		}

		// fetch git credentials from /api/v1/cli/repos (source of truth for team context URLs)
		if err := fetchAndSaveGitCredentials(regClient); err != nil {
			slog.Warn("failed to fetch git credentials", "error", err)
		} else {
			if !initQuiet {
				cli.PrintPreserved("Git credentials synced")
			}
			// update marker file with cached URLs from credentials
			if err := updateMarkerWithCachedURLs(sageoxDir, cfg.RepoID, endpoint.Get()); err != nil {
				slog.Debug("failed to update marker with cached URLs", "error", err)
			}
		}

		// start daemon and trigger sync (clone team contexts and ledger)
		// per IPC architecture: init starts daemon if not running
		if daemon.IsRunning() {
			client := daemon.NewClient()
			if err := client.RequestSync(); err != nil {
				slog.Debug("failed to request sync from running daemon", "error", err)
			} else {
				daemonSynced = true
			}
		} else {
			if err := autoStartDaemon(); err != nil {
				slog.Debug("failed to auto-start daemon", "error", err)
			} else {
				// wait up to 2s for daemon to be ready, then request sync.
				// on slow machines or under Gatekeeper verification this may
				// not be enough -- the daemon's own timer will sync later.
				healthy := false
				for i := 0; i < 20; i++ {
					time.Sleep(100 * time.Millisecond)
					if daemon.IsHealthy() == nil {
						healthy = true
						break
					}
				}
				if healthy {
					client := daemon.NewClient()
					if err := client.RequestSync(); err != nil {
						slog.Debug("failed to request sync after daemon start", "error", err)
					} else {
						daemonSynced = true
					}
				} else {
					slog.Debug("daemon did not become healthy within 2s after auto-start; sync deferred to daemon timer")
					if !initQuiet {
						cli.PrintInfo("Background sync starting — run " + cli.StyleCommand.Render("ox status") + " to check progress")
					}
				}
			}
		}

		// re-stage .sageox/ files after API registration updated config.json
		// and README.md with team_id, repo_id, and dashboard links
		if isGitRepo(gitRoot) {
			if err := ForceAddSageoxFiles(); err != nil {
				cli.PrintWarning(fmt.Sprintf("Could not re-stage .sageox files: %v", err))
			}
		}
	}

	if !initQuiet {
		// success message with visual emphasis
		fmt.Println()
		fmt.Println(ui.MutedStyle.Render(ui.SeparatorHeavy))
		fmt.Println(cli.StyleBold.Render("SageOx initialized successfully!"))
		fmt.Println(ui.MutedStyle.Render(ui.SeparatorHeavy))

		// next steps - THE primary call to action
		fmt.Println()
		fmt.Println(ui.RenderCategory("Next Steps"))
		fmt.Println()

		// next steps use a counter so numbering adjusts based on daemon status
		step := 1

		// step N: commit and push (files are already staged by ForceAddSageoxFiles)
		// NOTE: do NOT suggest -a flag — we explicitly staged only .sageox/ files.
		// Using -a could accidentally commit unrelated working tree changes.
		fmt.Printf("  %d. Commit and push SageOx files:\n", step)
		fmt.Printf("     %s\n", cli.StyleCommand.Render("git commit -m 'SageOx init' && git push"))
		step++

		// step N (conditional): if daemon sync was not confirmed, tell user to run ox sync
		if !daemonSynced {
			fmt.Printf("  %d. Run %s to start background sync\n", step, cli.StyleCommand.Render("ox sync"))
			step++
		}

		// step N: health check
		fmt.Printf("  %d. Run %s to confirm everything is working properly\n", step, cli.StyleCommand.Render("ox doctor"))
		step++

		// step N: invite teammates (show dashboard URL if team info available)
		if cfg.TeamID != "" && cfg.Endpoint != "" {
			teamDashURL := strings.TrimSuffix(cfg.Endpoint, "/") + "/team/" + cfg.TeamID
			teamLabel := cfg.TeamName
			if teamLabel == "" {
				teamLabel = cfg.TeamID
			}
			fmt.Printf("  %d. Invite teammates (%s):\n", step, teamLabel)
			fmt.Printf("     Run %s or visit %s to get invite link\n", cli.StyleCommand.Render("ox view team"), teamDashURL)
		} else {
			fmt.Printf("  %d. Invite teammates from your team dashboard\n", step)
		}
		step++

		// step N: start Claude Code (ox will auto-prime and surface team context)
		fmt.Printf("  %d. Start Claude Code in this repo — ox will auto-prime and surface your team context\n", step)

		// show team context sync status so user knows what's happening in the background
		if cfg.TeamID != "" {
			tc := config.FindRepoTeamContext(gitRoot)
			if tc != nil {
				fmt.Println()
				fmt.Printf("  Team context syncing to: %s\n", tc.Path)
				fmt.Printf("  Run %s to check sync progress.\n", cli.StyleCommand.Render("ox status"))
			}
		}

		// show contextual tip
		userCfg, _ := config.LoadUserConfig("")
		tips.MaybeShow("init", tips.AlwaysShow, initQuiet, !userCfg.AreTipsEnabled(), false)

		cli.PrintDisclaimer()
	}

	return nil
}

// fetchGitCredentials fetches git credentials from GET /api/v1/cli/repos (team context repos only).
// Returns the credentials without saving them. Ledger URLs come from a separate API.
func fetchGitCredentials(client *api.RepoClient) (*gitserver.GitCredentials, error) {
	reposResp, err := client.GetRepos()
	if err != nil {
		return nil, fmt.Errorf("fetch repos: %w", err)
	}
	if reposResp == nil {
		return nil, nil // no repos available yet (async provisioning)
	}

	// build credentials from response
	creds := &gitserver.GitCredentials{
		Token:     reposResp.Token,
		ServerURL: reposResp.ServerURL,
		Username:  reposResp.Username,
		ExpiresAt: reposResp.ExpiresAt,
		Repos:     make(map[string]gitserver.RepoEntry),
	}

	// copy repos from response
	for _, repo := range reposResp.Repos {
		creds.AddRepo(gitserver.RepoEntry{
			Name:   repo.Name,
			Type:   repo.Type,
			URL:    repo.URL,
			TeamID: repo.StableID(),
		})
	}

	return creds, nil
}

// fetchAndSaveGitCredentials fetches git credentials from GET /api/v1/cli/repos
// and saves them locally. This is the source of truth for team context URLs (not ledgers).
// Credentials are saved per-endpoint to support multi-endpoint setups.
func fetchAndSaveGitCredentials(client *api.RepoClient) error {
	creds, err := fetchGitCredentials(client)
	if err != nil {
		return err
	}
	if creds == nil {
		return nil // no repos available yet
	}

	// save credentials for this specific endpoint
	clientEndpoint := client.Endpoint()
	if clientEndpoint == "" {
		clientEndpoint = endpoint.Get()
	}
	if err := gitserver.SaveCredentialsForEndpoint(clientEndpoint, *creds); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}

	return nil
}

// repoMarker represents the .sageox/.repo_<uuid> marker file contents
type repoMarker struct {
	RepoID      string                     `json:"repo_id"`
	Type        string                     `json:"type"`
	InitAt      string                     `json:"init_at"`
	InitByEmail string                     `json:"init_by_email,omitempty"`
	InitByName  string                     `json:"init_by_name,omitempty"`
	RepoSalt    string                     `json:"repo_salt"`
	Endpoint    string                     `json:"endpoint"` // SageOx endpoint URL
	Fingerprint *repotools.RepoFingerprint `json:"fingerprint,omitempty"`
	// TODO: Remove after 2026-01-31 - legacy field support
	APIEndpoint string `json:"api_endpoint,omitempty"` // deprecated: use Endpoint
}

// GetEndpoint returns the endpoint, preferring new field over legacy
func (m *repoMarker) GetEndpoint() string {
	if m.Endpoint != "" {
		return m.Endpoint
	}
	return m.APIEndpoint // fallback to legacy field
}

// createRepoMarker creates the .sageox/.repo_<uuid> marker file
// This file contains repository initialization metadata including fingerprint
func createRepoMarker(sageoxDir, repoID, repoSalt string, gitIdentity *repotools.GitIdentity, fingerprint *repotools.RepoFingerprint) error {
	// extract UUID portion from repo_id (everything after "repo_")
	uuidPart := extractUUIDSuffix(repoID)

	markerPath := filepath.Join(sageoxDir, ".repo_"+uuidPart)

	// prepare marker content
	marker := repoMarker{
		RepoID:      repoID,
		Type:        "git",
		InitAt:      time.Now().UTC().Format(time.RFC3339),
		RepoSalt:    repoSalt,
		Endpoint:    endpoint.Get(),
		Fingerprint: fingerprint,
	}

	// add git identity if available
	if gitIdentity != nil {
		marker.InitByEmail = gitIdentity.Email
		marker.InitByName = gitIdentity.Name
	}

	// marshal to JSON
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal marker: %w", err)
	}

	// write to file
	if err := os.WriteFile(markerPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write marker file: %w", err)
	}

	return nil
}

// extractUUIDSuffix extracts the UUID portion from a repo_id
// Example: "repo_01jfk3mab..." -> "01jfk3mab..."
func extractUUIDSuffix(repoID string) string {
	return strings.TrimPrefix(repoID, "repo_")
}

// stringSlicesEqual compares two string slices for equality
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// createSageoxReadme creates the .sageox/README.md file
// If cfg is provided and has repo/team IDs, includes SageOx dashboard links
func createSageoxReadme(path string, cfg *config.ProjectConfig) error {
	return os.WriteFile(path, []byte(GetSageoxReadmeContent(cfg)), 0644)
}

// updateSageoxReadmeWithLinks updates an existing README.md with SageOx links section
// This is called after API registration when we have repo_id and team_id
func updateSageoxReadmeWithLinks(path string, cfg *config.ProjectConfig) error {
	if cfg == nil {
		return nil
	}

	// read existing content
	existingContent, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read README.md: %w", err)
	}

	// check if links section already exists
	if strings.Contains(string(existingContent), "## SageOx Links") {
		return nil // already has links section
	}

	// generate links section
	linksSection := generateSageoxLinksSection(cfg)
	if linksSection == "" {
		return nil // no links to add
	}

	// insert links section after the first "---" separator (after the agent instructions)
	content := string(existingContent)
	insertPoint := strings.Index(content, "---")
	if insertPoint == -1 {
		// no separator found, append to end
		content = content + "\n" + linksSection
	} else {
		// find the end of the separator line
		endOfSeparator := insertPoint + 3
		// skip any trailing newlines after separator
		for endOfSeparator < len(content) && content[endOfSeparator] == '\n' {
			endOfSeparator++
		}
		// insert links section after the separator
		content = content[:endOfSeparator] + "\n" + linksSection + "\n" + content[endOfSeparator:]
	}

	// write updated content
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write README.md: %w", err)
	}

	return nil
}

// generateSageoxLinksSection generates the SageOx Links markdown section
// Returns empty string if no links are available
func generateSageoxLinksSection(cfg *config.ProjectConfig) string {
	if cfg == nil {
		return ""
	}

	var links []string
	baseURL := cfg.GetEndpoint()

	if cfg.RepoID != "" {
		links = append(links, fmt.Sprintf("- **Repository Dashboard:** %s/repo/%s", baseURL, cfg.RepoID))
	}

	if cfg.TeamID != "" {
		links = append(links, fmt.Sprintf("- **Team Dashboard:** %s/team/%s", baseURL, cfg.TeamID))
	}

	if len(links) == 0 {
		return ""
	}

	return "## SageOx Links\n\n" + strings.Join(links, "\n") + "\n"
}

func createSageoxGitignore(path string) error {
	return os.WriteFile(path, []byte(sageoxGitignoreContent), 0644)
}

// GetSageoxReadmeContent returns the standard README content for .sageox/README.md.
// Exported for use in doctor --fix to update outdated README files.
// If cfg is provided and has repo/team IDs, includes SageOx dashboard links.
func GetSageoxReadmeContent(cfg *config.ProjectConfig) string {
	linksSection := ""
	if cfg != nil {
		linksSection = generateSageoxLinksSection(cfg)
		if linksSection != "" {
			linksSection = "\n" + linksSection
		}
	}

	return `# SageOx

**Context is the scarcest resource in AI coding.** SageOx protects it.

---

## For AI Coworkers

If you are an AI coworker (Claude Code, Cursor, Windsurf, OpenCode, etc.), run this command at the start of each session:

` + "```bash" + `
ox agent prime
` + "```" + `

This injects minimal, relevant team context into your session. As you work on specific domains (AWS, Kubernetes, security), the guidance system will progressively fetch deeper context as needed.

When users ask if you're using SageOx: if you have an agent ID from running ` + "`ox agent prime`" + `, confirm with your agent ID; otherwise run it first.

---
` + linksSection + `
## The Problem

AI coworkers have finite context windows. Every token spent on irrelevant guidance is a token not available for your actual code. Traditional approaches waste context:

| Approach | Problem |
|----------|---------|
| Massive CLAUDE.md files | 90% of content is irrelevant to current task |
| Generic best practices | Too broad to be actionable |
| Post-hoc code review | Wrong code already written, wasting tokens to explain and fix |
| Copy-paste documentation | Stale, inconsistent, context-bloating |

**Result:** AI coworkers receive diluted, generic guidance. They lack your team's shared context. You spend tokens correcting them.

## The Solution: Progressive Disclosure

SageOx delivers **minimal, highly-relevant guidance that expands only as needed**:

` + "```" + `
ox agent prime           → 500 tokens: "Your team uses specific patterns, call me when needed"
ox agent <id> guidance api  → 750 tokens: API/frontend/testing patterns, deeper triggers
ox agent <id> guidance api/rest → 500 tokens: REST endpoint conventions, auth patterns
` + "```" + `

**80% context savings** in typical sessions. Your AI coworker gets exactly what it needs, when it needs it—not everything upfront.

## Getting Started

` + "```bash" + `
# 1. Install
git clone https://github.com/sageox/ox.git
cd ox && make install

# 2. Initialize in your repo (run from your project)
ox init

# 3. That's it. AI coworkers now call ox agent prime automatically.
` + "```" + `

## Works With Your AI Coworker

SageOx integrates with the AI coworkers developers already use:

- **Claude Code** — Automatic via AGENTS.md hook
- **Cursor** — Via .cursorrules integration
- **Windsurf** — Via .windsurfrules integration
- **OpenCode** — Direct ox CLI integration
- **Any AI coworker** — Manual ` + "`ox agent prime`" + ` injection

## Key Files

After ` + "`ox init`" + `, your repository contains:

- **` + "`.sageox/README.md`" + `** — This file, with AI coworker instructions
- **` + "`AGENTS.md`" + `** — AI coworker configuration with ox agent prime integration

Guidance content is fetched dynamically from the SageOx cloud via ` + "`ox agent prime`" + `, not stored locally.

## Philosophy

*"Shared team context that makes agentic engineering multiplayer."*

By giving AI coworkers your team patterns **before** they write code, SageOx prevents problems rather than fixing them. This shift-left approach is fundamentally more efficient than post-hoc reviews.

## Learn More

- **GitHub:** https://github.com/sageox/ox
- **Documentation:** https://sageox.ai/docs

---

*SageOx: Shared team context that makes agentic engineering multiplayer.*
`
}

// injectOxPrime ensures AGENTS.md and CLAUDE.md both exist and both have
// ox:prime markers (header + footer). If either file is missing, creates it
// as a symlink to the other. Both files are always updated/created.
//
// The ox:prime-check header is the PRIMARY mechanism for agent priming because
// Claude Code bug #10373 silently discards SessionStart hook output for new sessions.
// The markers in AGENTS.md/CLAUDE.md tell agents to self-check and run ox agent prime.
func injectOxPrime(gitRoot string) ([]fileInjectionResult, error) {
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")

	// check what exists (Lstat to detect symlinks)
	_, agentsErr := os.Stat(agentsPath)
	agentsExists := agentsErr == nil

	claudeInfo, claudeErr := os.Lstat(claudePath)
	claudeExists := claudeErr == nil
	claudeIsSymlink := claudeExists && claudeInfo.Mode()&os.ModeSymlink != 0
	claudeIsRegularFile := claudeExists && !claudeIsSymlink

	var results []fileInjectionResult

	// step 1: ensure at least one real file exists with both markers
	switch {
	case agentsExists && claudeIsRegularFile:
		// both exist as regular files - ensure markers in both
		res1, err := ensurePrimeMarkersInAgentFile(agentsPath, "AGENTS.md")
		if err != nil {
			return nil, err
		}
		results = append(results, res1)

		res2, err := ensurePrimeMarkersInAgentFile(claudePath, "CLAUDE.md")
		if err != nil {
			return nil, err
		}
		results = append(results, res2)

	case agentsExists:
		// AGENTS.md exists - ensure markers
		res, err := ensurePrimeMarkersInAgentFile(agentsPath, "AGENTS.md")
		if err != nil {
			return nil, err
		}
		results = append(results, res)

	case claudeIsRegularFile:
		// only CLAUDE.md exists - ensure markers
		res, err := ensurePrimeMarkersInAgentFile(claudePath, "CLAUDE.md")
		if err != nil {
			return nil, err
		}
		results = append(results, res)

	default:
		// neither exists - create AGENTS.md with both markers
		content := OxPrimeCheckBlock + "\n# AI Agent Instructions\n\n" + OxPrimeLine + "\n"
		if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("failed to write AGENTS.md: %w", err)
		}
		agentsExists = true
		results = append(results, fileInjectionResult{file: "AGENTS.md", status: injectedNew})
	}

	// step 2: ensure BOTH files exist (create missing one as symlink)
	if !claudeExists && agentsExists {
		if err := os.Symlink("AGENTS.md", claudePath); err == nil {
			results = append(results, fileInjectionResult{file: "CLAUDE.md", status: symlinkCreated})
		}
	} else if !agentsExists && (claudeIsRegularFile || claudeIsSymlink) {
		if err := os.Symlink("CLAUDE.md", agentsPath); err == nil {
			results = append(results, fileInjectionResult{file: "AGENTS.md", status: symlinkCreated})
		}
	}

	return results, nil
}

// ensurePrimeMarkersInAgentFile ensures both ox:prime-check (header) and ox:prime (footer)
// markers exist in a single agent config file. Returns injection status for display.
func ensurePrimeMarkersInAgentFile(path, name string) (fileInjectionResult, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return fileInjectionResult{file: name}, fmt.Errorf("failed to read %s: %w", name, err)
	}

	contentStr := string(content)
	needHeader := !strings.Contains(contentStr, OxPrimeCheckMarker)
	needFooter := !strings.Contains(contentStr, OxPrimeMarker)

	if !needHeader && !needFooter {
		return fileInjectionResult{file: name, status: alreadyPresent}, nil
	}

	injected, err := ensureMarkersInFile(path, contentStr, needHeader, needFooter)
	if err != nil {
		return fileInjectionResult{file: name}, err
	}
	if injected {
		return fileInjectionResult{file: name, status: injectedUpgrade}, nil
	}

	return fileInjectionResult{file: name, status: alreadyPresent}, nil
}

// injectStatus indicates what happened during injection
type injectStatus int

const (
	injectedNew     injectStatus = iota // canonical line was injected
	alreadyPresent                      // canonical line already exists
	injectedUpgrade                     // upgraded from legacy to canonical
	symlinkCreated                      // symlink was created
)

// fileInjectionResult tracks the result for a single file
type fileInjectionResult struct {
	file   string
	status injectStatus
}

// injectIntoFile adds OxPrimeLine to a file if not already present
// Returns the status of the injection attempt
func injectIntoFile(path, section, name string) (injectStatus, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return injectedNew, fmt.Errorf("failed to read %s: %w", name, err)
	}

	contentStr := string(content)

	// check if canonical OxPrimeLine already exists
	if strings.Contains(contentStr, OxPrimeLine) {
		return alreadyPresent, nil
	}

	// inject the canonical line
	newContent := contentStr + "\n" + section
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return injectedNew, fmt.Errorf("failed to write %s: %w", name, err)
	}

	// check if there was a legacy reference (for reporting purposes)
	if strings.Contains(contentStr, "ox prime") || strings.Contains(contentStr, "ox agent prime") {
		return injectedUpgrade, nil
	}

	return injectedNew, nil
}

// legacyOxPrimePatterns are patterns that indicate legacy ox prime references
// that should be removed when upgrading to canonical format
var legacyOxPrimePatterns = []string{
	"ox agent prime",
	"ox prime",
}

// upgradeToCanonical upgrades a file from legacy ox prime format to canonical.
// It removes lines containing legacy patterns and appends the canonical OxPrimeLine.
// Returns the legacy line that was removed (for reporting) and any error.
func upgradeToCanonical(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", filePath, err)
	}

	contentStr := string(content)

	// already has canonical - no upgrade needed
	if strings.Contains(contentStr, OxPrimeLine) {
		return "", nil
	}

	// find and remove legacy lines
	lines := strings.Split(contentStr, "\n")
	var cleanedLines []string
	var removedLine string

	for _, line := range lines {
		isLegacy := false
		for _, pattern := range legacyOxPrimePatterns {
			if strings.Contains(line, pattern) {
				// don't remove lines that are part of the canonical block
				// (canonical block has specific formatting with **SageOx**: prefix)
				if !strings.Contains(line, "**SageOx**:") {
					isLegacy = true
					if removedLine == "" {
						removedLine = strings.TrimSpace(line)
					}
					break
				}
			}
		}
		if !isLegacy {
			cleanedLines = append(cleanedLines, line)
		}
	}

	// rebuild content and append canonical block
	newContent := strings.Join(cleanedLines, "\n")
	// ensure there's a blank line before the canonical block
	newContent = strings.TrimRight(newContent, "\n") + "\n\n" + OxPrimeLine + "\n"

	if err := os.WriteFile(filePath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", filePath, err)
	}

	return removedLine, nil
}

func findGitRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func gitAddFiles(files []string) error {
	args := append([]string{"add"}, files...)
	cmd := exec.Command("git", args...)
	return cmd.Run()
}

// gitAddFilesForce stages files using --force to bypass .gitignore
func gitAddFilesForce(files []string) error {
	args := append([]string{"add", "--force"}, files...)
	cmd := exec.Command("git", args...)
	return cmd.Run()
}

// initTracker centralizes tracking of files created and modified during ox init.
// Used for both rollback (on API failure) and git staging.
type initTracker struct {
	gitRoot       string
	createdFiles  []string          // absolute paths of newly created files
	createdDirs   []string          // absolute paths of newly created directories
	modifiedFiles map[string][]byte // absolute path -> original content (for restore)
	forceStage    []string          // absolute paths that need --force staging (may be gitignored)
	isFreshInit   bool              // true if .sageox/ didn't exist before
}

func newInitTracker(gitRoot string) *initTracker {
	return &initTracker{
		gitRoot:       gitRoot,
		modifiedFiles: make(map[string][]byte),
	}
}

// trackCreatedFile records a newly created file for rollback and staging.
func (t *initTracker) trackCreatedFile(absPath string) {
	t.createdFiles = append(t.createdFiles, absPath)
}

// trackCreatedDir records a newly created directory for rollback.
func (t *initTracker) trackCreatedDir(absPath string) {
	t.createdDirs = append(t.createdDirs, absPath)
}

// trackModifiedFile snapshots a file's current content before modification.
// No-op if the file was already snapshotted or doesn't exist.
func (t *initTracker) trackModifiedFile(absPath string) {
	if _, alreadyTracked := t.modifiedFiles[absPath]; alreadyTracked {
		return
	}
	if content, err := os.ReadFile(absPath); err == nil {
		t.modifiedFiles[absPath] = content
	}
}

// trackForceStage records a file that needs --force to stage (may be gitignored).
func (t *initTracker) trackForceStage(absPath string) {
	t.forceStage = append(t.forceStage, absPath)
}

// stageAll stages all tracked files in git.
func (t *initTracker) stageAll() {
	if !isGitRepo(t.gitRoot) {
		return
	}

	// force add all .sageox files
	if err := ForceAddSageoxFiles(); err != nil {
		cli.PrintWarning(fmt.Sprintf("Could not stage .sageox files: %v", err))
	}

	// stage AGENTS.md and CLAUDE.md if they exist
	var agentFiles []string
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Lstat(filepath.Join(t.gitRoot, name)); err == nil {
			agentFiles = append(agentFiles, filepath.Join(t.gitRoot, name))
		}
	}
	if len(agentFiles) > 0 {
		if err := gitAddFiles(agentFiles); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not stage agent files: %v", err))
		}
	}

	// stage .gitattributes if it exists
	gitattrsPath := filepath.Join(t.gitRoot, ".gitattributes")
	if _, err := os.Lstat(gitattrsPath); err == nil {
		if err := gitAddFiles([]string{gitattrsPath}); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not stage .gitattributes: %v", err))
		}
	}

	// force-stage files that may be gitignored (hooks, commands)
	if len(t.forceStage) > 0 {
		if err := gitAddFilesForce(t.forceStage); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not stage hook/command files: %v", err))
		}
	}
}

// rollback undoes all tracked changes on API registration failure.
func (t *initTracker) rollback(quiet bool) {
	dirsToRollback := t.createdDirs
	if !t.isFreshInit {
		dirsToRollback = nil
	}
	if len(t.createdFiles) == 0 && len(dirsToRollback) == 0 && len(t.modifiedFiles) == 0 {
		return
	}

	if !quiet {
		fmt.Println()
		fmt.Println(ui.RenderCategory("Rolling Back"))
	}

	// restore modified files first (before deleting anything)
	for filePath, originalContent := range t.modifiedFiles {
		if err := os.WriteFile(filePath, originalContent, 0644); err != nil {
			if !quiet {
				cli.PrintWarning(fmt.Sprintf("Could not restore %s: %v", filepath.Base(filePath), err))
			}
		} else if !quiet {
			cli.PrintSuccess(fmt.Sprintf("Restored %s", displayPath(filePath)))
		}
	}

	// remove created files (in reverse order of creation)
	for i := len(t.createdFiles) - 1; i >= 0; i-- {
		file := t.createdFiles[i]
		if err := os.Remove(file); err != nil {
			if !quiet {
				cli.PrintWarning(fmt.Sprintf("Could not remove %s: %v", filepath.Base(file), err))
			}
		} else if !quiet {
			cli.PrintSuccess(fmt.Sprintf("Removed %s", displayPath(file)))
		}
	}

	// remove directories (in reverse order, which handles nested dirs correctly)
	for i := len(dirsToRollback) - 1; i >= 0; i-- {
		dir := dirsToRollback[i]
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		if len(entries) == 0 {
			if err := os.Remove(dir); err != nil {
				if !quiet {
					cli.PrintWarning(fmt.Sprintf("Could not remove directory %s: %v", filepath.Base(dir), err))
				}
			} else if !quiet {
				cli.PrintSuccess(fmt.Sprintf("Removed %s/", filepath.Base(dir)))
			}
		} else if !quiet {
			cli.PrintWarning(fmt.Sprintf("Directory %s/ not empty, skipping removal", filepath.Base(dir)))
		}
	}
}

// fileExists returns true if path exists (regular file or symlink).
func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// displayPath returns a human-friendly relative path for rollback messages.
func displayPath(absPath string) string {
	base := filepath.Base(absPath)
	if strings.Contains(absPath, ".sageox") {
		return ".sageox/" + base
	}
	return base
}

// installAgentHooks detects AI coding agents and installs project-level hooks.
// Returns a list of relative paths to hook files that were created.
func installAgentHooks(gitRoot string, quiet bool) []string {
	var installedHooks []string

	// always install Claude Code hooks (we assume Claude Code usage)
	if HasProjectClaudeHooks(gitRoot) {
		if !quiet {
			cli.PrintPreserved("Claude Code integration")
		}
	} else {
		if err := InstallProjectClaudeHooks(gitRoot); err != nil {
			cli.PrintWarning(fmt.Sprintf("Could not install Claude Code integration: %v", err))
		} else {
			if !quiet {
				cli.PrintSuccess("Installed Claude Code integration")
			}
			installedHooks = append(installedHooks, ".claude/settings.local.json")
		}
	}

	// detect OpenCode: .opencode directory exists
	openCodeDir := filepath.Join(gitRoot, ".opencode")
	_, openCodeErr := os.Stat(openCodeDir)
	usesOpenCode := openCodeErr == nil

	if usesOpenCode {
		// check if integration already installed
		if HasProjectOpenCodeHooks(gitRoot) {
			if !quiet {
				cli.PrintPreserved("OpenCode integration")
			}
		} else {
			if err := InstallProjectOpenCodeHooks(gitRoot); err != nil {
				cli.PrintWarning(fmt.Sprintf("Could not install OpenCode integration: %v", err))
			} else {
				if !quiet {
					cli.PrintSuccess("Installed OpenCode integration")
				}
				installedHooks = append(installedHooks, ".opencode/plugin/ox-prime.ts")
			}
		}
	}

	return installedHooks
}

// detectExistingRepoMarkers scans .sageox/ for existing .repo_* marker files
// Returns a list of repo IDs found in those markers for the current API endpoint
func detectExistingRepoMarkers(sageoxDir string) ([]string, error) {
	currentEndpoint := endpoint.Get()
	return detectRepoMarkersForEndpoint(sageoxDir, currentEndpoint)
}

// detectRepoMarkersForEndpoint scans .sageox/ for .repo_* marker files matching endpoint
func detectRepoMarkersForEndpoint(sageoxDir, targetEndpoint string) ([]string, error) {
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var repoIDs []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".repo_") {
			// read marker file to get repo_id
			markerPath := filepath.Join(sageoxDir, entry.Name())
			data, err := os.ReadFile(markerPath)
			if err != nil {
				continue
			}

			// use repoMarker struct for proper JSON parsing (handles nested fingerprint)
			var marker repoMarker
			if err := json.Unmarshal(data, &marker); err != nil {
				continue
			}

			// only include markers for the target endpoint
			if marker.RepoID != "" && marker.GetEndpoint() == targetEndpoint {
				repoIDs = append(repoIDs, marker.RepoID)
			}
		}
	}

	// sort by repo_id (UUIDv7 is time-sortable, so first is oldest)
	sort.Strings(repoIDs)

	return repoIDs, nil
}

// initEndpointInfo holds display information for an endpoint in init selection
type initEndpointInfo struct {
	URL       string
	Slug      string
	Email     string
	ExpiresAt time.Time
	IsExpired bool
	IsValid   bool
}

// selectInitEndpoint shows endpoint selection UI for ox init.
// Returns (selectedEndpoint, needsLogin) where needsLogin is true if user must login first.
// Returns ("", false) if only one valid endpoint or user cancels.
func selectInitEndpoint() (string, bool) {
	// get all endpoints with stored tokens (including expired)
	storedEndpoints, err := auth.ListEndpoints()
	if err != nil {
		storedEndpoints = []string{}
	}

	// build endpoint info list
	var endpoints []initEndpointInfo
	seenSlugs := make(map[string]bool)

	for _, ep := range storedEndpoints {
		token, _ := auth.GetTokenForEndpoint(ep)
		info := initEndpointInfo{
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
		endpoints = append(endpoints, initEndpointInfo{
			URL:  endpoint.Default,
			Slug: prodSlug,
		})
	}

	// if only one endpoint and it's valid, use it without prompting
	if len(endpoints) == 1 && endpoints[0].IsValid {
		return endpoints[0].URL, false
	}

	// if only one endpoint and it's not valid, still need to show it so user knows to login
	// but if there are no endpoints at all, return empty (will be caught by auth gate later)
	if len(endpoints) == 0 {
		return "", false
	}

	// show endpoint selection
	fmt.Println()
	fmt.Println(ui.RenderCategory("Select Endpoint"))
	fmt.Println(cli.StyleDim.Render("Choose which SageOx endpoint to initialize with."))
	fmt.Println()

	// build options with status indicators
	options := make([]string, len(endpoints))
	for i, ep := range endpoints {
		status := ""
		if ep.IsValid {
			status = fmt.Sprintf(" (✓ %s)", ep.Email)
		} else if ep.Email != "" {
			status = fmt.Sprintf(" (expired: %s)", ep.Email)
		} else {
			status = " (not logged in)"
		}
		options[i] = fmt.Sprintf("%s%s", ep.Slug, status)
	}

	selected, err := cli.SelectOne("Endpoint:", options, 0)
	if err != nil {
		return "", false // canceled
	}
	if selected < 0 || selected >= len(endpoints) {
		return "", false
	}

	selectedEp := endpoints[selected]

	// if selected endpoint is not valid, tell user to login first
	if !selectedEp.IsValid {
		return selectedEp.URL, true
	}

	return selectedEp.URL, false
}

// selectTeam prompts the user to select a team for this repo.
// Always shows an interactive selector, even for single-team users.
// Includes a "Create new team" option that opens the dashboard.
// Returns the selected team ID, team name (may be empty), or error if canceled.
func selectTeam(teams []api.TeamMembership) (string, string, error) {
	if len(teams) == 0 {
		return "", "", fmt.Errorf("no teams available")
	}

	fmt.Println()
	fmt.Println(ui.RenderCategory("Select Team"))
	if len(teams) == 1 {
		fmt.Println(cli.StyleDim.Render("Confirm the team to associate this repo with."))
	} else {
		fmt.Println(cli.StyleDim.Render("Choose which team to associate this repo with."))
	}
	fmt.Println()

	// build options with role indicators + "Create new team"
	options := make([]string, len(teams)+1)
	for i, team := range teams {
		roleIndicator := ""
		if team.Role != "" && team.Role != "member" {
			roleIndicator = fmt.Sprintf(" (%s)", team.Role)
		}
		if team.Name != "" {
			options[i] = fmt.Sprintf("%s%s", team.Name, roleIndicator)
		} else {
			options[i] = fmt.Sprintf("%s%s", team.ID, roleIndicator)
		}
	}
	options[len(teams)] = "+ Create new team"

	selected, err := cli.SelectOne("Team:", options, 0)
	if err != nil {
		return "", "", err
	}
	if selected < 0 || selected >= len(options) {
		return "", "", fmt.Errorf("selection canceled")
	}

	// handle "Create new team" selection
	if selected == len(teams) {
		ep := endpoint.Get()
		dashURL := strings.TrimSuffix(ep, "/") + "/teams/new"
		fmt.Println()
		fmt.Printf("Opening %s to create a new team...\n", dashURL)
		if err := cli.OpenInBrowser(dashURL); err != nil {
			fmt.Printf("Visit %s to create a new team.\n", dashURL)
		}
		fmt.Println()
		fmt.Printf("After creating your team, re-run %s\n", cli.StyleCommand.Render("ox init"))
		return "", "", fmt.Errorf("team creation requested")
	}

	return teams[selected].ID, teams[selected].Name, nil
}

// promptNoTeams handles the case where the API returns zero teams.
// Offers to continue (server will auto-create a team) or open the dashboard.
func promptNoTeams() (bool, error) {
	ep := endpoint.Get()
	fmt.Println()
	fmt.Println(ui.RenderCategory("Team Setup"))
	fmt.Println(cli.StyleDim.Render(fmt.Sprintf("No teams found on %s.", endpoint.NormalizeSlug(ep))))
	fmt.Println()

	options := []string{
		"Continue (a new team will be created)",
		"+ Create new team via dashboard",
	}
	selected, err := cli.SelectOne("How would you like to proceed?", options, 0)
	if err != nil {
		return false, err
	}

	if selected == 1 {
		dashURL := strings.TrimSuffix(ep, "/") + "/teams/new"
		fmt.Println()
		fmt.Printf("Opening %s to create a new team...\n", dashURL)
		if err := cli.OpenInBrowser(dashURL); err != nil {
			fmt.Printf("Visit %s to create a new team.\n", dashURL)
		}
		fmt.Println()
		fmt.Printf("After creating your team, re-run %s\n", cli.StyleCommand.Render("ox init"))
		return false, nil
	}

	return true, nil
}
