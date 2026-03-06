package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

// sessionPushSummaryCmd pushes a raw-summary.json file to the ledger session directory.
// This is called by the agent after generating a summary from the summary_prompt returned
// by `ox session stop`. The summary is small JSON (~1-50KB) and does not need LFS.
var sessionPushSummaryCmd = &cobra.Command{
	Use:   "push-summary",
	Short: "Push session summary to ledger",
	Long: `Push a generated summary JSON file to the session's ledger directory.

Called automatically by coding agents after processing the summary_prompt
from 'ox session stop'. The summary file is committed directly to git
(no LFS needed) alongside meta.json.`,
	RunE: runSessionPushSummary,
}

func init() {
	sessionPushSummaryCmd.Flags().String("file", "", "Path to the summary JSON file (use '-' for stdin)")
	sessionPushSummaryCmd.Flags().String("session-dir", "", "Path to the session directory in the ledger")
	_ = sessionPushSummaryCmd.MarkFlagRequired("file")
	_ = sessionPushSummaryCmd.MarkFlagRequired("session-dir")
}

// pushSummaryOutput is the JSON output format for push-summary.
type pushSummaryOutput struct {
	Success     bool   `json:"success"`
	Type        string `json:"type"`
	SummaryPath string `json:"summary_path,omitempty"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
}

func runSessionPushSummary(cmd *cobra.Command, args []string) error {
	filePath, _ := cmd.Flags().GetString("file")
	sessionDir, _ := cmd.Flags().GetString("session-dir")

	result := pushSummaryToLedger(filePath, sessionDir)

	jsonOut, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("format JSON: %w", err)
	}
	fmt.Println(string(jsonOut))

	if !result.Success {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

// pushSummaryToLedger validates inputs, copies the summary file, and commits+pushes.
func pushSummaryToLedger(filePath, sessionDir string) *pushSummaryOutput {
	// read summary data from file or stdin
	var data []byte
	var err error
	if filePath == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(filePath)
	}
	if err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("read summary file: %v", err),
		}
	}

	if !json.Valid(data) {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   "file is not valid JSON",
		}
	}

	// validate --session-dir exists
	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("session directory does not exist: %s", sessionDir),
		}
	}

	// resolve ledger path by walking up from session-dir to find the git root
	ledgerPath, err := findGitRootFrom(sessionDir)
	if err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("find ledger git root: %v", err),
		}
	}

	// copy file to <session-dir>/summary.json
	summaryDst := filepath.Join(sessionDir, "summary.json")
	if err := os.WriteFile(summaryDst, data, 0644); err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("write summary.json: %v", err),
		}
	}

	// update meta.json summary with the AI-generated title from summary.json
	metaUpdated := false
	var summaryObj struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(data, &summaryObj); err == nil && summaryObj.Title != "" {
		if err := lfs.UpdateMetaSummary(sessionDir, summaryObj.Title); err != nil {
			slog.Debug("update meta.json summary", "error", err)
		} else {
			metaUpdated = true
		}
	}

	// ensure .gitignore is in place before any commit to prevent cache file leakage
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// extract session name from session dir path for commit message
	sessionName := filepath.Base(sessionDir)

	// git add the summary file (and meta.json if updated)
	// --sparse: ledger repos use sparse-checkout
	addArgs := []string{"-C", ledgerPath, "add", "--sparse", summaryDst}
	if metaUpdated {
		addArgs = append(addArgs, filepath.Join(sessionDir, "meta.json"))
	}
	addCmd := exec.Command("git", addArgs...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("git add: %s: %v", strings.TrimSpace(string(output)), err),
		}
	}

	// git commit
	commitMsg := fmt.Sprintf("summary: %s", sessionName)
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		outStr := string(output)
		if strings.Contains(outStr, "nothing to commit") {
			// file was already committed (e.g., re-run)
			return &pushSummaryOutput{
				Success:     true,
				Type:        "push_summary",
				SummaryPath: summaryDst,
				Message:     "summary already committed",
			}
		}
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("git commit: %s: %v", strings.TrimSpace(outStr), err),
		}
	}

	// push using existing retry logic
	slog.Info("pushing summary to ledger", "session", sessionName)
	if err := pushLedger(context.Background(), ledgerPath); err != nil {
		return &pushSummaryOutput{
			Success: false,
			Type:    "push_summary",
			Error:   fmt.Sprintf("git push: %v", err),
		}
	}

	// clear .needs-summary marker in cache if we can find it
	clearNeedsSummaryMarkerForSession(sessionName)

	// regenerate local cache HTML with rich summary (aha moments, insights, etc.)
	regenerateLocalCacheHTML(sessionName, data)

	return &pushSummaryOutput{
		Success:     true,
		Type:        "push_summary",
		SummaryPath: summaryDst,
		Message:     "summary pushed to ledger",
	}
}

// clearNeedsSummaryMarkerForSession attempts to find and clear the .needs-summary marker
// for a session by scanning known cache locations. Best-effort, failures are logged.
func clearNeedsSummaryMarkerForSession(sessionName string) {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return
	}

	repoID := getRepoIDOrDefault(gitRoot)
	contextPath := session.GetContextPath(repoID)
	if contextPath == "" {
		return
	}

	cacheSessionDir := filepath.Join(contextPath, "sessions", sessionName)
	if err := session.ClearNeedsSummaryMarker(cacheSessionDir); err != nil {
		slog.Debug("clear needs-summary marker", "error", err, "session", sessionName)
	}
}

// regenerateLocalCacheHTML regenerates the session HTML in the local cache
// using the rich summary (with aha moments, SageOx insights, etc.).
// It writes summary.json to the cache dir so generateHTML() can read it,
// then regenerates the HTML with the full rich template.
// Best-effort: failures are logged but don't affect push-summary success.
func regenerateLocalCacheHTML(sessionName string, summaryData []byte) {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return
	}

	repoID := getRepoIDOrDefault(gitRoot)
	contextPath := session.GetContextPath(repoID)
	if contextPath == "" {
		return
	}

	cacheSessionDir := filepath.Join(contextPath, "sessions", sessionName)

	// find raw.jsonl in cache dir
	rawPath := filepath.Join(cacheSessionDir, ledgerFileRaw)
	if _, err := os.Stat(rawPath); err != nil {
		slog.Debug("no raw.jsonl in cache for HTML regen", "path", rawPath)
		return
	}

	// write summary.json to cache dir so generateHTML() can read it
	cacheSummaryPath := filepath.Join(cacheSessionDir, "summary.json")
	if err := os.WriteFile(cacheSummaryPath, summaryData, 0644); err != nil {
		slog.Debug("write summary.json to cache for HTML regen", "error", err)
		return
	}

	// read stored session
	stored, err := session.ReadSessionFromPath(rawPath)
	if err != nil {
		slog.Debug("read session for HTML regen", "error", err)
		return
	}

	// regenerate HTML using generateHTML() which reads summary.json from
	// the same directory and populates aha moments, SageOx insights, etc.
	htmlPath := filepath.Join(filepath.Dir(rawPath), ledgerFileHTML)
	if err := generateHTML(stored, htmlPath); err != nil {
		slog.Debug("regenerate HTML with rich summary", "error", err)
		return
	}

	slog.Info("regenerated local cache HTML with rich summary", "path", htmlPath)
}

// findGitRootFrom walks up from a directory to find the .git root.
func findGitRootFrom(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	for {
		gitDir := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no git root found above %s", dir)
		}
		dir = parent
	}
}
