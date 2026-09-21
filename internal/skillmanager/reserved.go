package skillmanager

import "strings"

// Reserved namespaces. These three strings are the whole ownership contract, so
// they live in one place: the installer, the ignore-file writer, the doctor
// checks, and the migration all have to agree on them exactly, and a copy that
// drifts by one character would either strand files in git or sweep files ox does
// not own.
const (
	// CLIBase is the exact reserved name ox uses for its primary rule file
	// (.claude/rules/ox-cli.md). It has no trailing hyphen, so a glob written as
	// "ox-cli-*" would MISS it — a mistake worth naming here, because the miss is
	// silent: the file simply stays visible in every pull request.
	CLIBase = "ox-cli"

	// CLIPrefix marks everything else the ox binary ships. It deliberately carries
	// "-cli-" rather than being a bare "ox-": people author their own ox-named
	// skills (ox-kb, ox-cart), and a bare prefix would claim those too.
	CLIPrefix = CLIBase + "-"

	// TeamPrefix marks content synced from a team context. "sageox-" is used
	// rather than another "ox-" family name because it is unmistakably
	// vendor-owned, and team content carries a different trust story than
	// binary-anchored content.
	TeamPrefix = "sageox-team-"

	// CommittedOnRamp is the ONE surface that stays committed to the repository.
	// It is unprefixed on purpose so it can never match a reserved glob — it must
	// survive in git, being the only SageOx artifact present on a machine where
	// the CLI is not installed.
	CommittedOnRamp = "sageox"
)

// IsReservedName reports whether a skill/rule/command basename belongs to an
// ox-owned namespace.
//
// This answers the name-only REPORTING and IGNORING question. Unprefixed catalog
// entries are deliberately absent: availability is not ownership. They need a
// committed selection plus an exact ignore rule, or a verified in-band stamp.
//
// It is NOT the ownership predicate. Anything that decides whether ox may
// OVERWRITE bytes it has no record of writing must call IsReclaimableName
// instead — see the contrast documented there before reaching for this one.
//
// Anything not matched here belongs to the user and is never touched — including
// a name that merely starts with "ox-", which is why the CLI prefix is "ox-cli-".
//
// CommittedOnRamp is NOT reserved by this predicate: it is ox-authored but
// deliberately tracked, so callers that gitignore or sweep reserved paths must
// leave it alone.
func IsReservedName(name string) bool {
	name = strings.TrimSuffix(name, ".md")
	return name == CLIBase ||
		strings.HasPrefix(name, CLIPrefix) ||
		strings.HasPrefix(name, TeamPrefix)
}

// IsReclaimableName reports whether a name BY ITSELF is enough for ox to claim a
// skill directory it has no record of ever writing.
//
// It intentionally remains a separate predicate from IsReservedName even while
// their namespace sets match: this one authorizes destructive overwrite, while
// the other classifies a path. Future reporting exceptions must not silently
// widen the destructive boundary again.
//
// Only the PREFIXED namespaces qualify. "ox-cli-" and "sageox-team-" are
// namespaces ox declared and told people to stay out of, so a directory wearing
// one is ox's by contract even with no lockfile entry — the 0.15.0 inversion,
// reasoned out at the first-install reclaim in Plan.
//
// An unprefixed catalog name never qualifies, however certainly ox ships it.
// "post-cutoff" is ordinary English naming what the skill IS, not who authored
// it; a repository can already hold a hand-authored skill at exactly that path,
// written by someone who was never told the name was spoken for. Those names
// must earn ownership the honest way — a recorded lockfile digest, a recovery
// journal entry, or a legacy stamp — and a same-name stranger is reported as a
// conflict and left alone.
//
// CommittedOnRamp is excluded for the same reason it is excluded above: ox
// authors it, but it is tracked rather than owned-and-hidden.
func IsReclaimableName(name string) bool {
	name = strings.TrimSuffix(name, ".md")
	return name == CLIBase ||
		strings.HasPrefix(name, CLIPrefix) ||
		strings.HasPrefix(name, TeamPrefix)
}
