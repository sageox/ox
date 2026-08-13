package prime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the repository root from this test file's package dir
// (internal/prime/ → ../..). The test reads the filesystem to verify that every
// table ID resolves to a real surface; OxCapabilities() itself stays pure.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// TestOxCapabilitiesIDsUnique asserts no two entries share an ID — IDs are the
// stable surface keys both reminder-assembly and the conformance test key on.
func TestOxCapabilitiesIDsUnique(t *testing.T) {
	seen := make(map[string]struct{})
	for _, c := range OxCapabilities() {
		if _, dup := seen[c.ID]; dup {
			t.Errorf("duplicate capability id: %q", c.ID)
		}
		seen[c.ID] = struct{}{}
	}
}

// TestOxCapabilitiesMechanismClassValid asserts every mechanism_class is one of
// the three kebab slugs.
func TestOxCapabilitiesMechanismClassValid(t *testing.T) {
	valid := map[MechanismClass]struct{}{
		MechanismFloor:   {},
		MechanismCommand: {},
		MechanismSkill:   {},
	}
	for _, c := range OxCapabilities() {
		if _, ok := valid[c.MechanismClass]; !ok {
			t.Errorf("capability %q has invalid mechanism_class %q", c.ID, c.MechanismClass)
		}
	}
}

// TestOxCapabilitiesIDResolves asserts every command/skill id resolves to a real
// extensions/claude/commands/<id>.md file, and every floor id resolves to a <id>
// tag emit site in cmd/ox/agent_prime_xml.go. Table-driven over the resolution
// strategy per mechanism class.
func TestOxCapabilitiesIDResolves(t *testing.T) {
	root := repoRoot(t)

	// read the prime XML source once for floor-tag emit-site assertions.
	primeXMLPath := filepath.Join(root, "cmd", "ox", "agent_prime_xml.go")
	primeXMLBytes, err := os.ReadFile(primeXMLPath)
	if err != nil {
		t.Fatalf("read %s: %v", primeXMLPath, err)
	}
	primeXMLSrc := string(primeXMLBytes)

	commandsDir := filepath.Join(root, "extensions", "claude", "commands")
	skillsDir := filepath.Join(root, "extensions", "claude", "skills")

	for _, c := range OxCapabilities() {
		c := c
		t.Run(string(c.MechanismClass)+"/"+c.ID, func(t *testing.T) {
			switch c.MechanismClass {
			case MechanismCommand:
				// id resolves to extensions/claude/commands/<id>.md
				path := filepath.Join(commandsDir, c.ID+".md")
				if _, err := os.Stat(path); err != nil {
					t.Errorf("capability %q (%s) does not resolve to a file: %v", c.ID, c.MechanismClass, err)
				}
			case MechanismSkill:
				// skills are a directory per skill: extensions/claude/skills/<id>/SKILL.md
				path := filepath.Join(skillsDir, c.ID, "SKILL.md")
				if _, err := os.Stat(path); err != nil {
					t.Errorf("capability %q (%s) does not resolve to a file: %v", c.ID, c.MechanismClass, err)
				}
			case MechanismFloor:
				// id resolves to a <id> tag emit site in the prime XML renderer.
				tag := "<" + c.ID + ">"
				if !strings.Contains(primeXMLSrc, tag) {
					t.Errorf("floor capability %q has no emit site %q in %s", c.ID, tag, primeXMLPath)
				}
			default:
				t.Errorf("capability %q has unhandled mechanism_class %q", c.ID, c.MechanismClass)
			}
		})
	}
}

// TestOxCapabilitiesClassInvariants asserts the per-class support invariants:
// floor entries are non-invokable but carry a Layer-1 home; commands are
// slash-only; skills both slash and auto-activate.
func TestOxCapabilitiesClassInvariants(t *testing.T) {
	for _, c := range OxCapabilities() {
		c := c
		t.Run(string(c.MechanismClass)+"/"+c.ID, func(t *testing.T) {
			switch c.MechanismClass {
			case MechanismFloor:
				if c.Supports.Slash || c.Supports.AutoActivate {
					t.Errorf("floor capability %q must not be invokable, got %+v", c.ID, c.Supports)
				}
				if c.Layer1Source == "" {
					t.Errorf("floor capability %q must name a Layer1Source", c.ID)
				}
			case MechanismCommand:
				if !c.Supports.Slash {
					t.Errorf("command capability %q must support slash", c.ID)
				}
				if c.Supports.AutoActivate {
					t.Errorf("command capability %q must not auto-activate", c.ID)
				}
			case MechanismSkill:
				if !c.Supports.Slash || !c.Supports.AutoActivate {
					t.Errorf("skill capability %q must support slash AND auto-activate, got %+v", c.ID, c.Supports)
				}
			}
		})
	}
}

