package prime

// Cross-agent capability conformance test (epic ox-fvjh).
//
// The two-tier-product risk this guards against: ox is rich on Claude Code
// (Layer-2 commands + skills) but can silently become thin on Codex/Droid if a
// behavior meant to be portable (Layer-1 "floor") ends up living only inside a
// Claude-only command/skill body. Codex declares NEITHER a commands installer
// NOR a rules installer, so anything not carried by Layer 1 (the `ox agent
// prime` output) is invisible to Codex users.
//
// The contract proven here:
//   - Every floor capability has a non-empty Layer1Source (a real Layer-1 home).
//     A floor entry whose only realization is a Claude command/skill body FAILS.
//   - Command-class capabilities resolve only on adapters declaring a commands
//     installer; skill-class only on a skills installer. Their ABSENCE on
//     Codex/Droid is the documented Layer-2 additive case, NOT a failure.
//   - Floor capabilities resolve on ALL adapters (they ride Layer 1).
//   - Codex regression lock: Codex declares zero command/rule/skill installers,
//     yet every floor capability still reaches it via Layer 1.

import (
	"sort"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/claude"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// adapterCaps is a hermetic fixture mirroring each adapter's installed-surface
// capability set as declared by its handleInfo() in cmd/ox-adapter-*/main.go.
// package main is not importable here, so we encode the cap sets locally and
// pin them to the real binaries via the per-adapter guard tests
// (cmd/ox-adapter-*/main_test.go), which compare handleInfo().Capabilities
// against these exact sets (order-insensitively, as sets). If a binary's caps
// drift from this fixture, those guard tests fail — so this fixture cannot
// silently rot.
//
// KEEP IN SYNC: each entry below must match the `want` set in the corresponding
// cmd/ox-adapter-<name>/main_test.go pin test (claude-code / codex / droid).
// Adding or removing a capability requires updating BOTH this fixture AND that
// adapter's pin test.
var adapterCaps = map[string][]string{
	"claude-code": {
		adapterprotocol.CapSessionReader,
		adapterprotocol.CapHookInstaller,
		adapterprotocol.CapRulesInstaller,
		adapterprotocol.CapCommandsInstaller,
		adapterprotocol.CapSkillsInstaller,
		adapterprotocol.CapIncrementalReader,
		adapterprotocol.CapFileWatcher,
		adapterprotocol.CapServeMode,
		adapterprotocol.CapSessionImporter,
		adapterprotocol.CapCapturePrior,
	},
	"codex": {
		adapterprotocol.CapSessionReader,
		adapterprotocol.CapHookInstaller,
		adapterprotocol.CapIncrementalReader,
		adapterprotocol.CapFileWatcher,
		adapterprotocol.CapServeMode,
		adapterprotocol.CapSessionImporter,
	},
	"droid": {
		adapterprotocol.CapSessionReader,
		adapterprotocol.CapHookInstaller,
		adapterprotocol.CapRulesInstaller,
		adapterprotocol.CapIncrementalReader,
		adapterprotocol.CapFileWatcher,
		adapterprotocol.CapServeMode,
		adapterprotocol.CapSessionImporter,
	},
	// No CapFileWatcher: Goose sessions are SQLite rows behind a virtual
	// "goose:<id>" handle, so there is no path for fsnotify to watch.
	"goose": {
		adapterprotocol.CapSessionReader,
		adapterprotocol.CapHookInstaller,
		adapterprotocol.CapIncrementalReader,
		adapterprotocol.CapServeMode,
		adapterprotocol.CapSessionImporter,
		adapterprotocol.CapCapturePrior,
	},
}

// hasCap reports whether the adapter declares the given capability.
func hasCap(adapter, cap string) bool {
	for _, c := range adapterCaps[adapter] {
		if c == cap {
			return true
		}
	}
	return false
}

// floorGuardViolation returns a non-empty message if a capability is a floor
// entry without a Layer-1 representation. This is the single checker behind the
// floor guard; TestFloorGuard_FlagsEmptyLayer1Source pins its behavior on a
// synthetic offender so the guard itself can't silently stop catching things.
func floorGuardViolation(c Capability) string {
	if c.MechanismClass != MechanismFloor {
		return ""
	}
	if c.Layer1Source == "" {
		return "floor capability " + c.ID +
			" has no Layer-1 representation (invisible to codex/droid)"
	}
	return ""
}

// resolvesOn reports whether a capability resolves to an installed surface on
// the given adapter. Floor always resolves (rides Layer 1). Command resolves
// only with a commands installer; skill only with a skills installer.
func resolvesOn(c Capability, adapter string) bool {
	switch c.MechanismClass {
	case MechanismFloor:
		return true
	case MechanismCommand:
		return hasCap(adapter, adapterprotocol.CapCommandsInstaller)
	case MechanismSkill:
		return hasCap(adapter, adapterprotocol.CapSkillsInstaller)
	default:
		return false
	}
}

// TestFloorGuard_EveryFloorHasLayer1 is the catch that matters: every
// MechanismFloor capability must declare a non-empty Layer1Source. A floor
// behavior with no Layer-1 home is trapped inside a Claude-only Layer-2 body
// and never reaches Codex/Droid.
func TestFloorGuard_EveryFloorHasLayer1(t *testing.T) {
	for _, c := range OxCapabilities() {
		if c.MechanismClass != MechanismFloor {
			continue
		}
		if msg := floorGuardViolation(c); msg != "" {
			t.Errorf("%s", msg)
		}
	}
}

// TestFloorGuard_FlagsEmptyLayer1Source documents and locks the failure path:
// a synthetic floor entry with an empty Layer1Source MUST be flagged by the
// checker, with a message naming the id and the "no Layer-1 representation
// (invisible to codex/droid)" phrase. If someone weakens floorGuardViolation,
// this fails even though the real table is clean.
func TestFloorGuard_FlagsEmptyLayer1Source(t *testing.T) {
	offender := Capability{
		ID:             "synthetic-floor-no-layer1",
		MechanismClass: MechanismFloor,
		// Layer1Source intentionally empty.
	}
	msg := floorGuardViolation(offender)
	if msg == "" {
		t.Fatal("floorGuardViolation must flag a floor entry with empty Layer1Source")
	}
	wantID := "synthetic-floor-no-layer1"
	wantPhrase := "no Layer-1 representation (invisible to codex/droid)"
	if !contains(msg, wantID) {
		t.Errorf("violation message must name the offending id %q, got %q", wantID, msg)
	}
	if !contains(msg, wantPhrase) {
		t.Errorf("violation message must contain %q, got %q", wantPhrase, msg)
	}

	// a floor entry WITH a Layer1Source must NOT be flagged.
	ok := Capability{ID: "ok-floor", MechanismClass: MechanismFloor, Layer1Source: "agent prime"}
	if floorGuardViolation(ok) != "" {
		t.Errorf("a floor entry with a Layer1Source must not be flagged")
	}
}

// TestResolution proves the additive-Layer-2 contract across all three
// adapters: every capability either resolves to an installed surface, is floor
// (always resolves), or its absence is the documented Layer-2 additive case
// (command/skill surface not installed on that adapter). No floor capability is
// ever silently omitted.
func TestResolution(t *testing.T) {
	adapters := sortedAdapters()
	for _, c := range OxCapabilities() {
		for _, adapter := range adapters {
			resolves := resolvesOn(c, adapter)
			switch c.MechanismClass {
			case MechanismFloor:
				// Floor MUST resolve everywhere — it rides Layer 1. A floor
				// that fails to resolve is a silent omission, the exact bug.
				if !resolves {
					t.Errorf("floor capability %q does not resolve on %q (silent omission of the floor)",
						c.ID, adapter)
				}
			case MechanismCommand:
				// Resolves iff the adapter installs commands. Absence on
				// codex/droid is the documented additive case — not a failure.
				if resolves != hasCap(adapter, adapterprotocol.CapCommandsInstaller) {
					t.Errorf("command capability %q resolution on %q disagrees with commands-installer presence",
						c.ID, adapter)
				}
			case MechanismSkill:
				if resolves != hasCap(adapter, adapterprotocol.CapSkillsInstaller) {
					t.Errorf("skill capability %q resolution on %q disagrees with skills-installer presence",
						c.ID, adapter)
				}
			default:
				t.Errorf("capability %q has unknown mechanism class %q", c.ID, c.MechanismClass)
			}
		}
	}
}

// TestCodexFloorLock is the explicit "Codex users get the floor" regression
// lock. Codex declares NONE of the Layer-2 installers, yet every floor
// capability still reaches it via Layer 1 (Layer1Source set).
func TestCodexFloorLock(t *testing.T) {
	const codex = "codex"

	for _, cap := range []string{
		adapterprotocol.CapCommandsInstaller,
		adapterprotocol.CapRulesInstaller,
		adapterprotocol.CapSkillsInstaller,
	} {
		if hasCap(codex, cap) {
			t.Fatalf("codex regression lock: codex must NOT declare %q, but it does", cap)
		}
	}

	floorSeen := 0
	for _, c := range OxCapabilities() {
		if c.MechanismClass != MechanismFloor {
			continue
		}
		floorSeen++
		if c.Layer1Source == "" {
			t.Errorf("floor capability %q reaches codex only if Layer1Source is set, but it is empty", c.ID)
		}
		if !resolvesOn(c, codex) {
			t.Errorf("floor capability %q must resolve on codex via Layer 1, but it does not", c.ID)
		}
	}
	if floorSeen == 0 {
		t.Fatal("expected at least one floor capability to lock the codex contract")
	}
}

// sortedAdapters returns the fixture adapter names in deterministic order.
func sortedAdapters() []string {
	names := make([]string, 0, len(adapterCaps))
	for name := range adapterCaps {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// contains is a tiny substring helper (avoids importing strings for one call).
func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ════════════════════════════════════════════════════════════════════════
// Required-directive manifest (bd ox-32f6): a related but distinct
// guarantee from the cross-agent floor conformance above. Where the tests
// above prove a capability REACHES every adapter, these prove a capability
// SURVIVES prime's compact re-prime tier — i.e. that trimming prime's
// static preamble on a routine re-invocation (see ShouldCompactReprime)
// never quietly drops a directive that steers agent behavior on the FULL
// prime path. The manifest itself lives in directives.go so cmd/ox can
// import it too; the actual rendered-XML assertion lives in
// cmd/ox/agent_prime_xml_test.go since rendering only happens there. These
// tests validate the manifest's integrity and cross-check the entries that
// ARE reachable from pure prime-package functions.
// ════════════════════════════════════════════════════════════════════════

// TestRequiredFullPrimeDirectives_WellFormed guards the manifest itself:
// every entry has a name and marker, and no two entries share a name. A
// malformed manifest (empty marker, duplicate name masking a missing
// check) would silently stop proving anything.
func TestRequiredFullPrimeDirectives_WellFormed(t *testing.T) {
	directives := RequiredFullPrimeDirectives()
	if len(directives) == 0 {
		t.Fatal("expected a non-empty required-directive manifest")
	}
	seenNames := make(map[string]struct{}, len(directives))
	for _, d := range directives {
		if d.Name == "" {
			t.Errorf("directive with marker %q has empty Name", d.Marker)
		}
		if d.Marker == "" {
			t.Errorf("directive %q has empty Marker", d.Name)
		}
		if _, dup := seenNames[d.Name]; dup {
			t.Errorf("duplicate directive name %q", d.Name)
		}
		seenNames[d.Name] = struct{}{}
	}
}

// TestRequiredFullPrimeDirectives_ConsultFirstReachable proves the
// "consult-first query routing" manifest entry corresponds to a real route
// in OxCapabilities()'s consult-first floor entry — not a marker that was
// copied once and now never matches because the source text drifted.
func TestRequiredFullPrimeDirectives_ConsultFirstReachable(t *testing.T) {
	var routes []ConsultRoute
	for _, c := range OxCapabilities() {
		if c.MechanismClass == MechanismFloor && c.ID == "consult-first" {
			routes = c.ConsultRoutes
		}
	}
	if len(routes) == 0 {
		t.Fatal("expected the consult-first floor capability to carry ConsultRoutes")
	}

	for _, d := range RequiredFullPrimeDirectives() {
		if d.Name != "consult-first query routing" {
			continue
		}
		found := false
		for _, r := range routes {
			if contains(r.Command, d.Marker) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("manifest directive %q (marker %q) does not appear in any consult-first route command", d.Name, d.Marker)
		}
	}
}

// TestRequiredFullPrimeDirectives_CommandTableReachable proves every
// "command table: *" manifest entry corresponds to a real IntentCommand row
// BuildGuidance produces when every gated feature is available (team
// context, ledger, code index, coworkers, murmuring, memory). A maximal
// GuidanceParams fixture mirrors a fully-loaded real session — the shape
// cmd/ox/agent_prime_xml.go renders into the <commands> table.
// Failure prevented: a manifest entry that documents a command row which
// BuildGuidance no longer actually produces (renamed intent, dropped gate).
func TestRequiredFullPrimeDirectives_CommandTableReachable(t *testing.T) {
	params := GuidanceParams{
		AgentID:  "test-agent",
		RepoSlug: "org/repo",
		TeamCtx: &TeamContextInfo{
			TeamID:               "team-1",
			TeamName:             "Test Team",
			Coworkers:            []claude.Agent{{Name: "reviewer", Description: "reviews code"}},
			ObservationGuideHint: "/team/memory/GUIDE.md",
		},
		Ledger:           &LedgerInfo{Exists: true},
		CodeDBExists:     true,
		MemoryEnabled:    true,
		MurmuringEnabled: true,
		AgentType:        "claude-code",
	}
	guidance := BuildGuidance(params)

	var commandBlob strings.Builder
	for _, c := range guidance.Commands {
		commandBlob.WriteString(c.Intent)
		commandBlob.WriteByte(' ')
		commandBlob.WriteString(c.Command)
		commandBlob.WriteByte('\n')
	}
	blob := commandBlob.String()

	for _, d := range RequiredFullPrimeDirectives() {
		if !strings.HasPrefix(d.Name, "command table:") {
			continue
		}
		if !contains(blob, d.Marker) {
			t.Errorf("manifest directive %q (marker %q) not found in a maximally-populated BuildGuidance() output:\n%s", d.Name, d.Marker, blob)
		}
	}
}
