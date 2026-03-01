package manifest

// ManifestConfig holds parsed sync manifest configuration.
type ManifestConfig struct {
	Version         int
	Includes        []string // paths to sparse checkout
	Denies          []string // hard-blocked paths
	SyncIntervalMin int      // minutes between syncs
	GCIntervalDays  int      // days between reclones
}

const (
	DefaultSyncIntervalMin = 5
	MinSyncIntervalMin     = 1

	DefaultGCIntervalDays = 7
	MinGCIntervalDays     = 1
	MaxGCIntervalDays     = 90

	SupportedVersion = 1
)
