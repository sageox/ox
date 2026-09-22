package main

import (
	"context"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/flags"
)

// TestPacksCommandIsNeverRegisteredWhileGated is a FORWARD guard, and it is
// expected to pass trivially today: `ox packs` does not exist in this binary.
//
// It earns its place on the day someone builds it. ADR-032 requires every packs
// entry point to consult flags.Get().PacksEnabled, because a selection model is
// the hardest feature to take back — once a repository or team records a choice,
// withdrawing the feature orphans the file that recorded it. That is exactly the
// hole `ox skills catalog | install | uninstall` left behind.
//
// A reviewer cannot notice a missing gate; a failing test can. Register `packs`
// unconditionally and this goes red.
func TestPacksCommandIsNeverRegisteredWhileGated(t *testing.T) {
	restore := flagsSnapshot{flags.Get()}
	t.Cleanup(func() { flags.Init(context.Background(), restore) })

	off := flagsSnapshot{flags.Defaults()}
	off.PacksEnabled = false
	flags.Init(context.Background(), off)

	if flags.Get().PacksEnabled {
		t.Fatal("fixture did not take: PacksEnabled is on, so this proves nothing")
	}
	if cmd, _, err := rootCmd.Find([]string{"packs"}); err == nil && cmd != nil && cmd.Name() == "packs" {
		t.Fatal("`ox packs` is reachable with PacksEnabled=false — gate it with setCommandRegistered (ADR-032)")
	}
}

// TestFlagsSnapshotRoundTripsPacksEnabled guards the test fixture itself.
//
// flags.Patch carries a NOTE requiring every new field to be added to allNil()
// and applyPatch(). The snapshot helper is a third site that note does not name,
// and a field missing here does not fail loudly — it silently restores the wrong
// value, so a later test in the same package inherits flag state it never set.
// That is the worst shape of test bug: it moves the failure somewhere else.
func TestFlagsSnapshotRoundTripsPacksEnabled(t *testing.T) {
	restore := flagsSnapshot{flags.Get()}
	t.Cleanup(func() { flags.Init(context.Background(), restore) })

	on := flagsSnapshot{flags.Defaults()}
	on.PacksEnabled = true
	flags.Init(context.Background(), on)

	if !flags.Get().PacksEnabled {
		t.Fatal("flagsSnapshot dropped PacksEnabled on the way through Patch; add the field to the helper")
	}
}

// TestPacksHelpNamesNoUnregisteredCommand is the durable form of a CodeRabbit
// finding on #1026: with the gate ON, `ox sync --help` told users to run
// `ox packs update`, and nothing registers a `packs` command — so the one state
// the flag exists to make safe was the state that produced broken advice.
//
// Deleting the sentence fixes today. This fixes the CLASS: help shown behind the
// gate may not name a command the root cannot resolve, whichever way that stops
// being true. Ship `ox packs` and the assertion stops applying on its own,
// because Find() will resolve it — no test edit needed, and no window where the
// help advertises a command that is still follow-up work.
//
// Failure prevented: `FEATURE_PACKS=true` handing a dogfooder an instruction
// that answers "unknown command".
func TestPacksHelpNamesNoUnregisteredCommand(t *testing.T) {
	restore := flagsSnapshot{flags.Get()}
	t.Cleanup(func() { flags.Init(context.Background(), restore) })

	on := flagsSnapshot{flags.Defaults()}
	on.PacksEnabled = true
	flags.Init(context.Background(), on)
	if !flags.Get().PacksEnabled {
		t.Fatal("fixture did not take: PacksEnabled is off, so this proves nothing")
	}

	gateOn := syncLong(true)
	if !strings.Contains(gateOn, "Pack Catalog") {
		t.Fatal("fixture did not take: gate-on help carries no packs paragraph, so this proves nothing")
	}

	cmd, _, err := rootCmd.Find([]string{"packs"})
	registered := err == nil && cmd != nil && cmd.Name() == "packs"
	if registered {
		return // `ox packs` exists; naming it is now correct.
	}
	if strings.Contains(gateOn, "ox packs") {
		t.Errorf("gate-on `ox sync --help` names `ox packs`, but no such command is registered — "+
			"drop the pointer or register the command:\n%s", gateOn)
	}
}

// TestPacksGateLeavesEverySkillsMechanismAvailable pins the blast radius of the
// packs gate to zero. The Pack Catalog is a *selection* surface layered on top
// of skills delivery; skills delivery itself — `ox sync`, team convergence, and
// the whole `ox skills` command family — predates packs and must keep working
// identically whether the gate is open or shut.
//
// Failure prevented: a future change gating skills machinery behind
// FEATURE_PACKS, so turning packs off (the default) silently stops delivering
// team skills a team already depends on. That failure would be invisible in the
// gate-off default everyone runs.
func TestPacksGateLeavesEverySkillsMechanismAvailable(t *testing.T) {
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

	for _, packs := range []bool{false, true} {
		t.Run(map[bool]string{false: "gate off", true: "gate on"}[packs], func(t *testing.T) {
			f := flagsSnapshot{flags.Defaults()}
			f.PacksEnabled = packs
			flags.Init(context.Background(), f)
			if flags.Get().PacksEnabled != packs {
				t.Fatalf("fixture did not take: PacksEnabled=%v, want %v", flags.Get().PacksEnabled, packs)
			}
			syncFeatureGatedCommands(rootCmd)

			for _, path := range mechanisms {
				cmd, _, err := rootCmd.Find(path)
				if err != nil || cmd == nil || cmd.Name() != path[len(path)-1] {
					t.Errorf("`ox %s` is unreachable with PacksEnabled=%v — the packs gate must not "+
						"cover skills delivery (err=%v)", strings.Join(path, " "), packs, err)
					continue
				}
				if cmd.RunE == nil && cmd.Run == nil {
					t.Errorf("`ox %s` resolved but has no runnable action with PacksEnabled=%v",
						strings.Join(path, " "), packs)
				}
			}
		})
	}
}
