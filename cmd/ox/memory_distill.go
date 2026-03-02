package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/pkg/agentx"
	"github.com/spf13/cobra"
)

var memoryDistillCmd = &cobra.Command{
	Use:   "distill",
	Short: "Distill accumulated observations into team memory summaries",
	Long: `Distill accumulated observations into team memory summaries.

This command is designed for AI coworkers. It reads pending observations
from the team context, sends them to the SageOx API for LLM-powered
distillation, and updates the distill state.

When run by a human, it explains the distillation process.
When run by an AI coworker, it delegates to 'ox agent <id> distill'.`,
	RunE: runMemoryDistill,
}

func runMemoryDistill(cmd *cobra.Command, _ []string) error {
	if errMsg := agentx.RequireAgent("ox memory distill"); errMsg != "" {
		fmt.Fprintln(cmd.OutOrStdout(), "Memory distillation is an automated process run by AI coworkers.")
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "How it works:")
		fmt.Fprintln(cmd.OutOrStdout(), "  1. AI coworkers record observations via 'ox memory put'")
		fmt.Fprintln(cmd.OutOrStdout(), "  2. Observations accumulate in the team context")
		fmt.Fprintln(cmd.OutOrStdout(), "  3. Periodically, an AI coworker runs 'ox agent <id> distill'")
		fmt.Fprintln(cmd.OutOrStdout(), "     to aggregate observations into team memory summaries")
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "This happens automatically — no human action needed.")
		return nil
	}

	// running in agent context — delegate to the agent distill flow
	return runAgentDistill(nil, cmd)
}

// distillState tracks what has been distilled to avoid reprocessing.
type distillState struct {
	SchemaVersion    string `json:"schema_version"`
	LastDistilled    string `json:"last_distilled"` // RFC3339
	ObservationCount int    `json:"observation_count"`
	TeamID           string `json:"team_id"`
}

// distillObservation is a parsed observation ready for distillation.
type distillObservation struct {
	Content    string    `json:"content"`
	RecordedAt time.Time `json:"recorded_at"`
	SourceFile string    `json:"-"`
}

// runAgentDistill is called via `ox agent <id> distill`.
// It reads accumulated observations and sends them to the SageOx API for distillation.
func runAgentDistill(inst *agentinstance.Instance, cmd *cobra.Command) error {
	_ = inst // agent instance available for future use (e.g., tracking)

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return fmt.Errorf("no team context configured — run 'ox init' first")
	}

	// load since timestamp from distill state
	var since time.Time
	state, _ := loadDistillState(projectRoot)
	if state != nil && state.LastDistilled != "" {
		since, _ = time.Parse(time.RFC3339, state.LastDistilled)
	}

	// scan for pending observations
	obsDir := filepath.Join(tc.Path, "memory", ".observations")
	observations, _, err := scanPendingObservations(obsDir, since)
	if err != nil {
		return fmt.Errorf("scan observations: %w", err)
	}

	if len(observations) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Nothing to distill — no pending observations")
		return nil
	}

	// compute date range
	earliest := observations[0].RecordedAt
	latest := observations[0].RecordedAt
	for _, obs := range observations[1:] {
		if obs.RecordedAt.Before(earliest) {
			earliest = obs.RecordedAt
		}
		if obs.RecordedAt.After(latest) {
			latest = obs.RecordedAt
		}
	}

	// get auth token
	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil {
		return fmt.Errorf("authentication required: %w", err)
	}

	// build API request
	apiObs := make([]api.DistillObservation, len(observations))
	for i, obs := range observations {
		apiObs[i] = api.DistillObservation{
			Content:    obs.Content,
			RecordedAt: obs.RecordedAt.Format(time.RFC3339),
		}
	}

	req := &api.DistillRequest{
		Observations: apiObs,
		DateRange:    [2]string{earliest.Format(time.RFC3339), latest.Format(time.RFC3339)},
	}

	// call distill API
	client := api.NewRepoClientForProject(projectRoot).WithAuthToken(token.AccessToken)
	resp, err := client.DistillMemory(tc.TeamID, req)
	if err != nil {
		return fmt.Errorf("distill API: %w", err)
	}

	// save distill state
	newState := &distillState{
		SchemaVersion:    "1",
		LastDistilled:    time.Now().UTC().Format(time.RFC3339),
		ObservationCount: len(observations),
		TeamID:           tc.TeamID,
	}
	if err := saveDistillState(projectRoot, newState); err != nil {
		slog.Warn("failed to save distill state", "error", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Distilled %d observations\n", len(observations))
	if resp.Summary != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Summary: %s\n", resp.Summary)
	}
	return nil
}

// scanPendingObservations walks observation directories and parses JSONL files.
func scanPendingObservations(obsDir string, since time.Time) ([]distillObservation, map[string]int, error) {
	dayCounts := make(map[string]int)

	dayDirs, err := os.ReadDir(obsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, dayCounts, nil
		}
		return nil, nil, err
	}

	var observations []distillObservation
	for _, dayEntry := range dayDirs {
		if !dayEntry.IsDir() {
			continue
		}
		dayName := dayEntry.Name()
		if _, err := time.Parse("2006-01-02", dayName); err != nil {
			continue
		}

		dayPath := filepath.Join(obsDir, dayName)
		files, err := os.ReadDir(dayPath)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}

			filePath := filepath.Join(dayPath, f.Name())
			fileObs, err := parseObservationFile(filePath, since)
			if err != nil {
				slog.Debug("skip observation file", "path", filePath, "error", err)
				continue
			}

			observations = append(observations, fileObs...)
			dayCounts[dayName] += len(fileObs)
		}
	}

	sort.Slice(observations, func(i, j int) bool {
		return observations[i].RecordedAt.Before(observations[j].RecordedAt)
	})

	return observations, dayCounts, nil
}

// parseObservationFile reads a single JSONL observation file.
// First line is the header with schema_version and recorded_at.
// Subsequent lines are observations with content field.
func parseObservationFile(filePath string, since time.Time) ([]distillObservation, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	var recordedAt time.Time
	var observations []distillObservation

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if lineNum == 1 {
			var header observationHeader
			if err := json.Unmarshal([]byte(line), &header); err != nil {
				return nil, fmt.Errorf("invalid header: %w", err)
			}
			recordedAt, _ = time.Parse(time.RFC3339, header.RecordedAt)
			if !since.IsZero() && !recordedAt.IsZero() && recordedAt.Before(since) {
				return nil, nil
			}
			continue
		}

		var obs observation
		if err := json.Unmarshal([]byte(line), &obs); err != nil {
			continue
		}
		if obs.Content == "" {
			continue
		}

		observations = append(observations, distillObservation{
			Content:    obs.Content,
			RecordedAt: recordedAt,
			SourceFile: filePath,
		})
	}

	return observations, scanner.Err()
}

func loadDistillState(projectRoot string) (*distillState, error) {
	path := filepath.Join(projectRoot, ".sageox", "distill-state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state distillState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func saveDistillState(projectRoot string, state *distillState) error {
	path := filepath.Join(projectRoot, ".sageox", "distill-state.json")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
