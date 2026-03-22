package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/spf13/cobra"
)

const maxMurmurContentBytes = 4096 // ~1000 tokens — murmurs are short coordination signals

var murmurCmd = &cobra.Command{
	Use:   "murmur [content]",
	Short: "Publish a coordination signal to other AI coworkers",
	Long: `Murmur publishes a short-lived coordination signal that other AI coworkers
on the same repo (or team) will hear as a whisper.

Examples:
  ox murmur --topic=lint "ESLint rule failing in src/auth/"
  ox murmur --scope=team --topic=architecture "API contract v3 rolling out"
  ox murmur --importance=critical --topic=conflict "Modifying shared auth middleware"
  ox murmur '{"content": "Fixing lint rule X", "topic": "lint"}'`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMurmur,
}

var validImportanceLevels = map[string]bool{
	"critical": true,
	"normal":   true,
	"ambient":  true,
}

func init() {
	murmurCmd.Flags().String("topic", "general", "topic slug for filtering (e.g., lint, architecture, conflict)")
	murmurCmd.Flags().String("importance", "normal", "importance level: critical, normal, ambient")
	murmurCmd.Flags().String("scope", "ledger", "scope: ledger (this repo) or team (all repos)")
	murmurCmd.Flags().String("agent-id", "", "agent ID (falls back to SAGEOX_AGENT_ID env)")

	murmurCmd.GroupID = "agent-interface"
	rootCmd.AddCommand(murmurCmd)
}

// murmurInput holds parsed input from either flags+positional or JSON.
type murmurInput struct {
	Content    string `json:"content"`
	Topic      string `json:"topic,omitempty"`
	Importance string `json:"importance,omitempty"`
}

func runMurmur(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

	// parse input: positional arg or stdin
	var rawContent string
	if len(args) > 0 {
		rawContent = strings.TrimSpace(args[0])
	}
	if rawContent == "" {
		return fmt.Errorf("no content provided\nUsage: ox murmur [--topic=...] \"message\"")
	}

	// detect JSON input vs plain text
	topic, _ := cmd.Flags().GetString("topic")
	importance, _ := cmd.Flags().GetString("importance")

	if strings.HasPrefix(rawContent, "{") {
		var input murmurInput
		if err := json.Unmarshal([]byte(rawContent), &input); err != nil {
			return fmt.Errorf("invalid JSON input: %w", err)
		}
		if input.Content == "" {
			return fmt.Errorf("JSON input must have a 'content' field")
		}
		rawContent = input.Content
		// JSON fields override defaults but flags override JSON
		if input.Topic != "" && !cmd.Flags().Changed("topic") {
			topic = input.Topic
		}
		if input.Importance != "" && !cmd.Flags().Changed("importance") {
			importance = input.Importance
		}
	}

	// validate importance
	if !validImportanceLevels[importance] {
		return fmt.Errorf("invalid importance %q: must be critical, normal, or ambient", importance)
	}

	// validate content size
	if len(rawContent) > maxMurmurContentBytes {
		return fmt.Errorf("content too large (%d bytes, max %d)", len(rawContent), maxMurmurContentBytes)
	}

	// resolve scope and target directory
	scope, _ := cmd.Flags().GetString("scope")
	targetDir, err := resolveMurmurTarget(projectRoot, scope)
	if err != nil {
		return err
	}

	// resolve agent ID: flag > env > empty
	agentID, _ := cmd.Flags().GetString("agent-id")
	if agentID == "" {
		agentID = os.Getenv("SAGEOX_AGENT_ID")
	}

	// generate UUIDv7
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate murmur ID: %w", err)
	}

	now := time.Now().UTC()

	murmur := ledger.MurmurFile{
		SchemaVersion: "1",
		ID:            id.String(),
		Timestamp:     now,
		AgentID:       agentID,
		Topic:         topic,
		Importance:    importance,
		Content:       rawContent,
		Scope:         scope,
	}

	// write murmur file
	relPath, err := ledger.WriteMurmur(targetDir, murmur)
	if err != nil {
		return fmt.Errorf("write murmur: %w", err)
	}

	// git add --sparse + commit (no push — daemon syncs)
	if err := commitMurmur(targetDir, relPath, rawContent); err != nil {
		return fmt.Errorf("commit murmur: %w", err)
	}

	slog.Info("murmur published", "id", id.String(), "topic", topic, "importance", importance, "scope", scope)
	fmt.Fprintf(cmd.OutOrStdout(), "Murmur published: %s\n", id.String())
	return nil
}

// resolveMurmurTarget returns the git repo path where the murmur file should be written.
func resolveMurmurTarget(projectRoot, scope string) (string, error) {
	switch scope {
	case "ledger":
		path := getLedgerPath()
		if path == "" {
			return "", fmt.Errorf("no ledger found — run 'ox doctor --fix' or wait for daemon to clone")
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return "", fmt.Errorf("ledger not found at %s — run 'ox doctor --fix'", path)
		}
		return path, nil

	case "team":
		tc := config.FindRepoTeamContext(projectRoot)
		if tc == nil {
			return "", fmt.Errorf("no team context configured — run 'ox init' first")
		}
		return tc.Path, nil

	default:
		return "", fmt.Errorf("invalid scope %q: must be ledger or team", scope)
	}
}

// commitMurmur stages and commits a murmur file. Does not push.
func commitMurmur(repoDir, relPath, content string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --sparse: repos use sparse-checkout; without this flag
	// git refuses to stage files outside the sparse definition
	if _, err := gitutil.RunGit(ctx, repoDir, "add", "--sparse", relPath); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	summary := content
	if len(summary) > 50 {
		summary = summary[:50] + "..."
	}
	commitMsg := fmt.Sprintf("murmur: %s", summary)

	if _, err := gitutil.RunGit(ctx, repoDir, "commit", "-m", commitMsg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	return nil
}
