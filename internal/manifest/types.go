package manifest

// ResolveMode specifies how rebase conflicts on a path should be handled.
type ResolveMode string

const (
	// ResolveModeAuto accepts the incoming version (accept-theirs during rebase).
	// Safe for idempotent data that will be re-fetched on the next sync cycle.
	ResolveModeAuto ResolveMode = "auto"

	// ResolveModeNone blocks auto-resolution — conflicts require manual intervention.
	// This is the default for paths not covered by any resolve rule.
	ResolveModeNone ResolveMode = "none"

	// Future modes (not yet implemented):
	// ResolveModeSemantic — LLM semantic merge for agent-written content (memory/)
	// ResolveModeLatest  — remote-wins for cloud-authored content (discussions/)
)

// ResolveRule maps a path prefix to a conflict resolution strategy.
// Manifest syntax: `resolve <mode> <path>`
type ResolveRule struct {
	Mode ResolveMode
	Path string
}

// ManifestConfig holds parsed sync manifest configuration.
type ManifestConfig struct {
	Version         int
	Includes        []string // paths to sparse checkout
	Denies          []string // hard-blocked paths
	SyncIntervalMin int      // minutes between syncs
	GCIntervalDays  int      // days between reclones

	// ResolveRules maps path prefixes to conflict resolution modes.
	// Populated from `resolve <mode> <path>` manifest directives,
	// or DefaultResolveRules when no resolve directives are present.
	// Most specific prefix wins when multiple rules match a file.
	ResolveRules []ResolveRule
}

const (
	DefaultSyncIntervalMin = 5
	MinSyncIntervalMin     = 1

	DefaultGCIntervalDays = 7
	MinGCIntervalDays     = 1
	MaxGCIntervalDays     = 90

	SupportedVersion = 1
)
