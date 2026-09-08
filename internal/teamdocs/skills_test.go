package teamdocs

import (
	"os"
	"path/filepath"
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
