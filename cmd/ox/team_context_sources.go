package main

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/constants"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/teamdocs"
)

// teamReadSources are the roots one team's context is read from, most
// authoritative first.
//
// Both roots are read on every prime. The git checkout is primary: it is what
// `ox sync` writes, what the daemon reports staleness for, what every other ox
// command reads, and the copy a person can inspect with plain git. A mounted
// SageOx Files drive is a second source read alongside it — it contributes
// documents the checkout does not carry, and where both carry the same document
// the checkout wins.
//
// That precedence, not the reading, is what makes the drive secondary. Reading
// both is the point: neither source replaces the other, so a stale checkout
// cannot hide a document the drive has, and an unavailable drive cannot hide a
// document the checkout has. Promoting the drive later is a change to the order
// this returns and nothing else.
type teamReadSources struct {
	checkout string
	mount    string // empty when nothing is mounted, or reads are not opted in
}

// newTeamReadSources resolves the roots for a team. The checkout is always a
// source; the mount joins it only when a drive carries this team and the
// session opted in.
func newTeamReadSources(checkout, teamID string) teamReadSources {
	sources := teamReadSources{checkout: checkout}
	if mounted, ok := mountedTeamRoot(teamID); ok {
		sources.mount = mounted
	}
	return sources
}

// roots lists every root to read, most authoritative first.
func (s teamReadSources) roots() []string {
	roots := make([]string, 0, 2)
	if s.checkout != "" {
		roots = append(roots, s.checkout)
	}
	if s.mount != "" {
		roots = append(roots, s.mount)
	}
	return roots
}

