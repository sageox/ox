package main

import (
	"context"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/flags"
)

// TestAddonsCommandIsNeverRegisteredWhileGated is a FORWARD guard, and it is
// expected to pass trivially today: `ox addons` does not exist in this binary.
//
// It earns its place on the day someone builds it. ADR-032 requires every add-on
// entry point to consult flags.Get().AddonsEnabled, because a selection model is
// the hardest feature to take back — once a repository or team records a choice,
// withdrawing the feature orphans the file that recorded it. That is exactly the
// hole `ox skills catalog | install | uninstall` left behind.
//
// A reviewer cannot notice a missing gate; a failing test can. Register `addons`
// unconditionally and this goes red.
func TestAddonsCommandIsNeverRegisteredWhileGated(t *testing.T) {
	restore := flagsSnapshot{flags.Get()}
	t.Cleanup(func() { flags.Init(context.Background(), restore) })

	off := flagsSnapshot{flags.Defaults()}
	off.AddonsEnabled = false
	flags.Init(context.Background(), off)

	if flags.Get().AddonsEnabled {
		t.Fatal("fixture did not take: AddonsEnabled is on, so this proves nothing")
	}
	if cmd, _, err := rootCmd.Find([]string{"addons"}); err == nil && cmd != nil && cmd.Name() == "addons" {
		t.Fatal("`ox addons` is reachable with AddonsEnabled=false — gate it with setCommandRegistered (ADR-032)")
	}
}

// TestFlagsSnapshotRoundTripsAddonsEnabled guards the test fixture itself.
//
// flags.Patch carries a NOTE requiring every new field to be added to allNil()
// and applyPatch(). The snapshot helper is a third site that note does not name,
// and a field missing here does not fail loudly — it silently restores the wrong
// value, so a later test in the same package inherits flag state it never set.
// That is the worst shape of test bug: it moves the failure somewhere else.
func TestFlagsSnapshotRoundTripsAddonsEnabled(t *testing.T) {
	restore := flagsSnapshot{flags.Get()}
	t.Cleanup(func() { flags.Init(context.Background(), restore) })

	on := flagsSnapshot{flags.Defaults()}
	on.AddonsEnabled = true
	flags.Init(context.Background(), on)

	if !flags.Get().AddonsEnabled {
		t.Fatal("flagsSnapshot dropped AddonsEnabled on the way through Patch; add the field to the helper")
	}
}

// TestAddonsHelpNamesNoUnregisteredCommand is the durable form of a CodeRabbit
// finding on #1026: with the gate ON, `ox sync --help` told users to run
// `ox addons update`, and nothing registers an `addons` command — so the one state
// the flag exists to make safe was the state that produced broken advice.
//
// Deleting the sentence fixes today. This fixes the CLASS: help shown behind the
// gate may not name a command the root cannot resolve, whichever way that stops
// being true. Ship `ox addons` and the assertion stops applying on its own,
// because Find() will resolve it — no test edit needed, and no window where the
// help advertises a command that is still follow-up work.
//
// Failure prevented: `FEATURE_ADDONS=true` handing a dogfooder an instruction
// that answers "unknown command".
func TestAddonsHelpNamesNoUnregisteredCommand(t *testing.T) {
	restore := flagsSnapshot{flags.Get()}
	t.Cleanup(func() { flags.Init(context.Background(), restore) })

	on := flagsSnapshot{flags.Defaults()}
	on.AddonsEnabled = true
	flags.Init(context.Background(), on)
	if !flags.Get().AddonsEnabled {
		t.Fatal("fixture did not take: AddonsEnabled is off, so this proves nothing")
	}

	// Drive the REAL wiring, not syncLong directly. Help text and command
	// registration are two outputs of one function; asserting on the text
	// while skipping the registration half is how this test passed while the
	// pairing it claims to guard was broken.
	longBefore := syncCmd.Long
	t.Cleanup(func() { syncCmd.Long = longBefore })
	syncFeatureGatedCommands(rootCmd)

	gateOn := syncCmd.Long
	if !strings.Contains(gateOn, "Add-on Catalog") {
		t.Fatal("fixture did not take: gate-on help carries no add-on paragraph, so this proves nothing")
	}

	cmd, _, err := rootCmd.Find([]string{"addons"})
	registered := err == nil && cmd != nil && cmd.Name() == "addons"

	if strings.Contains(gateOn, "ox addons") && !registered {
		t.Errorf("gate-on `ox sync --help` names `ox addons`, but no such command is registered — "+
			"drop the pointer or register the command:\n%s", gateOn)
	}

	// The inverse half, which only became testable once the command shipped:
	// registering the verb while the help stays silent about it hides the one
	// instruction a dogfooder with the gate on actually needs.
	if registered && !strings.Contains(gateOn, "ox addons update") {
		t.Errorf("`ox addons` is registered but gate-on `ox sync --help` never names it — "+
			"restore the `ox addons update` pointer:\n%s", gateOn)
	}
}

// TestAddonsGateLeavesEverySkillsMechanismAvailable pins the blast radius of the
// addons gate to zero. The Add-on Catalog is a *selection* surface layered on top
// of skills delivery; skills delivery itself — `ox sync`, team convergence, and
// the whole `ox skills` command family — predates add-ons and must keep working
// identically whether the gate is open or shut.
//
// Failure prevented: a future change gating skills machinery behind
// FEATURE_ADDONS, so turning add-ons off (the default) silently stops delivering
// team skills a team already depends on. That failure would be invisible in the
// gate-off default everyone runs.
func TestAddonsGateLeavesEverySkillsMechanismAvailable(t *testing.T) {
	restore := flagsSnapshot{flags.Get()}
	t.Cleanup(func() { flags.Init(context.Background(), restore) })

	// syncFeatureGatedCommands below REWRITES syncCmd.Long, which is process-wide
	// state a later test in this package asserts on. Restoring it is not tidiness:
	// without this the gate-on pass leaks and
	// TestSyncHelp_DefaultLongMatchesTheGateOffRendering fails somewhere else,
	// which is the worst shape of test bug — it moves the failure.
	longBefore := syncCmd.Long
	t.Cleanup(func() { syncCmd.Long = longBefore })

	// Named explicitly rather than derived from the command tree: deriving the
	// expectation from the thing under test would keep passing if a command
	// disappeared from both sides at once.
	mechanisms := [][]string{
		{"sync"},
		{"skills", "list"},
		{"skills", "status"},
		{"skills", "publish"},
		{"skills", "approve"},
		{"skills", "revoke"},
	}

	for _, addons := range []bool{false, true} {
		t.Run(map[bool]string{false: "gate off", true: "gate on"}[addons], func(t *testing.T) {
			f := flagsSnapshot{flags.Defaults()}
			f.AddonsEnabled = addons
			flags.Init(context.Background(), f)
			if flags.Get().AddonsEnabled != addons {
				t.Fatalf("fixture did not take: AddonsEnabled=%v, want %v", flags.Get().AddonsEnabled, addons)
			}
			syncFeatureGatedCommands(rootCmd)

			for _, path := range mechanisms {
				cmd, _, err := rootCmd.Find(path)
				if err != nil || cmd == nil || cmd.Name() != path[len(path)-1] {
					t.Errorf("`ox %s` is unreachable with AddonsEnabled=%v — the addons gate must not "+
						"cover skills delivery (err=%v)", strings.Join(path, " "), addons, err)
					continue
				}
				if cmd.RunE == nil && cmd.Run == nil {
					t.Errorf("`ox %s` resolved but has no runnable action with AddonsEnabled=%v",
						strings.Join(path, " "), addons)
				}
			}
		})
	}
}
