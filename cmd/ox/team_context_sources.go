package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/internal/constants"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/teamdocs"
)

// mountReadBudget bounds every read of mounted content in one prime, together.
//
// Discovery is bounded separately and that is not enough: once a mounted root
// is resolved, the reads that follow are the expensive ones. They are also not
// local — touching a dataless file makes macOS call the File Provider
// extension, which fetches from the Drive API — so a signed-out or stalled
// drive could hang `ox agent prime` long after the discovery budget it was
// supposed to be bounded by had passed.
//
// One budget for all of them, not one each: six bounded reads with their own
// deadlines is six times the stall a user actually feels. Once it is spent,
// every later mounted read contributes nothing.
const mountReadBudget = 5 * time.Second

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
// roots returns and nothing else.
type teamReadSources struct {
	checkout string
	mount    string // empty when nothing is mounted, or reads are not opted in

	// mountDeadline is when this prime stops waiting on the drive, shared by
	// every mounted read.
	//
	// Zero means unbounded. That is the right default for a value built
	// directly — tests reading a temp directory are not going to block on a
	// network — and newTeamReadSources, the only constructor production uses,
	// always sets it when a mount is present.
	mountDeadline time.Time
}

// newTeamReadSources resolves the roots for a team. The checkout is always a
// source; the mount joins it only when a drive carries this team and the
// session opted in.
func newTeamReadSources(checkout, teamID string) teamReadSources {
	sources := teamReadSources{checkout: checkout}
	if mounted, ok := mountedTeamRoot(teamID); ok {
		sources.mount = mounted
		sources.mountDeadline = time.Now().Add(mountReadBudget)
	}
	return sources
}

// roots lists every root to read, most authoritative first.
//
// Callers that read the filesystem should prefer readAcrossSources, which
// applies the mount's budget. This is for callers that only need the paths.
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
// "Missing" means the not-exist error and nothing else, matching what the
// single-root read did: a permission or I/O error is a reason to try reading
// and let each document report its own failure, not a reason to declare the
// whole team absent.
func (s teamReadSources) anyExists() bool {
	if s.checkout != "" {
		if _, err := os.Stat(s.checkout); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	present, ok := readMounted(s, func(root string) bool {
		_, err := os.Stat(root)
		return !errors.Is(err, fs.ErrNotExist)
	})
	return ok && present
}

// mountSource opens the record of what the drive contributed, nil when no drive
// was read.
func (s teamReadSources) mountSource() *prime.TeamMountSource {
	if s.mount == "" {
		return nil
	}
	return &prime.TeamMountSource{Path: s.mount}
}

// readMounted runs one read against the mounted root and abandons it when this
// prime's budget for the drive is gone.
//
// Reports false when there is no mount, when the budget is already spent, or
// when this read outlasts it — all of which mean the same thing to a caller
// under a union: the checkout answered alone, which is always a valid result.
// That is why the bound can be this blunt, and it is a property the secondary-
// source design buys. Replacing the checkout could not drop a slow read this
// cheaply, because there would be nothing left.
//
// A read that runs out of budget is abandoned, not stopped — filesystem calls
// cannot be canceled. It must therefore be a pure read: it returns a value and
// touches nothing the caller has moved on to, or the abandoned goroutine races
// whoever comes next.
func readMounted[T any](s teamReadSources, read func(root string) T) (T, bool) {
	var zero T
	if s.mount == "" {
		return zero, false
	}
	if s.mountDeadline.IsZero() {
		return read(s.mount), true
	}

	remaining := time.Until(s.mountDeadline)
	if remaining <= 0 {
		return zero, false
	}

	done := make(chan T, 1)
	go func() { done <- read(s.mount) }()

	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case value := <-done:
		return value, true
	case <-timer.C:
		return zero, false
	}
}

// readAcrossSources runs read against every source, most authoritative first,
// and drops the mounted result when the drive runs out of budget.
//
// The checkout is read without a deadline, exactly as it was before any of this
// existed: it is a local clone, and bounding it would only invent a way to lose
// context that is sitting on the disk.
func readAcrossSources[T any](s teamReadSources, read func(root string) T) []T {
	results := make([]T, 0, 2)
	if s.checkout != "" {
		results = append(results, read(s.checkout))
	}
	if value, ok := readMounted(s, read); ok {
		results = append(results, value)
	}
	return results
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
	perSource := readAcrossSources(sources, func(root string) []teamdocs.TeamDoc {
		docs, _ := teamdocs.DiscoverDocs(root)
		return docs
	})
	return mergeByKey(perSource, func(d teamdocs.TeamDoc) string { return d.Name })
}

