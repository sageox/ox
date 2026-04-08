package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/paths"
)

// ScoreFile represents a persisted SageOx contribution score.
// Stored at ~/.cache/sageox/scores/<agent_id>.json
type ScoreFile struct {
	Score     float64   `json:"score"`
	Reason    string    `json:"reason,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// scoresDir returns the directory for score files.
func scoresDir() string {
	return filepath.Join(paths.CacheDir(), "scores")
}

// scorePath returns the path to a specific agent's score file.
// Uses filepath.Base to prevent path traversal from untrusted input.
func scorePath(agentID string) string {
	return filepath.Join(scoresDir(), filepath.Base(agentID)+".json")
}

// WriteSageoxScore persists a SageOx contribution score for the given agent.
// Score must be in [0.0, 1.0].
func WriteSageoxScore(agentID string, score float64, reason string) error {
	if agentID == "" {
		return fmt.Errorf("agent ID must not be empty")
	}
	if score < 0 || score > 1 {
		return fmt.Errorf("score must be between 0.0 and 1.0, got %f", score)
	}

	dir := scoresDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create scores dir: %w", err)
	}

	sf := ScoreFile{
		Score:     score,
		Reason:    reason,
		UpdatedAt: time.Now().UTC(),
	}

	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal score: %w", err)
	}

	path := scorePath(agentID)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write score file: %w", err)
	}

	return nil
}

// ReadSageoxScore reads the SageOx contribution score for the given agent.
// Returns nil, nil if no score file exists.
func ReadSageoxScore(agentID string) (*ScoreFile, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent ID must not be empty")
	}

	data, err := os.ReadFile(scorePath(agentID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read score file: %w", err)
	}

	var sf ScoreFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("unmarshal score file: %w", err)
	}
	if sf.Score < 0 || sf.Score > 1 {
		return nil, fmt.Errorf("invalid score in score file: %f", sf.Score)
	}

	return &sf, nil
}

// CleanupSageoxScore removes the score file for the given agent.
func CleanupSageoxScore(agentID string) error {
	if agentID == "" {
		return nil
	}
	path := scorePath(agentID)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove score file: %w", err)
	}
	return nil
}

// CleanupStaleScores removes score files for agent IDs not in the provided
// set of active IDs. Called from ghost cleanup or ox doctor --fix.
func CleanupStaleScores(activeAgentIDs map[string]bool) (int, error) {
	dir := scoresDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read scores dir: %w", err)
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		agentID := strings.TrimSuffix(name, ".json")
		if activeAgentIDs[agentID] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		removed++
	}
	return removed, nil
}
