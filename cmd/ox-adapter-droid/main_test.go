package main

import (
	"reflect"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// TestHandleInfo_CapabilitiesPinned pins this binary's declared capabilities to
// the set the cross-agent conformance fixture mirrors
// (internal/prime/conformance_test.go). Droid declares a rules installer but
// NOT a commands or skills installer. If handleInfo() drifts, this fails so the
// conformance fixture cannot silently fall out of sync with the binary.
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
	if !reflect.DeepEqual(info.Capabilities, want) {
		t.Errorf("droid capabilities drifted from conformance fixture\n got: %v\nwant: %v",
			info.Capabilities, want)
	}
}