// TestOxCapabilitiesCountsByClass pins the expected entry counts so the table
// stays in sync with the documented surface inventory (5 floor, 14 command,
// 3 skill = 22 total). Update deliberately when surfaces change.
func TestOxCapabilitiesCountsByClass(t *testing.T) {
	counts := map[MechanismClass]int{}
	for _, c := range OxCapabilities() {
		counts[c.MechanismClass]++
	}
	want := map[MechanismClass]int{
		MechanismFloor:   5, // consult-first, rule-promotion, plan-enrichment, visualization, decision-record
		MechanismCommand: 14,
		MechanismSkill:   3, // ox-plan, ox-session-review, ox-recap
	}
	for class, n := range want {
		if counts[class] != n {
			t.Errorf("mechanism_class %q: got %d entries, want %d", class, counts[class], n)
		}
	}
	if total := len(OxCapabilities()); total != 22 {
		t.Errorf("total capabilities: got %d, want 22", total)
	}
}

// TestEveryOnDiskSurfaceIsAccounted closes the disk→table direction of the
// conformance contract. TestOxCapabilitiesIDResolves already proves table→disk
// (every table id resolves to a real file); this proves the reverse: every
// on-disk command (extensions/claude/commands/*.md) and skill
// (extensions/claude/skills/*/SKILL.md) is EITHER an OxCapabilities() row OR an
// explicitly documented additive skill (additiveSkills). Without this, a skill
// like ox-consult — or any future skill/command the installer ships — could
// silently escape the conformance contract.
func TestEveryOnDiskSurfaceIsAccounted(t *testing.T) {
	root := repoRoot(t)

	// build the set of accounted surface ids: table rows + additive allowlist.
	accounted := make(map[string]struct{})
	for _, c := range OxCapabilities() {
		accounted[c.ID] = struct{}{}
	}
	for id := range additiveSkills {
		accounted[id] = struct{}{}
	}

	// on-disk commands: extensions/claude/commands/<id>.md
	commandsDir := filepath.Join(root, "extensions", "claude", "commands")
	commandFiles, err := filepath.Glob(filepath.Join(commandsDir, "*.md"))
	if err != nil {
		t.Fatalf("glob commands: %v", err)
	}
	for _, f := range commandFiles {
		id := strings.TrimSuffix(filepath.Base(f), ".md")
		if _, ok := accounted[id]; !ok {
			t.Errorf("on-disk command %q (%s) is neither an OxCapabilities() row nor in additiveSkills — it has escaped the conformance contract", id, f)
		}
	}

	// on-disk skills: extensions/claude/skills/<id>/SKILL.md
	skillsDir := filepath.Join(root, "extensions", "claude", "skills")
	skillManifests, err := filepath.Glob(filepath.Join(skillsDir, "*", "SKILL.md"))
	if err != nil {
		t.Fatalf("glob skills: %v", err)
	}
	for _, f := range skillManifests {
		id := filepath.Base(filepath.Dir(f))
		if _, ok := accounted[id]; !ok {
			t.Errorf("on-disk skill %q (%s) is neither an OxCapabilities() row nor in additiveSkills — it has escaped the conformance contract", id, f)
		}
	}
}

// TestAdditiveSkillsAreNotTableRows asserts the additive allowlist stays disjoint
// from OxCapabilities(): an additive skill is, by definition, OUTSIDE the
// conformance table (its floor lives in a separate floor-class entry). If a skill
// is promoted to a real table row, it must be removed from additiveSkills so the
// two sources of truth cannot disagree about its status.
func TestAdditiveSkillsAreNotTableRows(t *testing.T) {
	tableIDs := make(map[string]struct{})
	for _, c := range OxCapabilities() {
		tableIDs[c.ID] = struct{}{}
	}
	for id, reason := range additiveSkills {
		if _, ok := tableIDs[id]; ok {
			t.Errorf("additive skill %q is also an OxCapabilities() row — additive skills must stay outside the table (reason on file: %q)", id, reason)
		}
		// each additive entry must carry a non-empty justification so the
		// special status is documented, not silent.
		if strings.TrimSpace(reason) == "" {
			t.Errorf("additive skill %q must document why it is additive (empty reason)", id)
		}
	}

	// ox-consult specifically must be present and additive (its floor is the
	// consult-first ConsultRoutes), never a table row. This pins the special
	// status the review called out.
	if _, ok := additiveSkills["ox-consult"]; !ok {
		t.Errorf("ox-consult must be in additiveSkills (additive; floor is the consult-first entry)")
	}
}
