package main

import (
	"io"
	"strings"
	"testing"
)

// addons_gate_test.go — what survived the removal of FEATURE_ADDONS.
//
// Two tests here used to exist only because of the flag and are gone with it:
// one asserted `ox addons` was NOT registered below the gate, the other that
// the flags snapshot round-tripped AddonsEnabled. Neither has a subject now.
// Deleting them beats keeping tests that pass because nothing can fail.
//
// The two below never depended on the flag for their claim, only for their
// fixture, so they keep their teeth unconditionally.

// TestAddonsHelpNamesNoRegisteredCommandItLacks is the durable form of a
// CodeRabbit finding on #1026: `ox sync --help` told users to run
// `ox addons update` while nothing registered an `addons` command, so the help
// sent them to "unknown command".
//
// The flag is gone, so the paragraph is unconditional — which makes the
// invariant stronger, not weaker: help that names a command the root cannot
// resolve is now always wrong, with no gate state to excuse it.
//
// It queries rootCmd rather than matching a hardcoded string, so it keeps
// working if the command is ever renamed.
func TestAddonsHelpNamesNoRegisteredCommandItLacks(t *testing.T) {
	long := syncLong()
	if !strings.Contains(long, "Add-on Catalog") {
		t.Fatal("fixture did not take: sync help carries no add-on paragraph, so this proves nothing")
	}

	cmd, _, err := rootCmd.Find([]string{"addons"})
	registered := err == nil && cmd != nil && cmd.Name() == "addons"

	if strings.Contains(long, "ox addons") && !registered {
		t.Errorf("`ox sync --help` names `ox addons`, but no such command is registered — "+
			"drop the pointer or register the command:\n%s", long)
	}
	if registered && !strings.Contains(long, "ox addons update") {
		t.Errorf("`ox addons` is registered but `ox sync --help` never names it — "+
			"restore the `ox addons update` pointer:\n%s", long)
	}
}

// TestEverySkillsMechanismAndAddonsAreReachable is what remains of the
// blast-radius test once there is no gate to bound.
//
// Its original claim — "the add-ons gate must not cover skills delivery" —
// has no subject anymore. The surviving one still matters: `ox addons` and the
// skills commands are registered by different mechanisms (init() here, init()
// in their own files), and a registration or init-order regression that drops
// any of them is invisible until a user types the command.
//
// Failure prevented: a command silently disappearing from the binary. Names
// are listed explicitly rather than derived from the command tree — deriving
// the expectation from the thing under test would keep passing if a command
// vanished from both sides at once.
func TestEverySkillsMechanismAndAddonsAreReachable(t *testing.T) {
	for _, path := range [][]string{
		{"sync"},
		{"skills", "list"},
		{"skills", "status"},
		{"skills", "publish"},
		{"skills", "approve"},
		{"skills", "revoke"},
		{"addons", "list"},
		{"addons", "install"},
		{"addons", "update"},
		{"addons", "remove"},
	} {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			cmd, _, err := rootCmd.Find(path)
			if err != nil || cmd == nil || cmd.Name() != path[len(path)-1] {
				t.Fatalf("`ox %s` is unreachable (err=%v)", strings.Join(path, " "), err)
			}
			if cmd.RunE == nil && cmd.Run == nil {
				t.Errorf("`ox %s` resolved but has no runnable action", strings.Join(path, " "))
			}
		})
	}
}

// TestAddonsIsPublic pins the product decision that `ox addons` is an ordinary,
// discoverable command — not hidden, not gated.
//
// Failure prevented: someone reinstating Hidden:true or a flag as a "safety"
// measure. The safety belongs on the remote provider that introduces untrusted
// bytes, not on the mechanism that installs bytes we compiled in ourselves.
func TestAddonsIsPublic(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"addons"})
	if err != nil || cmd == nil {
		t.Fatalf("`ox addons` must be registered: %v", err)
	}
	if cmd.Hidden {
		t.Error("`ox addons` must be discoverable in `ox --help`, not Hidden")
	}
}

// TestWithdrawnVerbsFailLoudly pins the message a user gets when they follow a
// stale doc.
//
// `ox skills catalog | install | uninstall` were withdrawn before release
// (ADR-032 D1, GH #1028), so blog posts, older READMEs and scripts will keep
// reaching for them. Cobra's default for an unmatched token on a parent with no
// Run is to swallow it as an argument and print generic help — which reads like
// the command did something. It exits non-zero, so a script does notice, but a
// human reading the output does not.
//
// Failure prevented: someone concluding `ox skills install` "worked but printed
// help", instead of learning the verb is gone and which verbs replaced it.
func TestWithdrawnVerbsFailLoudly(t *testing.T) {
	for _, tc := range []struct{ parent, verb string }{
		{"skills", "catalog"},
		{"skills", "install"},
		{"skills", "uninstall"},
		{"addons", "bogus"},
	} {
		t.Run(tc.parent+" "+tc.verb, func(t *testing.T) {
			cmd, _, err := rootCmd.Find([]string{tc.parent})
			if err != nil || cmd == nil {
				t.Fatalf("`ox %s` must exist: %v", tc.parent, err)
			}
			if cmd.RunE == nil {
				t.Fatalf("`ox %s` has no RunE, so an unknown verb falls through to generic help", tc.parent)
			}

			runErr := cmd.RunE(cmd, []string{tc.verb})
			if runErr == nil {
				t.Fatalf("`ox %s %s` must fail, not print help and look successful", tc.parent, tc.verb)
			}
			if !strings.Contains(runErr.Error(), tc.verb) {
				t.Errorf("the error must NAME the verb the user typed, got: %v", runErr)
			}
			if !strings.Contains(runErr.Error(), "--help") {
				t.Errorf("the error must point at what does exist, got: %v", runErr)
			}
		})
	}
}

// TestBareParentsStillPrintHelp is the other half: making unknown verbs fail
// must not turn a bare `ox skills` into an error. That is the discovery path.
func TestBareParentsStillPrintHelp(t *testing.T) {
	for _, parent := range []string{"skills", "addons"} {
		t.Run(parent, func(t *testing.T) {
			cmd, _, err := rootCmd.Find([]string{parent})
			if err != nil || cmd == nil {
				t.Fatalf("`ox %s` must exist: %v", parent, err)
			}
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			t.Cleanup(func() { cmd.SetOut(nil); cmd.SetErr(nil) })

			if runErr := cmd.RunE(cmd, nil); runErr != nil {
				t.Errorf("bare `ox %s` must print help, not error: %v", parent, runErr)
			}
		})
	}
}
