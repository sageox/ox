package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
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

// TestReadFromOffset_WiredInOneShotMode drives read-from-offset through the
// real CLI dispatch path (adapterruntime.RunWithArgs against adapterConfig,
// exactly what os.Args[1:] does in main), not the handler function directly.
// A binary declaring adapterprotocol.CapIncrementalReader answered every
// one-shot read-from-offset call with {"error":"read-from-offset not
// implemented"} because Config.ReadFromOffset was never set — the daemon's
// catch-up read on restart (internal/daemon/agentwork/session_watcher.go)
// hit exactly this path and silently dropped every turn written since the
// last persisted offset.
func TestReadFromOffset_WiredInOneShotMode(t *testing.T) {
	var buf bytes.Buffer
	args := []string{"read-from-offset", "--session-file", realFixture, "--offset", "0"}
	if err := adapterruntime.RunWithArgs(adapterConfig, args, nil, &buf); err != nil {
		t.Fatalf("read-from-offset one-shot dispatch failed: %v (output: %s)", err, buf.String())
	}
	if strings.Contains(buf.String(), "not implemented") {
		t.Fatalf("read-from-offset returned %q — Config.ReadFromOffset is not wired in main.go", buf.String())
	}

	var result adapterprotocol.ReadFromOffsetResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("decode read-from-offset result: %v (raw: %s)", err, buf.String())
	}
	if len(result.Entries) == 0 {
		t.Fatal("read-from-offset returned zero entries from a real transcript")
	}
	if result.NewOffset <= 0 {
		t.Fatalf("new_offset = %d, want > 0", result.NewOffset)
	}
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
