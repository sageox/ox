package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionScoreCmd = &cobra.Command{
	Use:   "score",
	Short: "Report or view SageOx contribution score for this session",
	Long: `Report how much SageOx team context influenced this session's work.

Without flags, displays the current score. With --score, sets or updates it.

The score determines whether commits receive SageOx attribution:
  none         (0.0)  No influence
  minor        (0.3)  Confirmed an approach
  moderate     (0.5)  Guided decisions
  significant  (0.7)  Domain knowledge otherwise unavailable
  critical     (1.0)  Entirely shaped the approach

Examples:
  ox session score                                      # view current score
  ox session score --score moderate                     # set score
  ox session score --score significant --reason "..."   # set score with explanation`,
	RunE: runSessionScore,
}

func init() {
	sessionScoreCmd.Flags().String("score", "", "SageOx contribution level (none, minor, moderate, significant, critical)")
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
	scoreStr, _ := cmd.Flags().GetString("score")
	reason, _ := cmd.Flags().GetString("reason")

	// try parsing as category name first
	if cat, ok := session.ParseScoreCategory(scoreStr); ok {
		if err := session.WriteSageoxScoreCategory(agentID, cat, reason); err != nil {
			return fmt.Errorf("write score: %w", err)
		}
		return outputScoreResult(session.CategoryValue(cat), string(cat), reason)
	}

	// fall back to numeric for backward compatibility
	scoreVal, err := strconv.ParseFloat(scoreStr, 64)
	if err != nil {
		validCats := make([]string, 0, len(session.ValidScoreCategories()))
		for _, c := range session.ValidScoreCategories() {
			validCats = append(validCats, string(c))
		}
		return fmt.Errorf("invalid score %q: use a category (%s)", scoreStr, strings.Join(validCats, ", "))
	}

	if scoreVal < 0 || scoreVal > 1 {
		return fmt.Errorf("score must be between 0.0 and 1.0, got %f", scoreVal)
	}

	if err := session.WriteSageoxScore(agentID, scoreVal, reason); err != nil {
		return fmt.Errorf("write score: %w", err)
	}
	return outputScoreResult(scoreVal, string(session.CategoryForScore(scoreVal)), reason)
}

func outputScoreResult(score float64, category, reason string) error {
	out := map[string]interface{}{
		"ok":           true,
		"sageox_score": score,
		"category":     category,
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
