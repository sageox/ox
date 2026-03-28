package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledger"
	"github.com/spf13/cobra"
)

const maxMurmurContentBytes = 500            // ~125 tokens — murmurs must be concise coordination signals
const murmurRateInterval  = 5 * time.Second // max 1 murmur per agent per this window

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
	// --- pure input validation (no I/O) ---

	var rawContent string
	if len(args) > 0 {
		rawContent = strings.TrimSpace(args[0])
	}
	if rawContent == "" {
		return fmt.Errorf("no content provided\nUsage: ox murmur [--topic=...] \"message\"")
	}

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
		if input.Topic != "" && !cmd.Flags().Changed("topic") {
			topic = input.Topic
		}
		if input.Importance != "" && !cmd.Flags().Changed("importance") {
			importance = input.Importance
		}
	}

	if !validImportanceLevels[importance] {
		return fmt.Errorf("invalid importance %q: must be critical, normal, or ambient", importance)
	}

	if len(rawContent) > maxMurmurContentBytes {
		return fmt.Errorf("content too large (%d bytes, max %d)", len(rawContent), maxMurmurContentBytes)
	}

	// --- project I/O ---

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}

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

	// rate limit: max 1 murmur per agent per murmurRateInterval
	if agentID != "" {
		lastMurmur := ledger.MostRecentMurmurTime(targetDir, agentID)
		if !lastMurmur.IsZero() && time.Since(lastMurmur) < murmurRateInterval {
			return fmt.Errorf("rate limited: max 1 murmur per %s per agent (last: %s ago)",
				murmurRateInterval, time.Since(lastMurmur).Truncate(time.Millisecond))
		}
	}

	// generate UUIDv7
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate murmur ID: %w", err)
	}

	now := time.Now().UTC()

	// resolve principal (human user the agent works for)
	principalID := resolvePrincipalID(projectRoot)

	murmur := ledger.MurmurFile{
		SchemaVersion: "1",
		ID:            id.String(),
		Timestamp:     now,
		AgentID:       agentID,
		PrincipalID:   principalID,
		PrincipalType: "human",
		Topic:         topic,
		Importance:    importance,
		Content:       rawContent,
		Scope:         scope,
	}

	// serialize the murmur for IPC — daemon owns all file I/O and git operations
	relPath := ledger.MurmurFilePath(now, id.String())
	murmurJSON, err := json.Marshal(murmur)
	if err != nil {
		return fmt.Errorf("marshal murmur: %w", err)
	}

	// murmurs are best-effort — if the daemon isn't running, warn and continue
	if err := publishMurmurViaIPC(targetDir, relPath, rawContent, murmurJSON); err != nil {
		slog.Debug("murmur not delivered", "error", err)
		fmt.Fprintf(cmd.OutOrStdout(), "Murmur not delivered (daemon unavailable): %s\n", id.String())
		return nil
	}

	slog.Info("murmur published", "id", id.String(), "topic", topic, "importance", importance, "scope", scope)
	fmt.Fprintf(cmd.OutOrStdout(), "Murmur published: %s\n", id.String())
	return nil
}

// resolvePrincipalID returns the human user identity for the murmur.
// Tries SageOx auth (email/name), falls back to OS username.
func resolvePrincipalID(projectRoot string) string {
	ep := endpoint.GetForProject(projectRoot)
	if username := auth.GetUsername(ep); username != "" {
		return username
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return ""
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

// publishMurmurViaIPC sends the full murmur to the daemon via IPC.
// The daemon writes the file to disk, git adds, and commits — the CLI does no disk I/O.
// Returns an error if the daemon is not running or the IPC send fails.
func publishMurmurViaIPC(targetDir, relPath, content string, murmurJSON []byte) error {
	client := daemon.TryConnect()
	if client == nil {
		return fmt.Errorf("daemon not running")
	}
	return client.Murmur(daemon.MurmurPayload{
		TargetDir:  targetDir,
		RelPath:    relPath,
		Content:    content,
		MurmurJSON: murmurJSON,
	})
}

