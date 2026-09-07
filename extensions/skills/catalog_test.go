package skills

import (
	"io/fs"
	"path"
	"strings"
	"testing"
	"testing/fstest"
)

func TestCanonicalCatalogValidAndDeterministic(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	first, err := SelectedBundles("1.0.0", nil, []string{"core", "attest"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := SelectedBundles("1.0.0", nil, []string{"core", "attest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("non-deterministic or empty catalog: %d / %d", len(first), len(second))
	}
	for i, skill := range first {
		if skill.Name != second[i].Name || len(skill.Files) == 0 {
			t.Fatalf("selection drift at %d: %#v / %#v", i, skill, second[i])
		}
		if !strings.HasPrefix(string(skill.Content), "---\nname: "+skill.Name+"\n") {
			t.Fatalf("%s has non-portable frontmatter", skill.Name)
		}
	}

	defaults, err := Selected("1.0.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range defaults {
		if strings.HasPrefix(skill.Name, "ox-attest-") {
			t.Fatalf("opt-in skill %s leaked into default bundle", skill.Name)
		}
	}
}

func TestValidateRejectsUnsafeAndMalformedSources(t *testing.T) {
	valid := "---\nname: safe\ndescription: safe\n---\nbody\n"
	tests := []struct {
		name    string
		files   fstest.MapFS
		catalog []Bundle
		want    string
	}{
		{"missing skill", fstest.MapFS{}, []Bundle{{ID: "core", Description: "x", SkillIDs: []string{"safe"}}}, "missing canonical skill"},
		{"name mismatch", fstest.MapFS{"safe/SKILL.md": {Data: []byte(strings.Replace(valid, "name: safe", "name: other", 1))}}, []Bundle{{ID: "core", Description: "x", SkillIDs: []string{"safe"}}}, "frontmatter name"},
		{"unsupported root file", fstest.MapFS{"safe/SKILL.md": {Data: []byte(valid)}, "safe/escape.txt": {Data: []byte("x")}}, []Bundle{{ID: "core", Description: "x", SkillIDs: []string{"safe"}}}, "unsupported file"},
		{"undeclared directory", fstest.MapFS{"safe/SKILL.md": {Data: []byte(valid)}}, []Bundle{{ID: "core", Description: "x", SkillIDs: []string{"other"}}}, "not in a bundle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSource(tt.files, tt.catalog)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateSource() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPortableSkillsAvoidHostSpecificActivationSyntax(t *testing.T) {
	selected, err := SelectedBundles("1.0.0", nil, []string{"core", "attest"})
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{
		"runs /ox-",
		"`/ox-",
		"(/ox-",
		"Claude-specific auto-activation",
		"Skills are agent-specific wrappers",
	}
	for _, skill := range selected {
		body := string(skill.Content)
		for _, phrase := range banned {
			if strings.Contains(body, phrase) {
				t.Errorf("portable skill %s contains host-specific activation phrase %q", skill.Name, phrase)
			}
		}
	}
}

// TestSlashOnlySkillsCarryAnAgentNeutralGuard closes a gap that depends on
// UNVERIFIED vendor behavior.
//
// `disable-model-invocation: true` is a Claude Code frontmatter key. Codex,
// Gemini, and OMP all read the SAME .agents/skills projection, and whether any of
// them honors that key is unknown. If they do not, every lifecycle and diagnostic
// skill — ox-cli-session-stop, ox-cli-session-abort, ox-cli-doctor, ox-cli-init —
// enters their context with a description and becomes model-invocable, so
// ADR-023's ruling that lifecycle surfaces stay EXPLICIT would silently fail on
// exactly the agents the command fold was meant to help.
//
// The body-level guard does not depend on any vendor honoring anything: it is
// instruction text every agent reads. Belt and braces, deliberately.
func TestSlashOnlySkillsCarryAnAgentNeutralGuard(t *testing.T) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatalf("read skills: %v", err)
	}
	var checked int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		body, err := fs.ReadFile(FS, path.Join(e.Name(), SkillFileName))
		if err != nil {
			continue
		}
		if !strings.Contains(string(body), "disable-model-invocation: true") {
			continue
		}
		checked++
		if !strings.Contains(string(body), "Explicit invocation only") {
			t.Errorf("%s declares disable-model-invocation but carries no agent-neutral guard; "+
				"on an agent that ignores the frontmatter key the model could fire it unprompted", e.Name())
		}
	}
	if checked == 0 {
		t.Fatal("no slash-only skills found; the fold or the frontmatter key regressed")
	}
}
