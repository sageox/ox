package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionScoreCmd = &cobra.Command{
	Use:   "score",
	Short: "Report or view SageOx contribution score for this session",
	Long: `Report how much SageOx team context influenced this session's work.

Without flags, displays the current score. With --score, sets or updates it.

The score (0.0-1.0) determines whether commits receive SageOx attribution:
  0.0  No influence
  0.3  Minor -- confirmed an approach
  0.5  Moderate -- guided decisions
  0.7  Significant -- domain knowledge otherwise unavailable
  1.0  Critical -- entirely shaped the approach

Examples:
  ox session score                              # view current score
  ox session score --score 0.7                  # set score
  ox session score --score 0.7 --reason "..."   # set score with explanation`,
	RunE: runSessionScore,
}

func init() {
	sessionScoreCmd.Flags().Float64("score", 0, "SageOx contribution score (0.0-1.0)")
	sessionScoreCmd.Flags().String("reason", "", "Detailed explanation of SageOx influence")
}

func runSessionScore(cmd *cobra.Command, _ []string) error {
	agentID := os.Getenv("SAGEOX_AGENT_ID")
	if agentID == "" {
		return fmt.Errorf("SAGEOX_AGENT_ID not set -- run 'ox agent prime' first")
	}

	if !cmd.Flags().Changed("score") {
		return showSessionScore(agentID)
	}

	return setSessionScore(cmd, agentID)
}

func showSessionScore(agentID string) error {
	sf, err := session.ReadSageoxScore(agentID)
	if err != nil {
		return fmt.Errorf("read score: %w", err)
	}

	if sf == nil {
		out := map[string]interface{}{
			"score":   nil,
			"message": "No score reported for this session",
		}
		jsonOut, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fmt.Errorf("format JSON: %w", err)
		}
		fmt.Println(string(jsonOut))
		return nil
	}

	jsonOut, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("format JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}

func setSessionScore(cmd *cobra.Command, agentID string) error {
	scoreVal, _ := cmd.Flags().GetFloat64("score")
	reason, _ := cmd.Flags().GetString("reason")

	if scoreVal < 0 || scoreVal > 1 {
		return fmt.Errorf("score must be between 0.0 and 1.0, got %f", scoreVal)
	}

	if err := session.WriteSageoxScore(agentID, scoreVal, reason); err != nil {
		return fmt.Errorf("write score: %w", err)
	}

	out := map[string]interface{}{
		"ok":           true,
		"sageox_score": scoreVal,
	}
	if reason != "" {
		out["reason"] = reason
	}

	jsonOut, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("format JSON: %w", err)
	}
	fmt.Println(string(jsonOut))
	return nil
}
