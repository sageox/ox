package teamdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, teamPath, root, dir, frontmatter string, extra map[string]string) {
	t.Helper()
	skillDir := filepath.Join(teamPath, root, dir)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	body := "---\n" + frontmatter + "---\n\nSkill body.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	for rel, content := range extra {
		p := filepath.Join(skillDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

func names(skills []TeamSkill) []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Name)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// The `repos:` frontmatter key IS the per-repo targeting mechanism, reused
// verbatim from team rules. A skill landing in a repository its author did not
// list is the failure that matters: it puts a team's deploy instructions in front
// of an agent working somewhere they do not apply.

func TestDiscoverSkills_ReposFilterTargetsExactlyTheListedRepos(t *testing.T) {
	team := t.TempDir()
	writeSkill(t, team, "agents/skills", "deploy", "name: deploy\nrepos: [ox, speaker]\n", nil)
	writeSkill(t, team, "agents/skills", "everywhere", "name: everywhere\n", nil)

	got, err := DiscoverSkills(team, "ox")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if !contains(names(got), "deploy") || !contains(names(got), "everywhere") {
		t.Errorf("expected both skills in a listed repo, got %v", names(got))
	}

	got, err = DiscoverSkills(team, "unrelated")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if contains(names(got), "deploy") {
		t.Error("a repos-targeted skill landed in a repository its author never listed")
	}
	if !contains(names(got), "everywhere") {
		t.Errorf("an unfiltered skill was withheld: %v", names(got))
	}
}

// TestDiscoverSkills_UnknownRepoGetsOnlyUnfilteredSkills: when ox cannot tell
// which repo it is in, defaulting to "include" would push targeted skills
// everywhere. Conservative is the only safe direction here.
func TestDiscoverSkills_UnknownRepoGetsOnlyUnfilteredSkills(t *testing.T) {
	team := t.TempDir()
	writeSkill(t, team, "agents/skills", "deploy", "name: deploy\nrepos: [ox]\n", nil)
	writeSkill(t, team, "agents/skills", "everywhere", "name: everywhere\n", nil)

	got, err := DiscoverSkills(team, "")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if contains(names(got), "deploy") {
		t.Error("a targeted skill was included when the repo could not be identified")
	}
	if !contains(names(got), "everywhere") {
		t.Errorf("unfiltered skill missing: %v", names(got))
	}
}

// TestDiscoverSkills_HonorsTheRuleLifecycleKeys — same contract as rules, so a
// draft or superseded skill is not shipped to coworkers mid-authoring.
func TestDiscoverSkills_HonorsTheRuleLifecycleKeys(t *testing.T) {
	team := t.TempDir()
	writeSkill(t, team, "agents/skills", "wip", "name: wip\nstatus: draft\n", nil)
	writeSkill(t, team, "agents/skills", "old", "name: old\nstatus: superseded-by:new\n", nil)
	writeSkill(t, team, "agents/skills", "hidden", "name: hidden\nvisibility: hidden\n", nil)
	writeSkill(t, team, "agents/skills", "humans", "name: humans\naudience: human\n", nil)
	writeSkill(t, team, "agents/skills", "live", "name: live\n", nil)

	got, err := DiscoverSkills(team, "ox")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if len(got) != 1 || got[0].Name != "live" {
		t.Errorf("lifecycle filtering wrong; want only [live], got %v", names(got))
	}
}

// TestDiscoverSkills_CanonicalRootWinsOverLegacy mirrors DiscoverRules: a team
// mid-migration must not get two copies or the stale one.
func TestDiscoverSkills_CanonicalRootWinsOverLegacy(t *testing.T) {
	team := t.TempDir()
	writeSkill(t, team, "coworkers/skills", "deploy", "name: deploy\ndescription: legacy\n", nil)
	writeSkill(t, team, "agents/skills", "deploy", "name: deploy\ndescription: canonical\n", nil)

	got, err := DiscoverSkills(team, "ox")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one deduped skill, got %v", names(got))
	}
	if got[0].Description != "canonical" {
		t.Errorf("legacy root won the collision: %q", got[0].Description)
	}
}

