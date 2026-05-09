package daemon

import "time"

// intervalForType returns the daemon sync cadence for a given kb type.
// Centralized here so all sync paths converge on one selection point —
// future migrations (ox-yvc1.3) replace per-callsite switches with calls
// to this function.
//
// Cadence rationale: high-churn types (personal/profile/team mutate as
// users edit and AI coworkers write) get the faster team-context cadence;
// lower-churn types (repo, custom) get the slower read cadence used for
// ledgers historically. Unknown types fall through to the read cadence —
// safe default that keeps an unrecognized kb visible without hammering
// the API.
func intervalForType(cfg *Config, kbType string) time.Duration {
	if cfg == nil {
		return 60 * time.Second
	}
	switch kbType {
	case "personal", "profile", "team":
		if cfg.TeamContextSyncInterval > 0 {
			return cfg.TeamContextSyncInterval
		}
	}
	if cfg.SyncIntervalRead > 0 {
		return cfg.SyncIntervalRead
	}
	return 60 * time.Second
}
