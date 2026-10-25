package session

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/config"
)

// ErrRedactPolicyUnusable reports a redaction policy an import cannot trust.
var ErrRedactPolicyUnusable = errors.New("redaction policy is not usable")

// ValidateRedactPolicy checks everything the strict raw writer does not. The
// writer refuses REDACT.md parse errors, but it silently carries on when a
// REDACT.md cannot be read, and when the repo's team context is not on this
// machine, so the team's rules would simply not apply. A live session can
// tolerate that for one session; an import replays months of history in bulk.
func ValidateRedactPolicy(projectRoot string) error {
	var problems []string
	if projectRoot != "" {
		if cfg, err := config.LoadProjectConfig(projectRoot); err == nil && cfg != nil && cfg.TeamID != "" {
			name := cfg.TeamName
			if name == "" {
				name = cfg.TeamID
			}
			tc := config.FindRepoTeamContext(projectRoot)
			if tc == nil || tc.Path == "" {
				problems = append(problems, fmt.Sprintf("team context for %s is not synced on this machine; run ox sync", name))
			} else if info, err := os.Stat(tc.Path); err != nil || !info.IsDir() {
				problems = append(problems, fmt.Sprintf("team context checkout %s is missing; run ox sync", tc.Path))
			}
		}
	}
	// Unreadable files, which the writer does not count.
	for _, source := range DiscoverRedactSources(projectRoot) {
		if source.IOError != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", source.Path, source.IOError))
		}
	}
	// Parse and compile errors, exactly as the strict writer counts them.
	if _, parseErrs := NewRedactorWithCustomRules(projectRoot); len(parseErrs) > 0 {
		for _, e := range parseErrs {
			problems = append(problems, e.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w:\n  %s", ErrRedactPolicyUnusable, strings.Join(problems, "\n  "))
	}
	return nil
}
