package teamskills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/teamdocs"
)

// orgsync.go records which team skills a team's Claude organization already
// delivers, so ox can stop copying the same skills into .claude/skills.
//
// A team admin can connect SageOx to their Claude organization, which then
// pulls the team's portable skills as a plugin named after the team. Claude
// Code on a coworker's machine would otherwise list each such skill twice: the
// org plugin's copy and ox's `<name>-team` copy. The org copy wins.
//
// The server reports the list on GET /api/v1/cli/repos (claude_org_synced_skills
// on each team), only once Claude's sync has actually pulled. The daemon's
// hourly team discovery records it here; the skill planner reads it on the
// prime hot path, where it cannot make a network call.
//
// Every failure resolves to "not connected", which is exactly today's
// behavior: ox copies every skill. A duplicate skill is a nuisance; a missing
// one is a broken workflow, so the uncertain direction is always the duplicate.

// ClaudeOrgSyncMaxAge is how long a recorded list is believed. The daemon
// refreshes it hourly, so a day without a refresh means ox no longer knows
// whether the org still delivers these skills (the daemon stopped, or the
// machine was offline). Past this, ox goes back to copying them.
const ClaudeOrgSyncMaxAge = 24 * time.Hour

const claudeOrgSyncSchema = 1

// ClaudeOrgTeam is one team's entry as the server reported it.
type ClaudeOrgTeam struct {
	TeamID   string `json:"team_id"`
	TeamSlug string `json:"team_slug,omitempty"`
	// SyncedSkills are team-side skill names (agents/skills/<name>), not ox's
	// installed `<name>-team` directories.
	SyncedSkills []string  `json:"synced_skills"`
	FetchedAt    time.Time `json:"fetched_at"`
}

type claudeOrgSyncFile struct {
	SchemaVersion int             `json:"schema_version"`
	Teams         []ClaudeOrgTeam `json:"teams"`
}

// ClaudeOrgSync is what ox believes one team's Claude organization delivers.
type ClaudeOrgSync struct {
	TeamSlug string
	// Skills is sorted and contains only names ox would accept for a team skill.
	Skills    []string
	FetchedAt time.Time
	// Fresh is false when the record is older than ClaudeOrgSyncMaxAge, or dated
	// in the future (a clock that moved back is not a confirmation).
	Fresh bool
}

// Active reports whether ox should defer to the org plugin at all.
func (s ClaudeOrgSync) Active() bool { return s.Fresh && len(s.Skills) > 0 }

// Has reports whether the org plugin delivers the team skill name. Exact match
// only: Claude installs the export under the same lowercase name, so a case or
// Unicode variant is a different skill and must still be copied.
func (s ClaudeOrgSync) Has(name string) bool {
	if !s.Active() {
		return false
	}
	i := sort.SearchStrings(s.Skills, name)
	return i < len(s.Skills) && s.Skills[i] == name
}

// Revision identifies the delivered set, or "" when ox is not deferring. It is
// folded into the skill catalog revision so that a change in what the org
// delivers re-plans at the next session start, without walking the team tree.
// Empty when inactive keeps every repository that never connected a Claude org
// on exactly the revision it had before this existed.
func (s ClaudeOrgSync) Revision() string {
	if !s.Active() {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(s.Skills, "\n")))
	return hex.EncodeToString(sum[:6])
}

// LoadClaudeOrgSync returns the recorded entry for teamID on endpoint ep.
//
// A missing file or team is a zero value with a nil error: nobody connected an
// org. An unreadable or corrupt file is a zero value WITH an error, so a
// diagnostic can say so; the planner ignores the error, which leaves it copying
// every skill.
func LoadClaudeOrgSync(ep, teamID string, now time.Time) (ClaudeOrgSync, error) {
	file := paths.ClaudeOrgSyncFile(ep)
	if file == "" || teamID == "" {
		return ClaudeOrgSync{}, nil
	}
	recorded, err := readClaudeOrgSyncFile(file)
	if err != nil {
		return ClaudeOrgSync{}, err
	}
	for _, team := range recorded.Teams {
		if team.TeamID != teamID {
			continue
		}
		age := now.Sub(team.FetchedAt)
		return ClaudeOrgSync{
			TeamSlug:  team.TeamSlug,
			Skills:    acceptedSkillNames(team.SyncedSkills),
			FetchedAt: team.FetchedAt,
			Fresh:     age >= 0 && age < ClaudeOrgSyncMaxAge,
		}, nil
	}
	return ClaudeOrgSync{}, nil
}

