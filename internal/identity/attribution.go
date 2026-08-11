package identity

import (
	"os"
	"strings"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/repotools"
)

// Attribution holds user identity for attributing work.
// Each field has a precise definition — do not conflate them.
//
// PRIVACY RULE:
//
//	Anything written to the ledger, murmur files, or shared with teammates
//	MUST use DisplayName (not Name). The ledger is a shared repo — full
//	names are PII that teammates and future team members can read.
//
//	Name (full, unabbreviated) is ONLY for:
//	  - Local git commit author (git config, not shared via ledger)
//	  - Internal audit logs
//	  - Display to the user themselves (e.g., "Signed in as ...")
//
//	DisplayName (privacy-abbreviated) is for EVERYTHING ELSE:
//	  - Session meta.json in the ledger (team-visible)
//	  - Murmur files (team-visible)
//	  - Session list/view output (may be shown to teammates)
//	  - Any UI or metadata that could be shared beyond the user
//
// Fallback chain (best → worst):
//  1. SageOx OAuth (ox login) — verified identity
//  2. Config display_name (ox config) — DisplayName only
//  3. Git identity (git config user.name / user.email)
//  4. OS username ($USER/$USERNAME) — Username only
//  5. "Anonymous"/"anonymous"/"" — absolute fallback
//
// For auth checks, use auth.GetTokenForEndpoint() directly.
// Attribution intentionally never fails — it always returns something.
type Attribution struct {
	// Username is a short, lowercase, filesystem-safe slug.
	// NOT an email. NOT a display name.
	// Examples: "ryan", "port8080", "anonymous"
	// Always lowercase. Always non-empty.
	// Use for: folder names, file paths, principal IDs, slug matching.
	Username string

	// Email is the full email address.
	// NOT a username. NOT a display name.
	// Examples: "ryan@example.com", ""
	// May be "" if no email source is available.
	// Use for: git commit author field, internal contact identity.
	Email string

	// Name is the full, unabbreviated name as provided by the source.
	// NOT a username. NOT an email.
	// Examples: "Albert Einstein" (not PII — illustrative), "Anonymous"
	// Always non-empty.
	// PRIVACY: LOCAL ONLY. Do NOT write to ledger or shared contexts.
	// Use for: local git commit author, login display, internal audit.
	Name string

	// DisplayName is a privacy-safe abbreviated name for shared contexts.
	// NOT a username. NOT an email. NOT the full name.
	// Examples: "Albert E.", "port8080", "Anonymous"
	// Always non-empty.
	// PRIVACY: SAFE FOR SHARING. Use for ALL team-visible metadata.
	// Use for: session meta.json, murmur files, session list/view,
	// any output that could be seen by teammates.
	DisplayName string
}

// IsAnonymous reports whether attribution fell all the way through to step 5,
// the absolute fallback — i.e. no OAuth identity, no git identity, and no OS
// username was available.
//
// Exists because DisplayName is documented "always non-empty", which makes
// `DisplayName != ""` useless as a did-this-resolve test: a genuine name and a
// total resolution failure are both non-empty strings. Callers that write
// attribution into shared, later-queried artifacts (plan meta, session meta)
// need to tell those apart — otherwise "% of plans with an author" counts the
// failures as successes and stops measuring anything.
//
// Deliberately checks the underlying sources rather than string-matching
// "Anonymous", so a user genuinely named that is not misclassified.
func (a Attribution) IsAnonymous() bool {
	return a.Email == "" && a.Username == "anonymous"
}

// ResolveAttribution determines user attribution from all available sources.
// Never fails — always returns a valid Attribution with non-empty Username,
// Name, and DisplayName.
//
// ep is the normalized SageOx endpoint (used to look up OAuth token).
// configDisplayName is the user's configured display_name from ox config.
// Pass "" for either if unavailable.
func ResolveAttribution(ep, configDisplayName string) Attribution {
	var email, name, username string

	// 1. Try SageOx OAuth (verified identity)
	if ep != "" {
		if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil {
			if token.UserInfo.Email != "" {
				email = token.UserInfo.Email
			}
			if token.UserInfo.Name != "" {
				name = token.UserInfo.Name
			}
		}
	}

	// 2. Try git config identity (local, no network)
	if gitIdent, err := repotools.DetectGitIdentity(); err == nil && gitIdent != nil {
		if email == "" {
			email = gitIdent.Email
		}
		if name == "" {
			name = gitIdent.Name
		}
		if slug := gitIdent.Slug(); slug != "" {
			username = slug
		}
	}

	// 3. Fallback: OS username for username/slug
	if username == "" {
		if u := osUsername(); u != "" {
			username = strings.ToLower(u)
		}
	}

	// 4. Absolute fallbacks
	if username == "" {
		username = "anonymous"
	}
	if name == "" {
		if email != "" {
			name = extractLocalPart(email)
		} else if username != "anonymous" {
			name = username
		} else {
			name = "Anonymous"
		}
	}

	// derive privacy-safe display name
	displayName := strings.TrimSpace(configDisplayName)
	if displayName == "" {
		gitUsername := ""
		if gitIdent, err := repotools.DetectGitIdentity(); err == nil && gitIdent != nil {
			gitUsername = gitIdent.Slug()
		}
		displayName = deriveDisplayName(email, name, gitUsername)
	}

	return Attribution{
		Username:    username,
		Email:       email,
		Name:        name,
		DisplayName: displayName,
	}
}

// AttributionUsername returns a lowercase, filesystem-safe slug for the current user.
// Use for: folder names, file paths, principal IDs.
func AttributionUsername(ep, configDisplayName string) string {
	return ResolveAttribution(ep, configDisplayName).Username
}

// AttributionEmail returns the user's email address. May be "".
// Use for: git commit author field.
func AttributionEmail(ep, configDisplayName string) string {
	return ResolveAttribution(ep, configDisplayName).Email
}

// AttributionName returns the full, unabbreviated name.
// PRIVACY: LOCAL ONLY — do not write to ledger or shared contexts.
// Use for: local git commit author, login display, internal audit.
func AttributionName(ep, configDisplayName string) string {
	return ResolveAttribution(ep, configDisplayName).Name
}

// AttributionDisplayName returns a privacy-safe abbreviated name.
// PRIVACY: SAFE FOR SHARING — use for all team-visible metadata.
// Use for: session meta.json, murmur files, session list/view.
func AttributionDisplayName(ep, configDisplayName string) string {
	return ResolveAttribution(ep, configDisplayName).DisplayName
}

// osUsername returns the OS-level username from environment variables.
func osUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return os.Getenv("USERNAME") // Windows
}
