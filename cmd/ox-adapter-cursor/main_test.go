package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

func TestCursorInfoPinsImplementedCapabilitiesAndSkills(t *testing.T) {
	info, err := handleInfo()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		adapterprotocol.CapHookInstaller,
		adapterprotocol.CapIncrementalReader,
		adapterprotocol.CapServeMode,
		adapterprotocol.CapSessionReader,
	}
	got := append([]string(nil), info.Capabilities...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	if info.Name != adapterName || info.DisplayName != adapterDisplay || info.Type != adapterprotocol.TypeSession {
		t.Fatalf("identity = %+v", info)
	}
	if !info.ServeMode || len(info.HookEnvValues) != 1 || info.HookEnvValues[0] != adapterName {
		t.Fatalf("runtime descriptors = %+v", info)
	}
	if len(info.SkillTargets) != 1 || info.SkillTargets[0] != cursorSkillTargets()[0] {
		t.Fatalf("skill targets = %+v", info.SkillTargets)
	}
	for _, forbidden := range []string{
		adapterprotocol.CapFileWatcher,
		adapterprotocol.CapSessionImporter,
		adapterprotocol.CapCapturePrior,
		adapterprotocol.CapSubagentController,
		adapterprotocol.CapSkillsInstaller,
	} {
		for _, capability := range info.Capabilities {
			if capability == forbidden {
				t.Errorf("unsupported capability advertised: %s", forbidden)
			}
		}
	}
}

func TestCursorAdapterConfigWiresEveryProducer(t *testing.T) {
	missing := make([]string, 0)
	checks := []struct {
		name    string
		present bool
	}{
		{"info", adapterConfig.Info != nil},
		{"detect", adapterConfig.Detect != nil},
		{"install-hooks", adapterConfig.InstallHooks != nil},
		{"check-hooks", adapterConfig.CheckHooks != nil},
		{"uninstall-hooks", adapterConfig.UninstallHooks != nil},
		{"read", adapterConfig.Read != nil},
		{"read-metadata", adapterConfig.ReadMetadata != nil},
		{"diagnose", adapterConfig.Diagnose != nil},
		{"find-session", adapterConfig.FindSession != nil},
		{"read-from-offset", adapterConfig.ReadFromOffset != nil},
		{"serve", adapterConfig.Serve != nil},
	}
	for _, check := range checks {
		if !check.present {
			missing = append(missing, check.name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("unwired producers: %v", missing)
	}
	if adapterConfig.ImportSession != nil || adapterConfig.CapturePrior != nil || adapterConfig.InstallSkills != nil {
		t.Fatal("unsupported import/capture/skill RPC handler was wired")
	}
}

func TestCursorNativeHookDispatchPrecedesAdapterRuntime(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runCursorAdapter([]string{"hook", "beforeSubmitPrompt"}, bytes.NewBufferString("not-json"), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("native response = %q: %v", stdout.String(), err)
	}
	if response["continue"] != true || !bytes.Contains(stderr.Bytes(), []byte("invalid-hook-input")) {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	if err := adapterruntime.RunWithArgs(adapterConfig, []string{"info"}, nil, &stdout); err != nil {
		t.Fatal(err)
	}
	var info adapterprotocol.InfoResponse
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil || info.Name != adapterName {
		t.Fatalf("ordinary runtime response = %q, %v", stdout.String(), err)
	}
}