// TestDiscoverSkills_SameRootNameCollisionsAreRejected prevents first-entry
// shadowing and case/normalization flip-flops. Two directories in one root have
// no legitimate precedence, so neither may reach installation.
func TestDiscoverSkills_SameRootNameCollisionsAreRejected(t *testing.T) {
	tests := []struct {
		name   string
		first  string
		second string
	}{
		{name: "exact declared name", first: "deploy", second: "deploy"},
		{name: "case variant", first: "Deploy", second: "deploy"},
		{name: "unicode normalization variant", first: "caf\u00e9", second: "cafe\u0301"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			team := t.TempDir()
			writeSkill(t, team, "agents/skills", "aaa-shadow", "name: "+tt.first+"\n", nil)
			writeSkill(t, team, "agents/skills", "deploy", "name: "+tt.second+"\n", nil)

			for pass := 0; pass < 3; pass++ {
				installable, rejected, err := DiscoverSkillsWithRejections(team, "ox")
				if err != nil {
					t.Fatalf("pass %d: DiscoverSkillsWithRejections: %v", pass, err)
				}
				if len(installable) != 0 {
					t.Fatalf("pass %d: a colliding identity reached installation: %v", pass, names(installable))
				}
				if len(rejected) != 1 {
					t.Fatalf("pass %d: collision was not reported deterministically: %+v", pass, rejected)
				}
				reason := rejected[0].NameError
				for _, want := range []string{"collision", "aaa-shadow", "deploy", "rename"} {
					if !strings.Contains(reason, want) {
						t.Errorf("pass %d: collision reason %q does not contain %q", pass, reason, want)
					}
				}
			}
		})
	}
}

func TestDiscoverSkills_NonPublishedDuplicateDoesNotBlockLiveSkill(t *testing.T) {
	team := t.TempDir()
	writeSkill(t, team, "agents/skills", "aaa-draft", "name: deploy\nstatus: draft\n", nil)
	writeSkill(t, team, "agents/skills", "deploy", "name: deploy\n", nil)

	installable, rejected, err := DiscoverSkillsWithRejections(team, "ox")
	if err != nil {
		t.Fatalf("DiscoverSkillsWithRejections: %v", err)
	}
	if len(rejected) != 0 {
		t.Fatalf("a draft entry manufactured a collision: %+v", rejected)
	}
	if len(installable) != 1 || installable[0].Name != "deploy" {
		t.Fatalf("a draft entry suppressed the live skill: %v", names(installable))
	}
}

// TestDiscoverSkills_CollectsTheSkillsOwnFilesForClassification: the trust model
// classifies on content, so discovery must report bundled scripts.
func TestDiscoverSkills_CollectsTheSkillsOwnFilesForClassification(t *testing.T) {
	team := t.TempDir()
	writeSkill(t, team, "agents/skills", "deploy", "name: deploy\n", map[string]string{
		"scripts/run.sh":    "#!/bin/sh\n",
		"references/doc.md": "notes\n",
	})

	got, err := DiscoverSkills(team, "ox")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one skill, got %v", names(got))
	}
	if !contains(got[0].Files, "scripts/run.sh") {
		t.Errorf("bundled script not reported; the trust model would classify it as prose: %v", got[0].Files)
	}
	if !contains(got[0].Files, "SKILL.md") {
		t.Errorf("manifest missing from the file list: %v", got[0].Files)
	}
}

