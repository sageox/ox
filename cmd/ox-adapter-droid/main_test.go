package main

import (
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// TestHandleInfo_CapabilitiesPinned pins this binary's declared capabilities to
// the set the cross-agent conformance fixture mirrors
// (internal/prime/conformance_test.go). Droid declares a rules installer but
// NOT a commands or skills installer. If handleInfo() drifts, this fails so the
// conformance fixture cannot silently fall out of sync with the binary.
//
// KEEP IN SYNC: the want set below must match the "droid" entry in adapterCaps
// in internal/prime/conformance_test.go. Adding/removing a capability requires
// updating BOTH places. Comparison is order-insensitive (a set) so the two
// fixtures need not list caps in the same order.
func TestHandleInfo_CapabilitiesPinned(t *testing.T) {
	want := []string{
		adapterprotocol.CapSessionReader,
		adapterprotocol.CapHookInstaller,
		adapterprotocol.CapRulesInstaller,
		adapterprotocol.CapIncrementalReader,
		adapterprotocol.CapFileWatcher,
		adapterprotocol.CapServeMode,
		adapterprotocol.CapSessionImporter,
	}

	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo() error: %v", err)
	}
	assertCapabilitySetsEqual(t, "droid", info.Capabilities, want)
}

// assertCapabilitySetsEqual compares two capability lists as sets, so the pin
// test does not impose an ordering the conformance fixture (which uses an
// order-insensitive hasCap lookup) is not held to.
func assertCapabilitySetsEqual(t *testing.T, adapter string, got, want []string) {
	t.Helper()
	gotSet := make(map[string]bool, len(got))
	for _, c := range got {
		gotSet[c] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, c := range want {
		wantSet[c] = true
	}
	for c := range wantSet {
		if !gotSet[c] {
			t.Errorf("%s capabilities missing %q (drifted from conformance fixture)\n got: %v\nwant: %v",
				adapter, c, got, want)
		}
	}
	for c := range gotSet {
		if !wantSet[c] {
			t.Errorf("%s capabilities include unexpected %q (drifted from conformance fixture)\n got: %v\nwant: %v",
				adapter, c, got, want)
		}
	}
}
