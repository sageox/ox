package version

// Version information set via ldflags during build
var (
	Version   = "0.3.0"
	BuildDate = "unknown"
	GitCommit = "unknown"
)

// Full returns a version string that is unique per build.
// Includes BuildDate so that dirty rebuilds (same git describe output)
// still produce a distinct identifier for daemon version-mismatch detection.
func Full() string {
	if BuildDate == "unknown" || BuildDate == "" {
		return Version
	}
	return Version + "+" + BuildDate
}
