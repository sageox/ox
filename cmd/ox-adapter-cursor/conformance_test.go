package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/sageox/ox/internal/testguard"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

const cursorConformanceFixture = "testdata/desktop/transcript-snapshots/0027-stop.jsonl"

type cursorCompiledResult struct {
	stdout []byte
	stderr []byte
	err    error
}

func cursorRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("repository root not found")
		}
		directory = parent
	}
}

func buildCursorConformanceBinary(t *testing.T) string {
	t.Helper()
	repoRoot := cursorRepositoryRoot(t)
	bin := filepath.Join(t.TempDir(), "ox-adapter-cursor")
	command := exec.Command("go", "build", "-o", bin, "./cmd/ox-adapter-cursor")
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Cursor adapter: %v\n%s", err, output)
	}
	return bin
}

func runCursorCompiled(t *testing.T, bin, cwd string, env []string, stdin []byte, args ...string) cursorCompiledResult {
	t.Helper()
	command := testguard.OxCmd(t, bin, cwd, env, args...)
	command.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return cursorCompiledResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), err: err}
}

func decodeCursorCompiled[T any](t *testing.T, result cursorCompiledResult) T {
	t.Helper()
	if result.err != nil {
		t.Fatalf("compiled command failed: %v; stdout=%q stderr=%q", result.err, result.stdout, result.stderr)
	}
	var value T
	if err := json.Unmarshal(result.stdout, &value); err != nil {
		t.Fatalf("decode compiled response: %v; stdout=%q stderr=%q", err, result.stdout, result.stderr)
	}
	return value
}

func TestCursorCompiledConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds the Cursor adapter binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("native hook sibling fixture is Unix-specific")
	}
	bin := buildCursorConformanceBinary(t)
	repoRoot := cursorRepositoryRoot(t)
	fixture, err := filepath.Abs(cursorConformanceFixture)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("info and one-shot readers", func(t *testing.T) {
		info := decodeCursorCompiled[adapterprotocol.InfoResponse](t, runCursorCompiled(t, bin, repoRoot, nil, nil, "info"))
		if info.Name != adapterName || !info.ServeMode || len(info.Capabilities) != 4 {
			t.Fatalf("info = %+v", info)
		}
		detected := decodeCursorCompiled[adapterprotocol.DetectResponse](t, runCursorCompiled(t, bin, repoRoot, []string{"AGENT_ENV=cursor"}, nil, "detect"))
		if !detected.Detected || detected.Reason != "AGENT_ENV=cursor" {
			t.Fatalf("detect = %+v", detected)
		}
		full := decodeCursorCompiled[adapterprotocol.ReadResult](t, runCursorCompiled(t, bin, repoRoot, nil, nil,
			"read", "--session-file", fixture))
		incremental := decodeCursorCompiled[adapterprotocol.ReadFromOffsetResult](t, runCursorCompiled(t, bin, repoRoot, nil, nil,
			"read-from-offset", "--session-file", fixture, "--offset", "0"))
		if !reflect.DeepEqual(full.Entries, incremental.Entries) {
			t.Fatalf("full entries differ from incremental: full=%+v incremental=%+v", full.Entries, incremental.Entries)
		}
		infoFile, err := os.Stat(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if incremental.NewOffset != infoFile.Size() || len(full.Entries) != 4 || full.Skipped != 1 {
			t.Fatalf("full=%+v incremental offset=%d want=%d", full, incremental.NewOffset, infoFile.Size())
		}
		toolCount := 0
		for _, entry := range full.Entries {
			if entry.Role == adapterprotocol.RoleTool {
				toolCount++
			}
			if entry.Timestamp != "" || entry.CallID != "" || entry.ToolOutput != "" || entry.IsError {
				t.Fatalf("JSONL omission contract violated: %+v", entry)
			}
		}
		if toolCount != 1 {
			t.Fatalf("tool entries = %d, want 1", toolCount)
		}
		metadata := decodeCursorCompiled[adapterprotocol.ReadMetadataResult](t, runCursorCompiled(t, bin, repoRoot, nil, nil,
			"read-metadata", "--session-file", fixture))
		if metadata != (adapterprotocol.ReadMetadataResult{}) {
			t.Fatalf("metadata = %+v, want empty", metadata)
		}
	})

	home := filepath.Join(t.TempDir(), "home")
	project := filepath.Join(t.TempDir(), "control-project")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".sageox"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".sageox", "config.json"), []byte(`{"repo_id":"cursor-conformance"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conversationID := "11111111-1111-1111-1111-111111111111"
	source, err := cursorpaths.SessionPath(home, project, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureData, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, fixtureData, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "ox-argv.log")
	fakeOx := filepath.Join(filepath.Dir(bin), "ox")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CURSOR_OX_ARGV_LOG\"\ncat >/dev/null\nprintf 'COMPILED_CONTEXT'\n"
	if err := os.WriteFile(fakeOx, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "CURSOR_OX_ARGV_LOG=" + logPath}

	t.Run("exact discovery and hook lifecycle", func(t *testing.T) {
		found := decodeCursorCompiled[adapterprotocol.FindSessionResult](t, runCursorCompiled(t, bin, project, env, nil,
			"find-session", "--repo-root", project, "--agent-session-id", conversationID))
		if found.SessionFile != source || found.Offset != 0 {
			t.Fatalf("find = %+v, want exact source at zero", found)
		}
		before := decodeCursorCompiled[adapterprotocol.DiagnoseResult](t, runCursorCompiled(t, bin, project, env, nil,
			"diagnose", "--repo-root", project, "--scope", "project"))
		var repair *adapterprotocol.DiagnoseIssue
		for index := range before.Issues {
			if before.Issues[index].Slug == "hooks-missing" {
				repair = &before.Issues[index]
				break
			}
		}
		if repair == nil || !repair.FixSafe || !reflect.DeepEqual(repair.FixArgv, []string{"ox", "integrate", "install", "--cursor"}) {
			t.Fatalf("pre-install diagnosis = %+v", before)
		}
		installed := decodeCursorCompiled[adapterprotocol.InstallHooksResponse](t, runCursorCompiled(t, bin, project, env, nil,
			"install-hooks", "--repo-root", project, "--scope", "project"))
		if !installed.Installed || len(installed.Hooks) != len(cursorHookEvents) {
			t.Fatalf("install = %+v", installed)
		}
		checked := decodeCursorCompiled[adapterprotocol.CheckHooksResponse](t, runCursorCompiled(t, bin, project, env, nil,
			"check-hooks", "--repo-root", project, "--scope", "project"))
		if !checked.Installed {
			t.Fatalf("check = %+v", checked)
		}
	})

	t.Run("diagnose installed integration", func(t *testing.T) {
		diagnosis := decodeCursorCompiled[adapterprotocol.DiagnoseResult](t, runCursorCompiled(t, bin, project, env, nil,
			"diagnose", "--repo-root", project, "--scope", "project"))
		if !diagnosis.OK || len(diagnosis.Issues) != 0 {
			t.Fatalf("diagnosis = %+v", diagnosis)
		}
	})

	payload := map[string]any{
		"conversation_id":     conversationID,
		"session_id":          conversationID,
		"generation_id":       "22222222-2222-2222-2222-222222222222",
		"workspace_roots":     []string{project},
		"transcript_path":     source,
		"is_background_agent": false,
	}

	t.Run("native hook ABI and exact ox argv", func(t *testing.T) {
		payload["hook_event_name"] = "sessionStart"
		input, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		result := runCursorCompiled(t, bin, project, env, input, "hook", "sessionStart")
		response := decodeCursorCompiled[map[string]any](t, result)
		if response["additional_context"] != "COMPILED_CONTEXT" {
			t.Fatalf("native hook response = %+v", response)
		}
		argv, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(argv) != "agent\nhook\nsessionStart\n" {
			t.Fatalf("ox argv = %q", argv)
		}
	})

	t.Run("serve handshake read unknown and shutdown", func(t *testing.T) {
		findParams, _ := json.Marshal(adapterprotocol.FindSessionParams{AgentID: "OxCursorA", RepoRoot: project, AgentSessionID: conversationID})
		readParams, _ := json.Marshal(adapterprotocol.ReadFromOffsetParams{AgentID: "OxCursorA", SessionFile: source})
		secondSource := filepath.Join(t.TempDir(), "second-session.jsonl")
		secondLine := []byte("{\"role\":\"user\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"synthetic second session\"}]}}\n")
		if err := os.WriteFile(secondSource, secondLine, 0o600); err != nil {
			t.Fatal(err)
		}
		secondParams, _ := json.Marshal(adapterprotocol.ReadFromOffsetParams{AgentID: "OxCursorB", SessionFile: secondSource})
		requests := []adapterprotocol.Request{
			{ID: 1, Method: adapterprotocol.MethodFindSession, Params: findParams},
			{ID: 2, Method: adapterprotocol.MethodReadFromOffset, Params: readParams},
			{ID: 3, Method: adapterprotocol.MethodReadFromOffset, Params: secondParams},
			{ID: 4, Method: "future-method", Params: json.RawMessage(`{}`)},
			{ID: 5, Method: adapterprotocol.MethodShutdown},
		}
		var input bytes.Buffer
		for _, request := range requests {
			if err := json.NewEncoder(&input).Encode(request); err != nil {
				t.Fatal(err)
			}
		}
		result := runCursorCompiled(t, bin, project, append(env, "OX_PROTOCOL_VERSION="+strconv.Itoa(adapterprotocol.ProtocolVersion)), input.Bytes(), "--serve")
		if result.err != nil {
			t.Fatalf("serve: %v; stderr=%q", result.err, result.stderr)
		}
		lines := bytes.Split(bytes.TrimSpace(result.stdout), []byte("\n"))
		if len(lines) != len(requests) {
			t.Fatalf("serve lines=%d want=%d raw=%q", len(lines), len(requests), result.stdout)
		}
		var responses []adapterprotocol.Response
		for _, line := range lines {
			var response adapterprotocol.Response
			if err := json.Unmarshal(line, &response); err != nil {
				t.Fatal(err)
			}
			responses = append(responses, response)
		}
		servedFind := decodeServeResult[adapterprotocol.FindSessionResult](t, responses[0])
		if servedFind.SessionFile != source || servedFind.Offset != 0 {
			t.Fatalf("serve find = %+v", servedFind)
		}
		served := decodeServeResult[adapterprotocol.ReadFromOffsetResult](t, responses[1])
		oneShot := decodeCursorCompiled[adapterprotocol.ReadFromOffsetResult](t, runCursorCompiled(t, bin, project, env, nil,
			"read-from-offset", "--session-file", source, "--offset", "0"))
		if !reflect.DeepEqual(served, oneShot) {
			t.Fatalf("serve=%+v one-shot=%+v", served, oneShot)
		}
		second := decodeServeResult[adapterprotocol.ReadFromOffsetResult](t, responses[2])
		if len(second.Entries) != 1 || second.Entries[0].Content != "synthetic second session" || second.NewOffset != int64(len(secondLine)) {
			t.Fatalf("second Session response = %+v", second)
		}
		if responses[3].Error == nil || responses[3].Error.Code != adapterprotocol.ErrCodeMethodNotFound || responses[4].Error != nil {
			t.Fatalf("serve responses = %+v", responses)
		}
	})

	t.Run("native failure stays fail-open", func(t *testing.T) {
		if err := os.Remove(fakeOx); err != nil {
			t.Fatal(err)
		}
		emptyPath := t.TempDir()
		payload["hook_event_name"] = "beforeSubmitPrompt"
		input, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		result := runCursorCompiled(t, bin, project, append(env, "PATH="+emptyPath), input, "hook", "beforeSubmitPrompt")
		if result.err != nil {
			t.Fatalf("fail-open hook exited: %v", result.err)
		}
		var response map[string]any
		if err := json.Unmarshal(result.stdout, &response); err != nil || response["continue"] != true {
			t.Fatalf("response=%q error=%v", result.stdout, err)
		}
		if !strings.Contains(string(result.stderr), "ox-not-found") || strings.Contains(string(result.stderr), conversationID) {
			t.Fatalf("stderr=%q", result.stderr)
		}
	})

	t.Run("uninstall", func(t *testing.T) {
		uninstalled := decodeCursorCompiled[adapterprotocol.UninstallHooksResponse](t, runCursorCompiled(t, bin, project, env, nil,
			"uninstall-hooks", "--repo-root", project, "--scope", "project"))
		if !uninstalled.Uninstalled {
			t.Fatalf("uninstall = %+v", uninstalled)
		}
	})
}

func TestCursorRealJSONLConformanceIsExplicitAboutOmissions(t *testing.T) {
	fixture, err := filepath.Abs(cursorConformanceFixture)
	if err != nil {
		t.Fatal(err)
	}
	result, err := handleRead(adapterprotocol.ReadParams{SessionFile: fixture})
	if err != nil {
		t.Fatal(err)
	}
	roles := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		roles = append(roles, entry.Role)
		if entry.Timestamp != "" || entry.CallID != "" || entry.ToolOutput != "" || entry.IsError {
			t.Fatalf("unsupported field invented: %+v", entry)
		}
	}
	if fmt.Sprint(roles) != "[user assistant tool assistant]" {
		t.Fatalf("roles = %v", roles)
	}
}
