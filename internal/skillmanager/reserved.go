package skillmanager

import (
	"strings"

	"github.com/sageox/ox/extensions/skills"
)

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

// IsReservedName reports whether a skill/rule/command basename is one ox ships.
//
// This answers the REPORTING and IGNORING question: "does ox write this name, so
// git should not see it and `ox skills list` should attribute it to ox?" It is
// deliberately the wider of the two predicates in this file, because being wrong
// about it costs a stray line in `git status`, not a lost file.
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
		strings.HasPrefix(name, TeamPrefix) ||
		isUnprefixedCatalogSkill(name)
}

// IsReclaimableName reports whether a name BY ITSELF is enough for ox to claim a
// skill directory it has no record of ever writing.
//
// This is strictly narrower than IsReservedName, and the gap between them is the
// entire point — do not "simplify" them back into one. IsReservedName answers a
// harmless question (write it, hide it from git, attribute it in a listing). This
// one answers a destructive question: may ox overwrite a file the user wrote, on
// the strength of the name alone, inside a directory it has just gitignored so
// the loss leaves no trace? Handing the destructive answer to every caller that
// only needed the harmless one is how hand-authored work disappears.
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

// isUnprefixedCatalogSkill covers catalog skills that carry none of the prefixes
// above. It widens reserved-ness only — gitignore entries, the migration sweep,
// provenance reporting — and never ownership; IsReclaimableName above is where
// that line is drawn and why.
//
// Curated knowledge skills are named for what they are ("post-cutoff"),
// not for the binary that ships them: "ox-cli-" reads as a CLI relay and, worse,
// survives a --team publish as "sageox-team-ox-cli-post-cutoff".
//
// The prefixes remain the contract for everything that CAN wear one — a prefix
// costs no ignore-file churn when the next skill lands, and an explicit name does.
// This is the narrow exception, and it is derived from the embedded catalog rather
// than hand-listed so the two can never disagree.
//
// CommittedOnRamp is excluded: it is ox-authored but deliberately tracked.
func isUnprefixedCatalogSkill(name string) bool {
	if name == "" || name == CommittedOnRamp {
		return false
	}
	return skills.IsKnown(name)
}
