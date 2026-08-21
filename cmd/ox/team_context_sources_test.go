package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTeamFile creates rel under root with content, making parents as needed.
func writeTeamFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// doc renders a team doc with the frontmatter DiscoverDocs reads.
func doc(title string) string {
	return "---\ntitle: " + title + "\nvisibility: indexed\n---\n\nbody\n"
}

// rule renders a team rule with the frontmatter DiscoverRules reads.
func rule(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description +
		"\nvisibility: indexed\n---\n\nbody\n"
}

// twoSources returns a checkout and a mount, wired as read sources.
func twoSources(t *testing.T) (sources teamReadSources, checkout, mount string) {
	t.Helper()
	checkout, mount = t.TempDir(), t.TempDir()
	return teamReadSources{checkout: checkout, mount: mount}, checkout, mount
}

// The whole point of the change: the drive adds, it does not displace. A
// document only the checkout has still ships.
func TestTeamDocsUnionKeepsWhatOnlyTheCheckoutHas(t *testing.T) {
	sources, checkout, mount := twoSources(t)
	writeTeamFile(t, checkout, "docs/principles.md", doc("Principles"))
	writeTeamFile(t, mount, "docs/onboarding.md", doc("Onboarding"))

	docs, fromMount := discoverTeamDocsAcross(sources)

	names := map[string]bool{}
	for _, d := range docs {
		names[d.Name] = true
	}
	if !names["principles.md"] || !names["onboarding.md"] {
		t.Fatalf("merged docs = %v, want both sources represented", names)
	}
	if fromMount != 1 {
		t.Errorf("mount contribution = %d, want 1", fromMount)
	}
}

// Secondary means secondary: where both carry the same document, the proven
// copy is the one that ships. Flipping this is the promotion decision, and it
// should take a code change to make.
func TestTeamDocsConflictResolvesToTheCheckout(t *testing.T) {
	sources, checkout, mount := twoSources(t)
	writeTeamFile(t, checkout, "docs/principles.md", doc("From the checkout"))
	writeTeamFile(t, mount, "docs/principles.md", doc("From the drive"))

	docs, fromMount := discoverTeamDocsAcross(sources)

	if len(docs) != 1 {
		t.Fatalf("got %d docs, want the duplicate collapsed to one", len(docs))
	}
	if docs[0].Title != "From the checkout" {
		t.Errorf("title = %q, want the checkout's copy to win", docs[0].Title)
	}
	if fromMount != 0 {
		t.Errorf("mount contribution = %d, want 0 — it only echoed the checkout", fromMount)
	}
}

func TestTeamRulesUnionAcrossSources(t *testing.T) {
	sources, checkout, mount := twoSources(t)
	writeTeamFile(t, checkout, "agents/rules/postgres.md", rule("postgres", "from the checkout"))
	writeTeamFile(t, mount, "agents/rules/postgres.md", rule("postgres", "from the drive"))
	writeTeamFile(t, mount, "agents/rules/react.md", rule("react", "only on the drive"))

	rules, fromMount := discoverTeamRulesAcross(sources, "any-repo")

	byName := map[string]string{}
	for _, r := range rules {
		byName[r.Name] = r.Description
	}
	if byName["postgres"] != "from the checkout" {
		t.Errorf("postgres description = %q, want the checkout's copy", byName["postgres"])
	}
	if byName["react"] != "only on the drive" {
		t.Errorf("react rule = %q, want the drive's contribution kept", byName["react"])
	}
	if fromMount != 1 {
		t.Errorf("mount contribution = %d, want 1", fromMount)
	}
}

func TestCustomizationsUnionAgentsAcrossSources(t *testing.T) {
	sources, checkout, mount := twoSources(t)
	writeTeamFile(t, checkout, "coworkers/agents/dba.md",
		"---\nname: dba\ndescription: checkout copy\n---\n")
	writeTeamFile(t, mount, "coworkers/agents/dba.md",
		"---\nname: dba\ndescription: drive copy\n---\n")
	writeTeamFile(t, mount, "coworkers/agents/sre.md",
		"---\nname: sre\ndescription: only on the drive\n---\n")

	merged, addedAgents, _ := discoverCustomizationsAcross(sources)

	byName := map[string]string{}
	for _, a := range merged.Agents {
		byName[a.Name] = a.Description
	}
	if byName["dba"] != "checkout copy" {
		t.Errorf("dba description = %q, want the checkout's copy", byName["dba"])
	}
	if _, ok := byName["sre"]; !ok {
		t.Error("an agent only the drive carried was dropped")
	}
	if addedAgents != 1 {
		t.Errorf("mount agents = %d, want 1", addedAgents)
	}
}

