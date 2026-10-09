//go:build slow

package adapters_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func TestCursorCapabilityContract(t *testing.T) {
	root := repoRoot(t)
	bin := build(t, root, "cursor", t.TempDir())
	var info adapterprotocol.InfoResponse
	if output := run(t, bin, "info"); json.Unmarshal([]byte(output), &info) != nil {
		t.Fatalf("Cursor info is not JSON: %s", output)
	}
	want := []string{"hook_installer", "incremental_reader", "serve_mode", "session_reader"}
	got := append([]string(nil), info.Capabilities...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Cursor capabilities = %v, want %v", got, want)
	}
	if !info.ServeMode || len(info.SkillTargets) != 1 || info.SkillTargets[0].Key != "agents-project" {
		t.Fatalf("Cursor runtime descriptors = %+v", info)
	}

	for capability, args := range map[string][]string{
		"session_reader":     {"read", "--session-file", "/nonexistent/cursor.jsonl"},
		"incremental_reader": {"read-from-offset", "--session-file", "/nonexistent/cursor.jsonl", "--offset", "0"},
		"hook_installer":     {"check-hooks", "--repo-root", t.TempDir(), "--scope", "project"},
	} {
		if output := run(t, bin, args...); strings.Contains(output, "not implemented") {
			t.Errorf("Cursor capability %s is not wired: %s", capability, output)
		}
	}

	shutdown, err := json.Marshal(adapterprotocol.Request{ID: 1, Method: adapterprotocol.MethodShutdown})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bin, "--serve")
	command.Dir = filepath.Dir(root)
	command.Stdin = bytes.NewReader(append(shutdown, '\n'))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Cursor serve mode: %v: %s", err, output)
	}
	var response adapterprotocol.Response
	if err := json.Unmarshal(bytes.TrimSpace(output), &response); err != nil || response.ID != 1 || response.Error != nil {
		t.Fatalf("Cursor shutdown response = %q, decode=%v", output, err)
	}
}
