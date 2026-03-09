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

Observations are written locally and synced to the server by the daemon.
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

type observationHeader struct {
	SchemaVersion string `json:"schema_version"`
	RecordedAt    string `json:"recorded_at"`
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

// writeObservation writes observations to a team context's memory directory.
// Used by both `ox memory put` and session-end auto-observation.
func writeObservation(teamContextPath string, observations []observation) error {
	now := time.Now().UTC()
	dateDir := now.Format("2006-01-02")
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate observation ID: %w", err)
	}

	obsDir := filepath.Join(teamContextPath, "memory", observationDirName, dateDir)
	if err := os.MkdirAll(obsDir, 0o755); err != nil {
		return fmt.Errorf("create observations directory: %w", err)
	}

	filename := id.String() + ".jsonl"
	filePath := filepath.Join(obsDir, filename)

	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("create observation file: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)

	header := observationHeader{
		SchemaVersion: "1",
		RecordedAt:    now.Format(time.RFC3339),
	}
	headerBytes, _ := json.Marshal(header)
	w.Write(headerBytes)
	w.WriteByte('\n')

	for _, obs := range observations {
		entryBytes, _ := json.Marshal(obs)
		w.Write(entryBytes)
		w.WriteByte('\n')
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("write observation file: %w", err)
	}
	f.Close()

	// git add + commit
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	relPath := filepath.Join("memory", observationDirName, dateDir, filename)

	// --sparse: team context repos use sparse-checkout; without this flag
	// git refuses to stage files outside the sparse definition
	if _, err := gitutil.RunGit(ctx, teamContextPath, "add", "--sparse", relPath); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	summary := observations[0].Content
	if len(summary) > 50 {
		summary = summary[:50] + "..."
	}
	commitMsg := fmt.Sprintf("observation: %s", summary)

	if _, err := gitutil.RunGit(ctx, teamContextPath, "commit", "-m", commitMsg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	slog.Info("observation recorded", "file", relPath, "count", len(observations))
	return nil
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