// A scalar the checkout is missing is exactly the gap the drive is here to
// fill — the case a machine that never cloned lives in permanently.
func TestCustomizationsTakeInstructionFilesFromTheDriveWhenTheCheckoutLacksThem(t *testing.T) {
	sources, _, mount := twoSources(t)
	writeTeamFile(t, mount, "coworkers/CLAUDE.md", "team preamble\n")

	merged, _, _ := discoverCustomizationsAcross(sources)

	if !merged.HasClaudeMD {
		t.Fatal("the drive's CLAUDE.md was not picked up")
	}
	if got, want := merged.ClaudeMDPath, filepath.Join(mount, "coworkers", "CLAUDE.md"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestMemoryContentPrefersTheCheckout(t *testing.T) {
	_, checkout, mount := twoSources(t)
	writeTeamFile(t, checkout, "MEMORY.md", "checkout memory\n")
	writeTeamFile(t, mount, "MEMORY.md", "drive memory\n")

	info := &teamContextInfo{}
	loadTeamMemory(info, checkout)
	fillTeamMemoryGapsFromMount(info, mount)

	if got := info.MemoryContent; got != "checkout memory" {
		t.Errorf("memory = %q, want the checkout's copy", got)
	}
}

// A day of team history the checkout is missing is history that would otherwise
// be silently absent, so timelines union rather than pick a winner.
func TestMemoryTimelinesUnionAndStayReverseChronological(t *testing.T) {
	_, checkout, mount := twoSources(t)
	writeTeamFile(t, checkout, "memory/daily/2026-08-01.md", "older\n")
	writeTeamFile(t, checkout, "memory/daily/2026-08-03.md", "newer\n")
	writeTeamFile(t, mount, "memory/daily/2026-08-02.md", "only on the drive\n")

	info := &teamContextInfo{}
	loadTeamMemory(info, checkout)
	added := fillTeamMemoryGapsFromMount(info, mount)

	want := []string{"2026-08-03.md", "2026-08-02.md", "2026-08-01.md"}
	if len(info.MemoryDaily) != len(want) {
		t.Fatalf("daily = %v, want %v", info.MemoryDaily, want)
	}
	for i := range want {
		if info.MemoryDaily[i] != want[i] {
			t.Fatalf("daily = %v, want %v", info.MemoryDaily, want)
		}
	}
	if added != 1 {
		t.Errorf("mount contribution = %d, want 1", added)
	}
}

// The fallback CodeRabbit asked for, obtained structurally rather than by
// retrying: the checkout is read on every prime, so a mount that is absent,
// unreadable, or half-hydrated subtracts nothing. There is no partial-context
// state to fall back FROM.
func TestAnUnreadableMountCostsTheCheckoutNothing(t *testing.T) {
	sources, checkout, _ := twoSources(t)
	writeTeamFile(t, checkout, "docs/principles.md", doc("Principles"))
	writeTeamFile(t, checkout, "agents/rules/postgres.md", rule("postgres", "checkout rule"))
	writeTeamFile(t, checkout, "MEMORY.md", "checkout memory\n")
	sources.mount = filepath.Join(t.TempDir(), "never-hydrated")

	docs, _ := discoverTeamDocsAcross(sources)
	rules, _ := discoverTeamRulesAcross(sources, "any-repo")
	info := &teamContextInfo{}
	loadTeamMemory(info, checkout)
	fillTeamMemoryGapsFromMount(info, sources.mount)

	if len(docs) != 1 || len(rules) != 1 || info.MemoryContent == "" {
		t.Fatalf("an unreachable mount degraded the checkout: %d docs, %d rules, memory=%q",
			len(docs), len(rules), info.MemoryContent)
	}
	if !sources.anyExists() {
		t.Error("the team read as unavailable while its checkout was present")
	}
}

// The mirror of the case above: a machine that never cloned still has context
// when a drive carries the team.
func TestTeamIsAvailableWhenOnlyTheMountExists(t *testing.T) {
	sources := teamReadSources{
		checkout: filepath.Join(t.TempDir(), "never-cloned"),
		mount:    t.TempDir(),
	}
	writeTeamFile(t, sources.mount, "docs/onboarding.md", doc("Onboarding"))

	if !sources.anyExists() {
		t.Fatal("a mounted team read as unavailable because the checkout was missing")
	}
	if docs, fromMount := discoverTeamDocsAcross(sources); len(docs) != 1 || fromMount != 1 {
		t.Errorf("got %d docs (%d from the mount), want 1 from the mount", len(docs), fromMount)
	}
}

// Reading order IS the precedence rule, so it is worth pinning directly.
func TestRootsPutTheCheckoutFirst(t *testing.T) {
	sources := teamReadSources{checkout: "/checkout", mount: "/mount"}
	if got := sources.roots(); len(got) != 2 || got[0] != "/checkout" || got[1] != "/mount" {
		t.Fatalf("roots = %v, want the checkout first", got)
	}

	none := teamReadSources{checkout: "/checkout"}
	if got := none.roots(); len(got) != 1 || got[0] != "/checkout" {
		t.Fatalf("roots = %v, want the checkout alone", got)
	}
}

func TestFirstRootWithPrefersTheCheckout(t *testing.T) {
	sources, checkout, mount := twoSources(t)
	writeTeamFile(t, checkout, "capabilities/team/index.md", "roster\n")
	writeTeamFile(t, mount, "capabilities/team/index.md", "roster\n")

	root, ok := firstRootWith(sources, filepath.Join("capabilities", "team", "index.md"))
	if !ok || root != checkout {
		t.Fatalf("root = %q (found=%v), want the checkout", root, ok)
	}

	sources.checkout = filepath.Join(t.TempDir(), "never-cloned")
	if root, ok := firstRootWith(sources, filepath.Join("capabilities", "team", "index.md")); !ok || root != mount {
		t.Fatalf("root = %q (found=%v), want the mount to fill the gap", root, ok)
	}
}
