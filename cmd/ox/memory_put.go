package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/facts"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/spf13/cobra"
)

const (
	maxObservationBytes = 20480 // ~5000 tokens
	observationDirName  = ".observations"
)

var memoryPutFile string

var memoryPutCmd = &cobra.Command{
	Use:   "put [json]",
	Short: "Record an observation into team memory",
	Long: `Record an observation into the team context memory pipeline.

Observations are committed to the team context on this machine;
` + "`ox memory distill`" + ` sends them to SageOx.
Each observation must have a "content" field.

Input formats:
  JSON object:  {"content": "We decided to use PostgreSQL"}
  JSONL:        One JSON object per line (batch)

Examples:
  ox memory put '{"content": "We decided to use PostgreSQL for analytics"}'
  ox memory put --file /tmp/observations.jsonl
  echo '{"content": "Auth module needs refactoring"}' | ox memory put`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMemoryPut,
}

func init() {
	memoryPutCmd.Flags().StringVar(&memoryPutFile, "file", "", "path to JSON/JSONL file with observations")
}

type observation struct {
	Content string `json:"content"`
}

func runMemoryPut(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return fmt.Errorf("no team context configured — run 'ox init' first")
	}

	// read input
	var rawInput []byte
	switch {
	case len(args) > 0:
		rawInput = []byte(args[0])
	case memoryPutFile != "":
		rawInput, err = os.ReadFile(memoryPutFile)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("file not found: %s", memoryPutFile)
			}
			return fmt.Errorf("read file: %w", err)
		}
	default:
		rawInput, err = io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}

	rawInput = []byte(strings.TrimSpace(string(rawInput)))
	if len(rawInput) == 0 {
		return fmt.Errorf("no input provided")
	}

	// parse observations (JSON or JSONL)
	observations, err := parseObservations(rawInput)
	if err != nil {
		return err
	}

	if len(observations) == 0 {
		return fmt.Errorf("no observations found in input")
	}

	// validate
	for i, obs := range observations {
		if obs.Content == "" {
			return fmt.Errorf("observation %d: each observation must have a 'content' field", i+1)
		}
		if len(obs.Content) > maxObservationBytes {
			return fmt.Errorf("observation %d: too large (%d bytes, max %d)", i+1, len(obs.Content), maxObservationBytes)
		}
	}

	// redaction is caller-side: the agent redacts content before calling ox memory put,
	// using guidance from REDACT.md files (same pattern as session summary prompts).

	if err := writeObservation(tc.Path, observations); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Recorded %d observation(s)\n", len(observations))
	return nil
}

// writeObservation writes observations to a team context's memory directory
// and commits them. Used by both `ox memory put` and session-end
// auto-observation.
func writeObservation(teamContextPath string, observations []observation) error {
	now := time.Now().UTC()
	dateDir := now.Format("2006-01-02")
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate observation ID: %w", err)
	}

	filename := id.String() + ".jsonl"
	filePath := filepath.Join(teamContextPath, "memory", observationDirName, dateDir, filename)

	header := facts.FileHeader{
		Meta: facts.FileMeta{
			SchemaVersion: facts.SchemaVersion,
			SourceType:    facts.SourceObservation,
			RecordedAt:    now.Format(time.RFC3339),
		},
	}

	factSlice := make([]facts.Fact, len(observations))
	for i, obs := range observations {
		factSlice[i] = facts.Fact{
			Headline:   obs.Content,
			SourceType: facts.SourceObservation,
			Timestamp:  now.Format(time.RFC3339),
		}
	}

	summary := observations[0].Content
	if len(summary) > 50 {
		summary = summary[:50] + "..."
	}
	commitMsg := fmt.Sprintf("observation: %s", summary)
	relPath := filepath.Join("memory", observationDirName, dateDir, filename)
	pending := "memory/" + observationDirName

	if err := writeObservationFile(teamContextPath, filePath, header, factSlice); err != nil {
		return fmt.Errorf("write observation file: %w", err)
	}

	gitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Under the repo lock the daemon's team-context pull and `ox import` hold
	// (ADR-030). The commit takes every pending observation, so one an earlier
	// failed put left is committed by the next.
	err = gitutil.WithRepoLock(gitCtx, teamContextPath, func() error {
		// git run outside a checkout finds any enclosing repository
		if !gitutil.IsGitRepo(teamContextPath) {
			return fmt.Errorf("git add: team context %s is not a git checkout", teamContextPath)
		}
		// mid-rebase, a staged file is deleted by the daemon's `rebase --abort`;
		// left unstaged it survives for the next put to commit
		if err := gitutil.IsSafeForGitOps(teamContextPath); err != nil {
			return fmt.Errorf("git add: team context not safe to commit: %w", err)
		}
		// --sparse: team context repos use sparse-checkout; without this flag
		// git refuses to stage files outside the sparse definition
		if _, err := gitutil.RunGit(gitCtx, teamContextPath, "add", "--sparse", "--", pending); err != nil {
			return fmt.Errorf("git add: %w", err)
		}
		if _, err := gitutil.CommitLedgerSnapshot(gitCtx, teamContextPath, commitMsg, pending); err != nil {
			return fmt.Errorf("git commit: %w", err)
		}
		return nil
	})
	if gitutil.IsRepoLockBusy(err) {
		return fmt.Errorf("observation saved but not committed, team context busy: %w", err)
	}
	if err != nil {
		return err
	}

	slog.Info("observation recorded", "file", relPath, "count", len(observations))
	return nil
}

// writeObservationFile writes the observation complete inside .git/ and then
// renames it into place, so a commit taking all of memory/.observations never
// stages one half written. Outside a checkout nothing commits it, and it is
// written in place.
func writeObservationFile(teamContextPath, filePath string, header facts.FileHeader, fs []facts.Fact) error {
	gitDir := filepath.Join(teamContextPath, ".git")
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		return facts.WriteFacts(filePath, header, fs)
	}
	tmp := filepath.Join(gitDir, "ox-observation-"+filepath.Base(filePath))
	if err := facts.WriteFacts(tmp, header, fs); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, filePath)
}

// parseObservations parses JSON (single object) or JSONL (one object per line).
func parseObservations(data []byte) ([]observation, error) {
	// try single JSON object first
	var single observation
	if err := json.Unmarshal(data, &single); err == nil && single.Content != "" {
		return []observation{single}, nil
	}

	// try JSONL (one object per line)
	var results []observation
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var obs observation
		if err := json.Unmarshal([]byte(line), &obs); err != nil {
			return nil, fmt.Errorf("invalid JSON on line %d: %w", lineNum, err)
		}
		results = append(results, obs)
	}

	return results, scanner.Err()
}
