package recap

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// SessionFacts is the distilled descriptor of one ledger session, read from its
// meta.json. It is the join key for every other miner (traces, decisions, git
// trailers).
type SessionFacts struct {
	Name            string
	Dir             string
	Title           string
	Summary         string
	Username        string
	SessionID       string // EffectiveSessionID — never the raw field
	RepoID          string
	CreatedAt       time.Time
	Mine            bool
	ProducedCommits []string
	LinkedPRs       []string
}

// ScanSessions reads every session under <ledgerPath>/sessions, filters to the
// half-open window [since, until), and marks which belong to the given
// identity. Fail-open: an unreadable or unparseable session dir is skipped, not
// fatal — a value report must never error on one bad meta.json.
func ScanSessions(ledgerPath string, since, until time.Time, id Identity) []SessionFacts {
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil
	}

	var facts []SessionFacts
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(sessionsDir, name)
		meta, err := lfs.ReadSessionMeta(dir)
		if err != nil || meta == nil {
			continue
		}
		if !inWindow(meta.CreatedAt, since, until) {
			continue
		}
		facts = append(facts, SessionFacts{
			Name:            name,
			Dir:             dir,
			Title:           meta.Title,
			Summary:         meta.Summary,
			Username:        meta.Username,
			SessionID:       meta.EffectiveSessionID(),
			RepoID:          meta.RepoID,
			CreatedAt:       meta.CreatedAt,
			Mine:            id.Matches(meta, name),
			ProducedCommits: meta.ProducedCommits,
			LinkedPRs:       meta.LinkedPRs,
		})
	}
	return facts
}

// inWindow reports whether t falls in the half-open interval [since, until).
// A zero `until` means "no upper bound".
func inWindow(t, since, until time.Time) bool {
	if t.Before(since) {
		return false
	}
	if !until.IsZero() && !t.Before(until) {
		return false
	}
	return true
}

// mineOnly returns the subset of sessions belonging to the user.
func mineOnly(facts []SessionFacts) []SessionFacts {
	var out []SessionFacts
	for _, f := range facts {
		if f.Mine {
			out = append(out, f)
		}
	}
	return out
}

// usernameSlug extracts the user slug from a session folder name of the form
// "<date>T<time>-<user>-<agentid>". Mirrors the glance harvester so scoping is
// consistent across commands. Returns "" when the name doesn't parse.
func usernameSlug(sessionName string) string {
	parts := strings.Split(sessionName, "-")
	if len(parts) >= 6 {
		return strings.Join(parts[4:len(parts)-1], "-")
	}
	return ""
}

// equalFold is a case-insensitive, whitespace-trimmed compare used for identity
// matching (display names and slugs vary in case/spacing across surfaces).
func equalFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
