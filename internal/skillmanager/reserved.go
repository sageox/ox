package skillmanager

import "strings"

// Reserved namespaces. These strings are the whole ownership contract, so they
// live in one place: the installer, the ignore-file writer, the doctor checks, the
// Team Rule projector, and the migration all have to agree on them exactly, and a
// copy that drifts by one character would either strand files in git or sweep
// files ox does not own.
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

	// TeamSuffix marks content projected out of a team context — every Team Skill
	// directory and every Team Rule file, with no per-artifact exception.
	//
	// It is a SUFFIX because an agent's slash name derives from the DIRECTORY, not
	// from the skill's own frontmatter `name:`. Under the old prefix a team skill
	// named `fork-scout` installed as `sageox-team-fork-scout`, so a teammate had to
	// type `/sageox-team-fork-scout` while the skill's own description — copied
	// verbatim from the team's file, which ox does not rewrite — still told them to
	// type `/fork-scout`. Every team skill's documentation was wrong the moment it
	// synced. A suffix puts the real name first and keeps the namespace.
	//
	// It stays a fixed string rather than a per-skill name so the gitignore entries
	// remain stable globs (`skills/*-team/`, `rules/*-team.md`). A per-name ignore
	// list would churn a COMMITTED file every time the team added a skill, which is
	// the pull-request noise ADR-031 exists to remove.
	TeamSuffix = "-team"

	// LegacyTeamPrefix is the namespace team content used before the suffix.
	//
	// It survives for exactly one purpose: sweeping directories and files ox wrote
	// under the old scheme. Nothing materializes under it any more. Unlike
	// TeamSuffix it is still RECLAIMABLE by name — ox declared that namespace and
	// told people to stay out of it, so a directory wearing it is ox's by contract.
	LegacyTeamPrefix = "sageox-team-"

	// CommittedOnRamp is the ONE surface that stays committed to the repository.
	// It is unprefixed on purpose so it can never match a reserved glob — it must
	// survive in git, being the only SageOx artifact present on a machine where
	// the CLI is not installed.
	CommittedOnRamp = "sageox"
)

// ruleProjectionExtensions are the file extensions a Team Rule projection can
// carry. They are stripped before a name is classified so `security-9f2a-team.mdc`
// (Cursor) is recognized exactly like `security-9f2a-team.md` (everyone else).
//
// The list is explicit rather than filepath.Ext because a skill DIRECTORY name may
// legitimately contain a dot (TeamSkillNamePattern admits `.`), and Ext would
// happily amputate `foo.bar-team` down to `foo`.
var ruleProjectionExtensions = []string{".md", ".mdc"}

// reservedBaseName strips a rule projection's extension, leaving the name the
// reserved-namespace tests actually classify.
func reservedBaseName(name string) string {
	for _, ext := range ruleProjectionExtensions {
		if strings.HasSuffix(name, ext) {
			return strings.TrimSuffix(name, ext)
		}
	}
	return name
}

// IsTeamProjectionName reports whether a skill directory or rule file basename
// belongs to a Team Context projection, under either the current suffix or the
// legacy prefix.
//
// It answers the CLASSIFICATION question — "is this row team content?" — for
// reporting, gitignoring, and the retirement sweep. It does NOT authorize
// overwriting anything; see IsReclaimableName.
func IsTeamProjectionName(name string) bool {
	name = reservedBaseName(name)
	return strings.HasSuffix(name, TeamSuffix) || strings.HasPrefix(name, LegacyTeamPrefix)
}

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
	base := reservedBaseName(name)
	return base == CLIBase ||
		strings.HasPrefix(base, CLIPrefix) ||
		IsTeamProjectionName(name)
}

// IsReclaimableName reports whether a name BY ITSELF is enough for ox to claim a
// skill directory it has no record of ever writing.
//
// It intentionally remains a separate predicate from IsReservedName, and since the
// team suffix landed their namespace sets genuinely differ: this one authorizes
// destructive overwrite, while the other classifies a path.
//
// Only namespaces ox DECLARED and told people to stay out of qualify. "ox-cli-"
// and the legacy "sageox-team-" are such namespaces, so a directory wearing one is
// ox's by contract even with no lockfile entry — the 0.15.0 inversion, reasoned out
// at the first-install reclaim in Plan.
//
// TeamSuffix deliberately does NOT qualify. "-team" is ordinary English: a
// repository can already hold a hand-authored `notify-team` or `onboard-team`
// skill, written by someone who was never told the name was spoken for. Those
// directories are gitignored and absent from the lockfile, so destroying one would
// be unrecoverable. Team projections earn ownership the honest way instead — a
// verified in-band projection stamp, a recorded lockfile digest, or a recovery
// journal entry — and a same-name stranger is reported as a conflict and left
// alone.
//
// An unprefixed catalog name never qualifies either, however certainly ox ships
// it. "post-cutoff" is ordinary English naming what the skill IS, not who authored
// it.
//
// CommittedOnRamp is excluded for the same reason it is excluded above: ox
// authors it, but it is tracked rather than owned-and-hidden.
func IsReclaimableName(name string) bool {
	base := reservedBaseName(name)
	return base == CLIBase ||
		strings.HasPrefix(base, CLIPrefix) ||
		strings.HasPrefix(base, LegacyTeamPrefix)
}
