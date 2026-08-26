package manifest

import "fmt"

// RepoKind identifies which kind of ox-synced repo a manifest belongs to.
//
// It selects the fallback include set used when .sageox/sync.manifest is
// missing or unparseable. Team contexts and knowledge bubbles have different
// on-disk shapes, so a single shared fallback silently checks out the wrong
// tree for one of them — which is exactly what happened to bubbles: they
// inherited the team-context set and never materialized knowledge/.
//
// Callers must pass a kind explicitly. There is deliberately no "default"
// repo kind: an unset value is a plumbing bug, not a supported input, and
// fallbackIncludesFor panics rather than guessing one.
type RepoKind string

const (
	// RepoKindTeamContext is a team-context repo — SOUL.md, docs/,
	// discussions/, coworkers/, and the team memory tree.
	RepoKindTeamContext RepoKind = "team_context"

	// RepoKindKB is a knowledge bubble — control-plane metadata plus the
	// curated knowledge/ tree the curator writes.
	RepoKindKB RepoKind = "kb"
)

// teamContextFallbackIncludes is the hardcoded sparse set for team-context
// repos when the manifest is missing or unparseable.
var teamContextFallbackIncludes = []string{
	".sageox/",
	"SOUL.md",
	"TEAM.md",
	"MEMORY.md",
	"AGENTS.md",
	"memory/",
	"docs/",
	"agents/",
	"coworkers/",
	"discussions/", // all discussion artifacts synced by cloud pipeline
	"agent-context/",
}

// kbFallbackIncludes is the hardcoded sparse set for knowledge bubbles.
//
// Deliberately narrow: a bubble is control-plane metadata (.sageox/), its
// agent-facing entry point (AGENTS.md), and the curated knowledge/ tree.
// Nothing from the team-context shape belongs here.
var kbFallbackIncludes = []string{
	".sageox/",
	"AGENTS.md",
	"knowledge/",
}

// fallbackIncludesFor returns the include set for kind.
//
// An unrecognized kind panics rather than degrading to some default. Degrading
// is what produced the bug this file exists to fix: a bubble quietly took the
// team-context shape and the only evidence was a log line nobody read. A new
// RepoKind added without a case here would reproduce that failure exactly, so
// it must fail at the first call instead.
//
// This is unreachable from any current path — every caller passes one of the
// two constants, and the compiler requires the argument. If a kind ever
// originates from config or an API response, validate it at that boundary
// before it reaches here; otherwise this becomes a remotely-triggerable crash.
func fallbackIncludesFor(kind RepoKind) []string {
	switch kind {
	case RepoKindKB:
		return kbFallbackIncludes
	case RepoKindTeamContext:
		return teamContextFallbackIncludes
	default:
		panic(fmt.Sprintf("manifest: unrecognized repo kind %q — every caller must pass an explicit RepoKind", string(kind)))
	}
}

// FallbackConfigFor returns a ManifestConfig with the hardcoded include set
// for kind plus sensible defaults. Used when .sageox/sync.manifest is
// missing, unparseable, or has an unknown version.
func FallbackConfigFor(kind RepoKind) *ManifestConfig {
	src := fallbackIncludesFor(kind)
	includes := make([]string, len(src))
	copy(includes, src)

	rules := make([]ResolveRule, len(DefaultResolveRules))
	copy(rules, DefaultResolveRules)

	return &ManifestConfig{
		Version:         SupportedVersion,
		Includes:        includes,
		Denies:          nil,
		SyncIntervalMin: DefaultSyncIntervalMin,
		GCIntervalDays:  DefaultGCIntervalDays,
		ResolveRules:    rules,
	}
}