// anyExists reports whether at least one root is present on disk.
//
// A team whose checkout has not cloned yet but whose drive is mounted still has
// context to offer, and the reverse is the ordinary case on a machine with no
// drive. Only when neither answers is the team genuinely unavailable.
//
// "Missing" means IsNotExist and nothing else, matching what the single-root
// read did: a permission or I/O error is a reason to try reading and let each
// document report its own failure, not a reason to declare the whole team
// absent.
func (s teamReadSources) anyExists() bool {
	for _, root := range s.roots() {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// mountSource opens the record of what the drive contributed, nil when no drive
// was read.
func (s teamReadSources) mountSource() *prime.TeamMountSource {
	if s.mount == "" {
		return nil
	}
	return &prime.TeamMountSource{Path: s.mount}
}

// mergeByKey unions per-source item lists into one, keeping the first
// occurrence of each key.
//
// perSource is ordered most-authoritative-first, so "first wins" is the whole
// precedence rule. The second return counts entries that came from a source
// other than the first — what the secondary source actually added, which is the
// only way to tell a drive that is earning its place from one that is echoing
// the checkout.
func mergeByKey[T any](perSource [][]T, key func(T) string) (merged []T, fromSecondary int) {
	seen := make(map[string]bool)
	for i, items := range perSource {
		for _, item := range items {
			k := key(item)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			merged = append(merged, item)
			if i > 0 {
				fromSecondary++
			}
		}
	}
	return merged, fromSecondary
}

// discoverTeamDocsAcross unions the team docs catalog across sources, keyed by
// filename.
func discoverTeamDocsAcross(sources teamReadSources) ([]teamdocs.TeamDoc, int) {
	perSource := make([][]teamdocs.TeamDoc, 0, 2)
	for _, root := range sources.roots() {
		docs, _ := teamdocs.DiscoverDocs(root)
		perSource = append(perSource, docs)
	}
	return mergeByKey(perSource, func(d teamdocs.TeamDoc) string { return d.Name })
}

// discoverTeamRulesAcross unions team rules across sources, keyed by rule name.
//
// DiscoverRules already resolves agents/rules over coworkers/rules within one
// root; this extends the same first-wins rule across roots.
func discoverTeamRulesAcross(sources teamReadSources, repoSlug string) ([]teamdocs.TeamRule, int) {
	perSource := make([][]teamdocs.TeamRule, 0, 2)
	for _, root := range sources.roots() {
		rules, _ := teamdocs.DiscoverRules(root, repoSlug)
		perSource = append(perSource, rules)
	}
	return mergeByKey(perSource, func(r teamdocs.TeamRule) string { return r.Name })
}

// discoverCustomizationsAcross unions coworker customizations across sources.
//
// Scalar fields take the first root that has them; agents and commands union by
// name. The result is shaped exactly like a single-root discovery, so the
// caller that turns it into prime output does not need to know there were two.
func discoverCustomizationsAcross(sources teamReadSources) (*claude.TeamCustomizations, int, int) {
	roots := sources.roots()
	if len(roots) == 0 {
		return nil, 0, 0
	}

	perSource := make([]*claude.TeamCustomizations, 0, len(roots))
	for _, root := range roots {
		found, err := claude.DiscoverAll(root)
		if err != nil || found == nil {
			found = &claude.TeamCustomizations{TeamPath: root}
		}
		perSource = append(perSource, found)
	}

	merged := &claude.TeamCustomizations{TeamPath: roots[0]}
	for _, found := range perSource {
		if !merged.HasClaudeMD && found.HasClaudeMD {
			merged.ClaudeMDPath, merged.HasClaudeMD = found.ClaudeMDPath, true
		}
		if !merged.HasAgentsMD && found.HasAgentsMD {
			merged.AgentsMDPath, merged.HasAgentsMD = found.AgentsMDPath, true
		}
		if !merged.HasAgentsAgentsMD && found.HasAgentsAgentsMD {
			merged.AgentsAgentsMDPath = found.AgentsAgentsMDPath
			merged.AgentsAgentsMDContent = found.AgentsAgentsMDContent
			merged.HasAgentsAgentsMD = true
		}
		if !merged.HasAgentsIndex && found.HasAgentsIndex {
			merged.AgentsIndexPath, merged.HasAgentsIndex = found.AgentsIndexPath, true
		}
	}

	agentLists := make([][]claude.Agent, 0, len(perSource))
	commandLists := make([][]claude.Command, 0, len(perSource))
	for _, found := range perSource {
		agentLists = append(agentLists, found.Agents)
		commandLists = append(commandLists, found.Commands)
	}
	var addedAgents, addedCommands int
	merged.Agents, addedAgents = mergeByKey(agentLists, func(a claude.Agent) string { return a.Name })
	merged.Commands, addedCommands = mergeByKey(commandLists, func(c claude.Command) string { return c.Name })

	return merged, addedAgents, addedCommands
}

// firstRootWith returns the first root carrying rel, most authoritative first.
func firstRootWith(sources teamReadSources, rel string) (string, bool) {
	for _, root := range sources.roots() {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			return root, true
		}
	}
	return "", false
}

// fillTeamMemoryGapsFromMount adds what the drive carries and the checkout does
// not, and reports how many timeline files that was.
//
// Called after loadTeamMemory has run on the checkout, so every field it can
// touch is one the checkout left empty. MEMORY.md and the SOUL/TEAM pointers
// are single documents: the checkout's copy stands if it exists at all. The
// timeline directories union by filename, because a day the checkout is missing
// is a day of team history that would otherwise be silently absent.
func fillTeamMemoryGapsFromMount(info *teamContextInfo, mountRoot string) int {
	if mountRoot == "" {
		return 0
	}

	if info.MemoryContent == "" {
		if content := claude.ReadFirstLines(
			filepath.Join(mountRoot, "MEMORY.md"), constants.MaxInlineContextLines,
		); content != "" {
			info.MemoryContent = content
		}
	}

	for _, pointer := range []struct {
		field *string
		rel   string
	}{
		{&info.SoulHint, "SOUL.md"},
		{&info.TeamHint, "TEAM.md"},
		{&info.ObservationGuideHint, filepath.Join("memory", "GUIDE.md")},
	} {
		if *pointer.field != "" {
			continue
		}
		path := filepath.Join(mountRoot, pointer.rel)
		if _, err := os.Stat(path); err == nil {
			*pointer.field = path
		}
	}

	added := 0
	for _, timeline := range []struct {
		files *[]string
		dir   string
	}{
		{&info.MemoryDaily, "daily"},
		{&info.MemoryWeekly, "weekly"},
		{&info.MemoryMonthly, "monthly"},
	} {
		mounted := discoverMemoryFiles(filepath.Join(mountRoot, "memory", timeline.dir))
		merged, fromMount := mergeByKey(
			[][]string{*timeline.files, mounted},
			func(name string) string { return name },
		)
		// discoverMemoryFiles sorts each root reverse-chronologically; the union
		// of two sorted lists is not sorted, so restore the order the emitter and
		// the reader both assume.
		sort.Sort(sort.Reverse(sort.StringSlice(merged)))
		*timeline.files = merged
		added += fromMount
	}
	return added
}
