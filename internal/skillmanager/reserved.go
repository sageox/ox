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

// IsReservedName reports whether a skill/rule/command basename belongs to ox.
//
// Reserved means ox writes it, ox overwrites it, and git never sees it. Anything
// else in those directories belongs to the user and is never touched — including
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