// TestDiscoverSkills_ADirectoryWithoutAManifestIsNotASkill: assets/ and
// references/ live beside skills; treating one as a skill would materialize a
// nameless directory.
func TestDiscoverSkills_ADirectoryWithoutAManifestIsNotASkill(t *testing.T) {
	team := t.TempDir()
	if err := os.MkdirAll(filepath.Join(team, "agents/skills", "not-a-skill"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSkill(t, team, "agents/skills", "real", "name: real\n", nil)

	got, err := DiscoverSkills(team, "ox")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if len(got) != 1 || got[0].Name != "real" {
		t.Errorf("a manifest-less directory was treated as a skill: %v", names(got))
	}
}

// TestDiscoverSkills_MissingRootIsNotAnError: this runs against paths that may
// not be checked out at all.
func TestDiscoverSkills_MissingRootIsNotAnError(t *testing.T) {
	got, err := DiscoverSkills(t.TempDir(), "ox")
	if err != nil {
		t.Fatalf("a team with no skills errored: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected nothing, got %v", names(got))
	}
	if got, err := DiscoverSkills("", "ox"); err != nil || got != nil {
		t.Errorf("empty team path: got %v, %v", got, err)
	}
}

// TestDiscoverSkills_SymlinkedManifestIsSkipped: the team checkout is mutable
// under the daemon. Following a link out of it would materialize content from an
// arbitrary path on the machine.
func TestDiscoverSkills_SymlinkedManifestIsSkipped(t *testing.T) {
	team := t.TempDir()
	outside := filepath.Join(t.TempDir(), "evil.md")
	if err := os.WriteFile(outside, []byte("---\nname: evil\n---\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	dir := filepath.Join(team, "agents/skills", "linked")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := DiscoverSkills(team, "ox")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a symlinked manifest was discovered: %v", names(got))
	}
}

// A team skill's NAME becomes a DIRECTORY NAME in the customer's repository, and
// it is attacker-controlled: free text in `name:` frontmatter, from a repo any
// teammate can push to, over a pull path that verifies no signature. These pin
// that discovery is where an unusable name stops — upstream of everything that
// installs OR offers to approve, so the two can never disagree about which
// skills exist.

func TestDiscoverSkills_NameThatWouldEscapeTheSkillsRootIsNotInstallable(t *testing.T) {
	traversals := []string{
		"../../../../.claude",
		`..\..\..\..\.claude`,
		"/etc/passwd",
		"..",
		".",
		"sageox-team-..",
	}
	for _, bad := range traversals {
		t.Run(bad, func(t *testing.T) {
			team := t.TempDir()
			writeSkill(t, team, "agents/skills", "onboarding", "name: "+bad+"\n", nil)
			writeSkill(t, team, "agents/skills", "deploy", "name: deploy\n", nil)

			got, err := DiscoverSkills(team, "ox")
			if err != nil {
				t.Fatalf("one unusable name failed discovery for every skill: %v", err)
			}
			if contains(names(got), bad) {
				t.Errorf("discovery offered %q for installation; it becomes a path and walks out of the skills root", bad)
			}
			if !contains(names(got), "deploy") {
				t.Errorf("one rejected skill took the team's other skills down with it: got %v", names(got))
			}
		})
	}
}

func TestDiscoverSkills_UppercaseNameIsNotInstallable(t *testing.T) {
	// Not a traversal, but a case-insensitive filesystem resolves `Deploy` and
	// `deploy` to ONE directory while the reserved-prefix ignore globs are
	// case-sensitive — the same collision manager.go's caseVariantDirOnDisk
	// already guards. One canonical case is the only way both can be right.
	team := t.TempDir()
	writeSkill(t, team, "agents/skills", "deploy", "name: Deploy\n", nil)

	got, err := DiscoverSkills(team, "ox")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if contains(names(got), "Deploy") {
		t.Error("an uppercase skill name was offered for installation")
	}
}

// TestPublishedSkills_ReportsARejectedNameRatherThanHidingIt.
//
// The rejection must stay VISIBLE. A team that publishes `Deploy` today would
// otherwise watch it silently vanish on upgrade, with no discoverable cause —
// which is a worse failure than the one being fixed, because nothing anywhere
// says the skill was seen at all.
func TestPublishedSkills_ReportsARejectedNameRatherThanHidingIt(t *testing.T) {
	team := t.TempDir()
	writeSkill(t, team, "agents/skills", "deploy", "name: Deploy\n", nil)

	published, err := PublishedSkills(team)
	if err != nil {
		t.Fatalf("PublishedSkills: %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("a rejected skill vanished from the report the human reads: %v", names(published))
	}
	if published[0].NameError == "" {
		t.Fatal("the skill was reported with no reason, so `ox skills status` cannot say why it is missing")
	}
	if !strings.Contains(published[0].NameError, "rename") {
		t.Errorf("the reason names no remedy: %q", published[0].NameError)
	}
}

// TestPublishedSkills_RejectedNamesNeverReachTheInstallPath is the layering
// assertion: reporting sees everything, installation sees only the safe set.
func TestPublishedSkills_RejectedNamesNeverReachTheInstallPath(t *testing.T) {
	team := t.TempDir()
	writeSkill(t, team, "agents/skills", "onboarding", "name: ../../../../.claude\n", nil)

	published, err := PublishedSkills(team)
	if err != nil {
		t.Fatalf("PublishedSkills: %v", err)
	}
	for _, s := range published {
		if s.NameError == "" {
			t.Errorf("skill %q was published as usable", s.Name)
		}
	}

	installable, err := DiscoverSkills(team, "ox")
	if err != nil {
		t.Fatalf("DiscoverSkills: %v", err)
	}
	if len(installable) != 0 {
		t.Fatalf("a rejected skill reached the install path: %v", names(installable))
	}
}

// TestPublishedSkills_RejectedNameIsSafeToPrint. The rejected name is the one
// attacker-controlled string that still reaches a terminal, so it is the one
// place an ANSI escape could hide the rest of a diagnostic.
func TestPublishedSkills_RejectedNameIsSafeToPrint(t *testing.T) {
	team := t.TempDir()
	writeSkill(t, team, "agents/skills", "onboarding", "name: ev\x1b[2Kil\n", nil)

	published, err := PublishedSkills(team)
	if err != nil {
		t.Fatalf("PublishedSkills: %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("want the rejected skill reported, got %v", names(published))
	}
	if strings.ContainsRune(published[0].Name, '\x1b') {
		t.Errorf("a terminal escape reached the reported name: %q", published[0].Name)
	}
}

// TestValidTeamSkillName is the canonical rule, pinned input by input.
//
// Rejecting rather than sanitizing is the deliberate half: sanitizing
// `../../deploy` and `..\..\deploy` into one `deploy` would collide two skills
// into one directory, which is a worse bug than the traversal it fixed.
func TestValidTeamSkillName(t *testing.T) {
	tests := []struct {
		why  string
		name string
		want bool
	}{
		{"ordinary name", "deploy", true},
		{"hyphenated", "deploy-to-prod", true},
		{"dotted", "deploy.v2", true},
		{"underscored", "deploy_v2", true},
		{"leading digit", "0auth", true},
		{"already prefixed", "sageox-team-deploy", true},
		{"64 byte boundary", strings.Repeat("a", 64), true},

		{"empty", "", false},
		{"parent", "..", false},
		{"current", ".", false},
		{"posix traversal", "../../../../.claude", false},
		{"windows traversal", `..\..\..\..\.claude`, false},
		{"absolute path", "/etc/passwd", false},
		{"traversal behind the reserved prefix", "sageox-team-..", false},
		{"uppercase", "Deploy", false},
		{"forward slash", "a/b", false},
		{"backslash", `a\b`, false},
		{"NUL byte", "deploy\x00", false},
		{"newline smuggles a second line past an anchor", "deploy\n../../../.claude", false},
		{"leading dot hides it from a directory listing", ".hidden", false},
		{"trailing dot is ambiguous on Windows", "deploy.", false},
		{"65 bytes", strings.Repeat("a", 65), false},
		{"multi-byte is not lowercase ASCII", "déploy", false},
		{"leading hyphen parses as a flag downstream", "-rf", false},
		{"space", "my skill", false},
		{"terminal escape", "ev\x1b[2Kil", false},
	}

	for _, tt := range tests {
		t.Run(tt.why, func(t *testing.T) {
			if got := ValidTeamSkillName(tt.name); got != tt.want {
				t.Errorf("ValidTeamSkillName(%q) = %v, want %v", tt.name, got, tt.want)
			}
			// Every rejection must come with a reason a human can act on;
			// "invalid" with no remedy is a support ticket.
			reason := rejectTeamSkillName(tt.name)
			switch {
			case tt.want && reason != "":
				t.Errorf("a valid name was given a rejection reason: %q", reason)
			case !tt.want && !strings.Contains(reason, "rename"):
				t.Errorf("rejection reason names no remedy: %q", reason)
			}
		})
	}
}
