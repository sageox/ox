//go:build slow

package adapters_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
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

	first := `{"role":"user","message":{"content":[{"type":"text","text":"capability user"}]}}` + "\n"
	source := first + `{"role":"assistant","message":{"content":[{"type":"text","text":"capability assistant"}]}}` + "\n" +
		`{"type":"turn_ended","status":"success"}` + "\n"
	sourcePath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	wantEntries := []adapterprotocol.RawEntry{
		{Role: adapterprotocol.RoleUser, Content: "capability user"},
		{Role: adapterprotocol.RoleAssistant, Content: "capability assistant"},
	}
	runReader := func(t *testing.T, result any, args ...string) {
		t.Helper()
		output, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("Cursor %s failed: %v: %s", args[0], err, output)
		}
		if err := json.Unmarshal(output, result); err != nil {
			t.Fatalf("Cursor %s returned invalid JSON: %v: %s", args[0], err, output)
		}
	}
	t.Run("session_reader", func(t *testing.T) {
		var result adapterprotocol.ReadResult
		runReader(t, &result, "read", "--session-file", sourcePath)
		if !reflect.DeepEqual(result.Entries, wantEntries) || result.Skipped != 1 {
			t.Fatalf("Cursor read = %+v, want entries %v and one skipped terminal row", result, wantEntries)
		}
	})
	t.Run("incremental_reader", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			offset  int
			entries []adapterprotocol.RawEntry
		}{
			{"initial", 0, wantEntries},
			{"resume", len(first), wantEntries[1:]},
			{"eof", len(source), []adapterprotocol.RawEntry{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var result adapterprotocol.ReadFromOffsetResult
				runReader(t, &result, "read-from-offset", "--session-file", sourcePath, "--offset", strconv.Itoa(tc.offset))
				if !reflect.DeepEqual(result.Entries, tc.entries) || result.NewOffset != int64(len(source)) {
					t.Fatalf("Cursor read-from-offset = %+v, want entries %v and offset %d", result, tc.entries, len(source))
				}
			})
		}
	})

	if output := run(t, bin, "check-hooks", "--repo-root", t.TempDir(), "--scope", "project"); strings.Contains(output, "not implemented") {
		t.Errorf("Cursor capability hook_installer is not wired: %s", output)
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
