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
