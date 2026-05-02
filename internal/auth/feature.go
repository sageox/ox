package auth

import (
	"os"
	"strings"
)

// =============================================================================
// SAGEOX MULTIPLAYER PHILOSOPHY
// =============================================================================
//
// SageOx is fundamentally about multiplayer collaboration. Being logged in is
// REQUIRED because:
//
//   1. Team context sharing requires authentication to the SageOx cloud
//   2. Guidance is fetched from authenticated team-specific endpoints
//   3. Sessions sync to shared ledgers
//   4. The entire value proposition is team-scale knowledge sharing
//
// Offline/disconnected mode is NOT supported for API operations because:
//
//   - It contradicts the multiplayer model
//   - Adds significant complexity for edge cases
//   - Creates stale data risks and version compatibility issues
//   - Post-2023 coding agents basically require connectivity anyway
//
// IMPORTANT DISTINCTION:
//
// While API operations require connectivity, the underlying git repositories
// (ledger and team context) work perfectly fine offline:
//
//   - Git commits can be made offline and pushed when reconnected
//   - Local changes are preserved across network outages
//   - The daemon handles network disconnection gracefully (not a failure mode)
//
// This is the best of both worlds: multiplayer collaboration when connected,
// local persistence when disconnected, automatic sync when reconnected.
// =============================================================================

// IsAuthRequired checks if authentication is required based on the FEATURE_AUTH environment variable
func IsAuthRequired() bool {
	value := strings.ToLower(os.Getenv("FEATURE_AUTH"))
	return value == "true" || value == "1" || value == "yes"
}

// IsCloudEnabled checks if cloud features are enabled based on the FEATURE_CLOUD environment variable
func IsCloudEnabled() bool {
	value := strings.ToLower(os.Getenv("FEATURE_CLOUD"))
	return value == "true" || value == "1" || value == "yes"
}

// IsPostMVPEnabled checks if post-MVP features (ox completion) are enabled.
// These features are for power users and not included in the MVP release.
// Set FEATURE_POST_MVP=true to enable.
func IsPostMVPEnabled() bool {
	value := strings.ToLower(os.Getenv("FEATURE_POST_MVP"))
	return value == "true" || value == "1" || value == "yes"
}

// IsMemoryEnabled checks if memory features (ox memory, ox agent <id> distill) are enabled.
// Memory is experimental and not included in the default experience.
// Set FEATURE_MEMORY=true to enable.
func IsMemoryEnabled() bool {
	value := strings.ToLower(os.Getenv("FEATURE_MEMORY"))
	return value == "true" || value == "1" || value == "yes"
}
