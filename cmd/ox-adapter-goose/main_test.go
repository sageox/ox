package main

import (
	"sort"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// TestHandleInfo_CapabilitiesPinned locks the declared capability set.
//
// KEEP IN SYNC with the "goose" entry in internal/prime/conformance_test.go's
// adapterCaps fixture. Adding or removing a capability requires updating BOTH.
//
// Every capability listed here MUST have its handler registered in the
// adapterruntime.Config literal in main.go. Declaring a capability without
// wiring its handler makes the subcommand return "not implemented" at runtime
// while every check reports the feature as present — see ox-8arr, where exactly
// that made OpenCode's hook-driven recording silently capture nothing.
func TestHandleInfo_CapabilitiesPinned(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo() error: %v", err)
	}

	want := []string{
		adapterprotocol.CapSessionReader,
		adapterprotocol.CapHookInstaller,
		adapterprotocol.CapIncrementalReader,
		adapterprotocol.CapSessionImporter,
		adapterprotocol.CapCapturePrior,
		adapterprotocol.CapServeMode,
	}

	got := append([]string(nil), info.Capabilities...)
	sort.Strings(got)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("capabilities = %v, want %v", got, want)
		}
	}

	// Goose sessions are SQLite rows behind a virtual handle, so there is no
	// path fsnotify could watch. Declaring file_watcher would make the daemon
	// try to tail a handle that will never exist on disk.
	for _, c := range info.Capabilities {
		if c == adapterprotocol.CapFileWatcher {
			t.Error("goose must not declare file_watcher: session handles are virtual")
		}
	}
}

func TestHandleInfo_Identity(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatalf("handleInfo() error: %v", err)
	}

	if info.Name != "goose" {
		t.Errorf("Name = %q, want goose", info.Name)
	}
	if info.Type != adapterprotocol.TypeSession {
		t.Errorf("Type = %q, want %q", info.Type, adapterprotocol.TypeSession)
	}
	if !info.ServeMode {
		t.Error("ServeMode should be true")
	}
	if len(info.HookEnvValues) != 1 || info.HookEnvValues[0] != "goose" {
		t.Errorf("HookEnvValues = %v, want [goose]", info.HookEnvValues)
	}
	if info.ProtocolVersion != adapterprotocol.ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", info.ProtocolVersion, adapterprotocol.ProtocolVersion)
	}
}
