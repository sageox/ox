package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// The team-context layout ox documents (`ox guide team-context`, the installed
// use-team-context rule, both adapters' rule text) names agents/profiles/ and
// agents/commands/ canonical and coworkers/ legacy. Only agents/rules/ was ever
// wired up, so a coworker who filed a profile or command exactly where the docs
// said to put it got silence: nothing listed it at prime, and nothing could load
// it. These tests pin the documented layout to the code.

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDiscoverAgents_ReadsCanonicalProfilesDir(t *testing.T) {
	team := t.TempDir()
	writeFile(t, filepath.Join(team, "agents", "profiles", "postgres-pro.md"),
		"---\ndescription: Postgres expert\nmodel: opus\n---\n\nbody\n")

	agents, err := DiscoverAgents(team)
	if err != nil {
		t.Fatalf("DiscoverAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 profile from agents/profiles/, got %d", len(agents))
	}
	if agents[0].Name != "postgres-pro" {
		t.Errorf("name = %q, want postgres-pro", agents[0].Name)
	}
	if agents[0].Description != "Postgres expert" {
		t.Errorf("description = %q, want Postgres expert", agents[0].Description)
	}
}

func TestDiscoverAgents_CanonicalWinsOverLegacy(t *testing.T) {
	team := t.TempDir()
	writeFile(t, filepath.Join(team, "agents", "profiles", "reviewer.md"),
		"---\ndescription: canonical copy\n---\n")
	writeFile(t, filepath.Join(team, "coworkers", "agents", "reviewer.md"),
		"---\ndescription: legacy copy\n---\n")
	writeFile(t, filepath.Join(team, "coworkers", "agents", "legacy-only.md"),
		"---\ndescription: still discoverable\n---\n")

	agents, err := DiscoverAgents(team)
	if err != nil {
		t.Fatalf("DiscoverAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected reviewer (deduped) + legacy-only, got %d", len(agents))
	}
	byName := map[string]Agent{}
	for _, a := range agents {
		byName[a.Name] = a
	}
	if got := byName["reviewer"].Description; got != "canonical copy" {
		t.Errorf("canonical root must win: description = %q", got)
	}
	if _, ok := byName["legacy-only"]; !ok {
		t.Error("legacy root must still be read for backward compat")
	}
}

func TestLoadAgent_ReadsCanonicalProfilesDir(t *testing.T) {
	team := t.TempDir()
	writeFile(t, filepath.Join(team, "agents", "profiles", "sre.md"),
		"---\ndescription: On-call expert\n---\n\nfull body here\n")

	// discovery and load must agree: a profile prime lists but `ox coworker load`
	// cannot open is worse than one that was never listed.
	got, err := LoadAgent(team, "sre")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got == nil {
		t.Fatal("LoadAgent returned nil for a profile in the canonical root")
	}
	if got.Description != "On-call expert" {
		t.Errorf("description = %q", got.Description)
	}
}

func TestLoadAgent_CanonicalNonFileDoesNotShadowLegacyProfile(t *testing.T) {
	team := t.TempDir()
	if err := os.MkdirAll(filepath.Join(team, "agents", "profiles", "sre.md"), 0o755); err != nil {
		t.Fatalf("create canonical directory collision: %v", err)
	}
	writeFile(t, filepath.Join(team, "coworkers", "agents", "sre.md"),
		"---\ndescription: Legacy on-call expert\n---\n\nfull body here\n")

	got, err := LoadAgent(team, "sre")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got == nil || got.Description != "Legacy on-call expert" {
		t.Fatalf("non-file canonical collision shadowed valid legacy profile: %+v", got)
	}
}

func TestDiscoverTeamCommands_ReadsCanonicalCommandsDir(t *testing.T) {
	team := t.TempDir()
	writeFile(t, filepath.Join(team, "agents", "commands", "deploy.md"),
		"---\nname: deploy\ndescription: Ship to production\n---\n")

	commands, err := DiscoverTeamCommands(team)
	if err != nil {
		t.Fatalf("DiscoverTeamCommands: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command from agents/commands/, got %d", len(commands))
	}
	if commands[0].Description != "Ship to production" {
		t.Errorf("description = %q", commands[0].Description)
	}
	// the catalog is only useful if the agent can open the file
	if commands[0].Path == "" {
		t.Error("command must carry a path the agent can read")
	}
}

func TestDiscoverTeamCommands_CanonicalWinsOverLegacy(t *testing.T) {
	team := t.TempDir()
	writeFile(t, filepath.Join(team, "agents", "commands", "review.md"),
		"---\nname: review\ndescription: canonical copy\n---\n")
	writeFile(t, filepath.Join(team, "coworkers", "commands", "review.md"),
		"---\nname: review\ndescription: legacy copy\n---\n")

	commands, err := DiscoverTeamCommands(team)
	if err != nil {
		t.Fatalf("DiscoverTeamCommands: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("expected the duplicate to be deduped, got %d", len(commands))
	}
	if commands[0].Description != "canonical copy" {
		t.Errorf("canonical root must win: description = %q", commands[0].Description)
	}
}

func TestDiscoverAll_ProfileIndexAndAgentsMDFromCanonicalRoot(t *testing.T) {
	team := t.TempDir()
	writeFile(t, filepath.Join(team, "agents", "profiles", "AGENTS.md"), "profile-level instructions\n")
	writeFile(t, filepath.Join(team, "agents", "profiles", "index.md"), "# Catalog\n")
	writeFile(t, filepath.Join(team, "agents", "profiles", "dba.md"), "---\ndescription: DBA\n---\n")

	tc, err := DiscoverAll(team)
	if err != nil {
		t.Fatalf("DiscoverAll: %v", err)
	}
	if tc == nil {
		t.Fatal("DiscoverAll returned nil")
	}
	if !tc.HasAgentsAgentsMD {
		t.Error("profile-level AGENTS.md in the canonical root should be found")
	}
	if !tc.HasAgentsIndex {
		t.Error("profile index.md in the canonical root should be found")
	}
	if len(tc.Agents) != 1 {
		t.Errorf("expected 1 discovered profile, got %d", len(tc.Agents))
	}
}
