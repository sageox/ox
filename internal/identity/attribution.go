package identity

import (
	"os"
	"slices"
	"strings"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/runtime"
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
//  0. The AI coworker a team token acts as (auth.TeamCoworker) — when named,
//     it is the whole attribution; nothing below is consulted
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

	// AI is true when the work belongs to the AI coworker a team token acts
	// as. Username is then the coworker's slug (or agt_ id), Name and
	// DisplayName its name as set in SageOx, unabbreviated, and Email is "".
	AI bool
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
	// The git identity, $USER and display_name config on the machine running
	// a team token describe that machine, not the coworker doing the work.
	if c, _ := auth.TeamCoworker(ep); c != nil {
		return Attribution{Username: c.Username(), Name: c.Name(), DisplayName: c.Name(), AI: true}
	}

	var username string

	// 1. SageOx account (verified identity)
	_, email, name := signedInUser(ep)
	verified := &repotools.GitIdentity{Email: email, Name: name}

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

	// In an Amp orb, git's identity is the orb's, not the person's: an Amp
	// project orb commits as its project's commit author, "Amp", which named
	// and credited every session "amp" (sageox/ox#1425). There the verified
	// SageOx identity names the work. Only Amp orbs: the other hosts that end
	// with the agent (Claude Code cloud, Devin) were not seen to have this
	// problem, and elsewhere the slug stays git's so session names don't move.
	if (verified.Email != "" || verified.Name != "") && runtime.IsAmpOrb() {
		username = verified.Slug()
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

// signedInUser returns the account ID, email and full name of the SageOx
// account signed in at ep, or "" for all three.
func signedInUser(ep string) (id, email, name string) {
	if ep == "" {
		return "", "", ""
	}
	if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil {
		id, email, name = token.UserInfo.UserID, token.UserInfo.Email, token.UserInfo.Name
		if id == "" {
			// A login stored before user_id existed names the account only in
			// its access token's subject; LocalUserID recovers it without
			// treating a team token's AI coworker as a signed-in account.
			id = auth.LocalUserID(ep)
		}
	}
	// A personal SAGEOX_TOKEN carries no UserInfo; the server names its owner.
	if email == "" && name == "" {
		if u := auth.PersonalTokenUser(ep); u != nil {
			id, email, name = u.ID, u.Email, u.Name
		}
	}
	return id, email, name
}

// accountEmailDomain ends the address SignedInAuthor commits under. The
// account's real email stays local (PRIVACY RULE); ".invalid" (RFC 2606) never
// resolves, so the address reaches no one.
const accountEmailDomain = "@sageox-account.invalid"

// SignedInAuthor is who ox commits as in a clone whose origin is remoteURL: the
// SageOx account signed in on that clone's git server, named by its DisplayName
// and an address built from its account ID, because those commits reach
// teammates (PRIVACY RULE). An expired sign-in still counts; only signing out
// ends it. Both results are "" when no signed-in account owns that git server,
// which leaves git's own identity in charge.
func SignedInAuthor(remoteURL, configDisplayName string) (name, email string) {
	signedIn := signedInEndpoints()
	for _, ep := range signedIn {
		if !gitserver.RemoteOnGitServer(remoteURL, ep, gitServerFor(ep, signedIn)) {
			continue
		}
		id, accountEmail, fullName := signedInUser(ep)
		if id == "" {
			return "", ""
		}
		name = strings.TrimSpace(configDisplayName)
		if name == "" {
			name = deriveDisplayName(accountEmail, fullName, "")
		}
		return name, id + accountEmailDomain
	}
	return "", ""
}

// gitServerFor returns the git server the API named for ep, or "" when that is
// unknown, so two servers on one host (local stacks on different ports) never
// match each other's clones. Credential records are keyed by host, so such
// stacks share one. A record ep's own token fetched is ep's. One fetched with
// an earlier token (a refreshed or pre-hash login) is trusted only when no
// other sign-in shares the host and could own it.
func gitServerFor(ep string, signedIn []string) string {
	creds, err := gitserver.LoadCredentialsForEndpoint(ep)
	if err != nil || creds == nil {
		return ""
	}
	if token, err := auth.GetTokenForEndpoint(ep); err == nil && token != nil &&
		creds.BearerTokenHash == gitserver.BearerTokenFingerprint(token.AccessToken) {
		return creds.ServerURL
	}
	host := endpoint.NormalizeSlug(ep)
	for _, other := range signedIn {
		if other != ep && endpoint.NormalizeSlug(other) == host {
			return ""
		}
	}
	return creds.ServerURL
}

// signedInEndpoints lists every endpoint with a sign-in: a usable SAGEOX_TOKEN
// and each stored token, expired or not. GetLoggedInEndpoints alone drops an
// expired token that a refresh would renew, and with it the commit author.
func signedInEndpoints() []string {
	endpoints := auth.GetLoggedInEndpoints()
	stored, _ := auth.ListEndpoints()
	for _, ep := range stored {
		if !slices.Contains(endpoints, ep) {
			endpoints = append(endpoints, ep)
		}
	}
	return endpoints
}

// AttributionUsername returns a lowercase, filesystem-safe slug for the current user.
// Use for: folder names, file paths, principal IDs.
func AttributionUsername(ep, configDisplayName string) string {
	return ResolveAttribution(ep, configDisplayName).Username
}

// LocalAttributionUsername is the Username AttributionUsername gives a person
// on an ordinary machine, computed from local sources only: git's identity,
// then $USER. It never consults a token and so never reaches the network,
// which makes it safe for per-keystroke surfaces like the status line. It
// differs from AttributionUsername only where that one is decided by a
// token: a team token's AI coworker, and an Amp orb's verified identity.
func LocalAttributionUsername() string {
	if gitIdent, err := repotools.DetectGitIdentity(); err == nil && gitIdent != nil {
		if slug := gitIdent.Slug(); slug != "" {
			return slug
		}
	}
	if u := osUsername(); u != "" {
		return strings.ToLower(u)
	}
	return "anonymous"
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