// RecordClaudeOrgSync merges what the server just reported for teams into the
// cache for endpoint ep.
//
// Merged per team rather than replaced: a process authenticated as a single
// team (a team access token) sees only that team, and replacing the file would
// erase every other team's entry until the next personal-token refresh. Entries
// for teams the server stopped reporting simply age out past
// ClaudeOrgSyncMaxAge and are pruned here.
//
// A team reported with no synced skills is removed, and an empty result deletes
// the file: a server that predates the field leaves nothing on disk.
//
// Two daemons writing at once can lose one update; it is rewritten at the next
// hourly discovery, and the atomic rename means a reader never sees a torn file.
func RecordClaudeOrgSync(ep string, teams []ClaudeOrgTeam, now time.Time) error {
	file := paths.ClaudeOrgSyncFile(ep)
	if file == "" {
		return nil
	}
	recorded, err := readClaudeOrgSyncFile(file)
	if err != nil {
		// A corrupt cache is rebuilt from this response rather than kept: it is
		// derived data, and refusing to write would leave it corrupt forever.
		recorded = claudeOrgSyncFile{}
	}

	reported := make(map[string]bool, len(teams))
	var merged []ClaudeOrgTeam
	for _, team := range teams {
		if team.TeamID == "" || reported[team.TeamID] {
			continue
		}
		reported[team.TeamID] = true
		skills := acceptedSkillNames(team.SyncedSkills)
		if len(skills) == 0 {
			continue
		}
		merged = append(merged, ClaudeOrgTeam{
			TeamID: team.TeamID, TeamSlug: team.TeamSlug, SyncedSkills: skills, FetchedAt: now,
		})
	}
	for _, team := range recorded.Teams {
		if reported[team.TeamID] || now.Sub(team.FetchedAt) >= ClaudeOrgSyncMaxAge {
			continue
		}
		merged = append(merged, team)
	}

	if len(merged) == 0 {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove claude org sync cache: %w", err)
		}
		return nil
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].TeamID < merged[j].TeamID })
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("create claude org sync cache dir: %w", err)
	}
	return fileutil.AtomicWriteJSON(file, claudeOrgSyncFile{SchemaVersion: claudeOrgSyncSchema, Teams: merged}, 0o600)
}

func readClaudeOrgSyncFile(file string) (claudeOrgSyncFile, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return claudeOrgSyncFile{}, nil
	}
	if err != nil {
		return claudeOrgSyncFile{}, fmt.Errorf("read claude org sync cache: %w", err)
	}
	var recorded claudeOrgSyncFile
	if err := json.Unmarshal(data, &recorded); err != nil {
		return claudeOrgSyncFile{}, fmt.Errorf("parse claude org sync cache %s: %w", file, err)
	}
	if recorded.SchemaVersion != claudeOrgSyncSchema {
		// A newer ox may mean something else by the same file. Not deferring is
		// the safe reading of a shape this binary does not understand.
		return claudeOrgSyncFile{}, fmt.Errorf("claude org sync cache %s has schema %d, this ox reads %d",
			file, recorded.SchemaVersion, claudeOrgSyncSchema)
	}
	return recorded, nil
}

// acceptedSkillNames keeps only names ox would accept for a team skill, sorted
// and deduplicated. The server applies the same rule; a name ox would refuse
// could never match a skill ox projects, so carrying it would only reach a
// terminal through `ox skills status`.
func acceptedSkillNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if seen[name] || !teamdocs.ValidTeamSkillName(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