// discoverTeamRulesAcross unions team rules across sources, keyed by rule name.
//
// DiscoverRules already resolves agents/rules over coworkers/rules within one
// root; this extends the same first-wins rule across roots.
func discoverTeamRulesAcross(sources teamReadSources, repoSlug string) ([]teamdocs.TeamRule, int) {
	perSource := readAcrossSources(sources, func(root string) []teamdocs.TeamRule {
		rules, _ := teamdocs.DiscoverRules(root, repoSlug)
		return rules
	})
	return mergeByKey(perSource, func(r teamdocs.TeamRule) string { return r.Name })
}

// discoverCustomizationsAcross unions coworker customizations across sources.
//
// Scalar fields take the first root that has them; agents and commands union by
// name. The result is shaped exactly like a single-root discovery, so the
// caller that turns it into prime output does not need to know there were two.
func discoverCustomizationsAcross(sources teamReadSources) (*claude.TeamCustomizations, int, int) {
	perSource := readAcrossSources(sources, func(root string) *claude.TeamCustomizations {
		found, err := claude.DiscoverAll(root)
		if err != nil || found == nil {
			return &claude.TeamCustomizations{TeamPath: root}
		}
		return found
	})
	if len(perSource) == 0 {
		return nil, 0, 0
	}

	merged := &claude.TeamCustomizations{TeamPath: perSource[0].TeamPath}
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
	if sources.checkout != "" {
		if _, err := os.Stat(filepath.Join(sources.checkout, rel)); err == nil {
			return sources.checkout, true
		}
	}
	present, ok := readMounted(sources, func(root string) bool {
		_, err := os.Stat(filepath.Join(root, rel))
		return err == nil
	})
	if ok && present {
		return sources.mount, true
	}
	return "", false
}

// readFileAcross returns the contents of rel from the first root carrying it,
// and which root that was.
func readFileAcross(sources teamReadSources, rel string) (string, []byte, bool) {
	if sources.checkout != "" {
		//nolint:gosec // a team-context root the caller owns plus a fixed relative name
		if content, err := os.ReadFile(filepath.Join(sources.checkout, rel)); err == nil {
			return sources.checkout, content, true
		}
	}

	type fileRead struct {
		content []byte
		ok      bool
	}
	read, done := readMounted(sources, func(root string) fileRead {
		//nolint:gosec // a mount root validated by filesmount plus a fixed relative name
		content, err := os.ReadFile(filepath.Join(root, rel))
		return fileRead{content: content, ok: err == nil}
	})
	if done && read.ok {
		return sources.mount, read.content, true
	}
	return "", nil, false
}

// mountMemory is what one memory read of the drive found, gathered before any
// of it is applied.
//
// Read and apply are separate on purpose: readMounted abandons a read that
// outlasts the budget rather than stopping it, so the read must not touch the
// teamContextInfo that prime has already moved on to using.
type mountMemory struct {
	content string
	soul    string
	team    string
	guide   string
	daily   []string
	weekly  []string
	monthly []string
}

// fillTeamMemoryGapsFromMount adds what the drive carries and the checkout does
// not, and reports how many timeline files that was.
//
// Called after loadTeamMemory has run on the checkout, so every field it can
// touch is one the checkout left empty. MEMORY.md and the SOUL/TEAM pointers
// are single documents: the checkout's copy stands if it exists at all. The
// timeline directories union by filename, because a day the checkout is missing
// is a day of team history that would otherwise be silently absent.
func fillTeamMemoryGapsFromMount(info *teamContextInfo, sources teamReadSources) int {
	found, ok := readMounted(sources, func(root string) mountMemory {
		return mountMemory{
			content: claude.ReadFirstLines(
				filepath.Join(root, "MEMORY.md"), constants.MaxInlineContextLines),
			soul:    existingPath(filepath.Join(root, "SOUL.md")),
			team:    existingPath(filepath.Join(root, "TEAM.md")),
			guide:   existingPath(filepath.Join(root, "memory", "GUIDE.md")),
			daily:   discoverMemoryFiles(filepath.Join(root, "memory", "daily")),
			weekly:  discoverMemoryFiles(filepath.Join(root, "memory", "weekly")),
			monthly: discoverMemoryFiles(filepath.Join(root, "memory", "monthly")),
		}
	})
	if !ok {
		return 0
	}

	if info.MemoryContent == "" {
		info.MemoryContent = found.content
	}
	for _, pointer := range []struct {
		field *string
		value string
	}{
		{&info.SoulHint, found.soul},
		{&info.TeamHint, found.team},
		{&info.ObservationGuideHint, found.guide},
	} {
		if *pointer.field == "" {
			*pointer.field = pointer.value
		}
	}

	added := 0
	for _, timeline := range []struct {
		files   *[]string
		mounted []string
	}{
		{&info.MemoryDaily, found.daily},
		{&info.MemoryWeekly, found.weekly},
		{&info.MemoryMonthly, found.monthly},
	} {
		merged, fromMount := mergeByKey(
			[][]string{*timeline.files, timeline.mounted},
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

// existingPath returns path when it exists, and "" when it does not.
func existingPath(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}
