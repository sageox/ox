package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// hooksCommitMsgCmd appends configured trailers to a commit message file.
//
// This is a deterministic hook called by git's prepare-commit-msg, not by AI
// agents. It emits predictable output based on config and recording state.
// Compare with "ox agent <id> hook" which handles AI coworker lifecycle
// events with non-deterministic guidance output.
var hooksCommitMsgCmd = &cobra.Command{
	Use:   "commit-msg",
	Short: "Append configured trailers to a commit message",
	Long: `Called by the prepare-commit-msg git hook to append trailers such as
Co-Authored-By and SageOx-Session to commit messages based on project config
and active recording state.

This command exits 0 always — failures are logged at debug level and never
block commits.`,
	Hidden:       true,
	SilenceUsage: true,
	RunE:         runHooksCommitMsg,
}

var (
	hooksCommitMsgFile   string
	hooksCommitMsgSource string
)

func init() {
	hooksCommitMsgCmd.Flags().StringVar(&hooksCommitMsgFile, "msg-file", "", "path to commit message file (from git hook $1)")
	hooksCommitMsgCmd.Flags().StringVar(&hooksCommitMsgSource, "source", "", "commit message source (from git hook $2: message, template, merge, squash, commit)")
	_ = hooksCommitMsgCmd.MarkFlagRequired("msg-file")

	hooksCmd.AddCommand(hooksCommitMsgCmd)
}

func runHooksCommitMsg(cmd *cobra.Command, args []string) error {
	// merge commits don't represent discrete work sessions
	if hooksCommitMsgSource == "merge" {
		return nil
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		slog.Debug("hooks commit-msg: project root not found", "error", err)
		return nil
	}

	if !config.IsInitialized(projectRoot) {
		return nil
	}

	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		slog.Debug("hooks commit-msg: config load failed", "error", err)
		return nil
	}

	attr := resolveProjectAttribution(cfg)

	msgBytes, err := os.ReadFile(hooksCommitMsgFile)
	if err != nil {
		slog.Debug("hooks commit-msg: read msg file failed", "error", err)
		return nil
	}
	msgContent := string(msgBytes)

	var trailers []string

	// co-authored-by trailer (config-gated)
	if attr.Commit != "" && !strings.Contains(msgContent, attr.Commit) {
		trailers = append(trailers, attr.Commit)
	}

	// session trailer (only when actively recording)
	if attr.Session != "" {
		state, loadErr := session.LoadRecordingState(projectRoot)
		if loadErr == nil && state != nil {
			sessionName := session.GetSessionName(state.SessionPath)
			sessionURL := buildSessionURL(cfg, sessionName)
			if sessionURL != "" {
				trailer := fmt.Sprintf("SageOx-Session: %s", sessionURL)
				if !strings.Contains(msgContent, "SageOx-Session:") {
					trailers = append(trailers, trailer)
				}
			}
		}
	}

	if len(trailers) == 0 {
		return nil
	}

	// use git interpret-trailers for correct formatting
	gitArgs := []string{"interpret-trailers", "--in-place"}
	for _, t := range trailers {
		gitArgs = append(gitArgs, "--trailer", t)
	}
	gitArgs = append(gitArgs, hooksCommitMsgFile)

	gitCmd := exec.Command("git", gitArgs...)
	if out, err := gitCmd.CombinedOutput(); err != nil {
		slog.Debug("hooks commit-msg: git interpret-trailers failed", "error", err, "output", string(out))
	}

	return nil
}

// resolveProjectAttribution loads and merges attribution for the project.
func resolveProjectAttribution(cfg *config.ProjectConfig) config.ResolvedAttribution {
	userCfg, _ := config.LoadUserConfig("")
	var userAttr *config.Attribution
	if userCfg != nil {
		userAttr = userCfg.Attribution
	}
	return config.MergeAttribution(cfg.Attribution, userAttr)
}
