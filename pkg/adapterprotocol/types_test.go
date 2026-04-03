package adapterprotocol_test

import (
	"encoding/json"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

func TestProtocolVersion(t *testing.T) {
	if adapterprotocol.ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1", adapterprotocol.ProtocolVersion)
	}
}

func TestInfoResponse_RoundTrip(t *testing.T) {
	want := adapterprotocol.InfoResponse{
		ProtocolVersion: 1,
		Name:            "claude-code",
		DisplayName:     "Claude Code",
		Version:         "1.2.0",
		Type:            adapterprotocol.TypeSession,
		Capabilities:    []string{adapterprotocol.CapSessionReader, adapterprotocol.CapHookInstaller},
		HookEnvValues:   []string{"claude-code"},
		ServeMode:       true,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.InfoResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if got.ProtocolVersion != want.ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", got.ProtocolVersion, want.ProtocolVersion)
	}
	if got.Type != want.Type {
		t.Errorf("Type = %q, want %q", got.Type, want.Type)
	}
	if len(got.Capabilities) != len(want.Capabilities) {
		t.Errorf("Capabilities len = %d, want %d", len(got.Capabilities), len(want.Capabilities))
	}
}

func TestInfoResponse_WithSubagentConfig(t *testing.T) {
	want := adapterprotocol.InfoResponse{
		ProtocolVersion: 1,
		Name:            "claude-code",
		DisplayName:     "Claude Code",
		Version:         "1.2.0",
		Type:            adapterprotocol.TypeSession,
		Capabilities:    []string{adapterprotocol.CapSessionReader, adapterprotocol.CapSubagentController},
		ServeMode:       true,
		SubagentConfig: &adapterprotocol.SubagentConfig{
			MaxConcurrent:  4,
			Models:         []string{"claude-opus-4-6", "claude-sonnet-4-6"},
			DefaultModel:   "claude-sonnet-4-6",
			CredentialHint: "ANTHROPIC_API_KEY",
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.InfoResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.SubagentConfig == nil {
		t.Fatal("SubagentConfig is nil, want non-nil")
	}
	if got.SubagentConfig.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent = %d, want 4", got.SubagentConfig.MaxConcurrent)
	}
	if got.SubagentConfig.DefaultModel != "claude-sonnet-4-6" {
		t.Errorf("DefaultModel = %q, want claude-sonnet-4-6", got.SubagentConfig.DefaultModel)
	}
	if len(got.SubagentConfig.Models) != 2 {
		t.Errorf("Models len = %d, want 2", len(got.SubagentConfig.Models))
	}
	if got.SubagentConfig.CredentialHint != "ANTHROPIC_API_KEY" {
		t.Errorf("CredentialHint = %q, want ANTHROPIC_API_KEY", got.SubagentConfig.CredentialHint)
	}
}

func TestInfoResponse_SubagentConfigOmittedWhenNil(t *testing.T) {
	info := adapterprotocol.InfoResponse{
		ProtocolVersion: 1,
		Name:            "test",
		Version:         "0.1.0",
		Type:            "test",
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	if _, ok := raw["subagent_config"]; ok {
		t.Error("expected subagent_config to be omitted when nil")
	}
}

func TestDetectResponse_RoundTrip(t *testing.T) {
	want := adapterprotocol.DetectResponse{
		Detected: true,
		Reason:   "found ~/.claude/projects/",
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.DetectResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Detected != want.Detected || got.Reason != want.Reason {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestRawEntry_OmitsEmptyFields(t *testing.T) {
	entry := adapterprotocol.RawEntry{
		Timestamp: "2026-04-02T10:30:00Z",
		Role:      "user",
		Content:   "hello",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// tool-specific fields should be omitted
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	for _, key := range []string{"tool_name", "tool_input", "tool_output", "is_error", "call_id"} {
		if _, ok := raw[key]; ok {
			t.Errorf("expected %q to be omitted for non-tool entry", key)
		}
	}
}

func TestRequest_RoundTrip(t *testing.T) {
	params := json.RawMessage(`{"agent_id":"OxA1b2","repo_root":"/tmp/repo"}`)
	want := adapterprotocol.Request{
		ID:     1,
		Method: adapterprotocol.MethodFindSession,
		Params: params,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.Request
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ID != want.ID || got.Method != want.Method {
		t.Errorf("got id=%d method=%s, want id=%d method=%s", got.ID, got.Method, want.ID, want.Method)
	}
}

func TestResponse_WithError(t *testing.T) {
	resp := adapterprotocol.Response{
		ID: 5,
		Error: &adapterprotocol.RPCError{
			Code:    adapterprotocol.ErrCodeMethodNotFound,
			Message: "unknown method: foo",
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.Response
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if got.Error.Code != adapterprotocol.ErrCodeMethodNotFound {
		t.Errorf("error code = %q, want %q", got.Error.Code, adapterprotocol.ErrCodeMethodNotFound)
	}
}

func TestDiagnoseResult_RoundTrip(t *testing.T) {
	want := adapterprotocol.DiagnoseResult{
		OK: false,
		Issues: []adapterprotocol.DiagnoseIssue{
			{
				Slug:     "hooks-not-installed",
				Severity: "error",
				Title:    "hooks not installed",
				Detail:   "ox hooks missing from settings",
				Fix:      "ox integrate install",
				FixSafe:  true,
			},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.DiagnoseResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.OK != want.OK {
		t.Errorf("OK = %v, want %v", got.OK, want.OK)
	}
	if len(got.Issues) != 1 {
		t.Fatalf("Issues len = %d, want 1", len(got.Issues))
	}
	if got.Issues[0].Slug != want.Issues[0].Slug {
		t.Errorf("Issue slug = %q, want %q", got.Issues[0].Slug, want.Issues[0].Slug)
	}
}

func TestFindSessionParams_RoundTrip(t *testing.T) {
	want := adapterprotocol.FindSessionParams{
		AgentID:  "OxA1b2",
		RepoID:   "abc123",
		TeamID:   "team_xyz",
		RepoRoot: "/tmp/repo",
		Since:    "2026-04-02T10:00:00Z",
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.FindSessionParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// --- AgentSessionID field tests ---

// TestFindSessionParams_AgentSessionID_RoundTrip verifies the new field
// serializes and deserializes correctly.
func TestFindSessionParams_AgentSessionID_RoundTrip(t *testing.T) {
	want := adapterprotocol.FindSessionParams{
		AgentID:        "OxA1b2",
		RepoID:         "abc123",
		RepoRoot:       "/tmp/repo",
		Since:          "2026-04-02T10:00:00Z",
		AgentSessionID: "d8a6d16b-10fe-4c0f-865f-7e05b74e405d",
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.FindSessionParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.AgentSessionID != want.AgentSessionID {
		t.Errorf("AgentSessionID = %q, want %q", got.AgentSessionID, want.AgentSessionID)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestFindSessionParams_AgentSessionID_OmittedWhenEmpty ensures backward
// compatibility: the field is absent from JSON when empty, so old adapters
// that don't know about it won't see an unexpected key.
func TestFindSessionParams_AgentSessionID_OmittedWhenEmpty(t *testing.T) {
	params := adapterprotocol.FindSessionParams{
		AgentID:  "OxA1b2",
		RepoRoot: "/tmp/repo",
		Since:    "2026-04-02T10:00:00Z",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	if _, ok := raw["agent_session_id"]; ok {
		t.Error("expected agent_session_id to be omitted when empty")
	}
}

// TestFindSessionParams_BackwardCompatible verifies that JSON without
// agent_session_id (from an older caller) deserializes cleanly with an
// empty AgentSessionID, preserving backward compatibility.
func TestFindSessionParams_BackwardCompatible(t *testing.T) {
	oldJSON := `{"agent_id":"OxA1b2","repo_id":"abc","repo_root":"/tmp/repo","since":"2026-04-02T10:00:00Z"}`

	var got adapterprotocol.FindSessionParams
	if err := json.Unmarshal([]byte(oldJSON), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.AgentSessionID != "" {
		t.Errorf("AgentSessionID = %q, want empty for old JSON", got.AgentSessionID)
	}
	if got.AgentID != "OxA1b2" {
		t.Errorf("AgentID = %q, want OxA1b2", got.AgentID)
	}
}

// --- Subagent type roundtrip tests ---

func TestSpawnSubagentParams_RoundTrip(t *testing.T) {
	want := adapterprotocol.SpawnSubagentParams{
		WorkerID:   "w-OxA1b2-0001",
		AgentID:    "r7f3a2-OxA1b2",
		RepoID:     "a1b2c3d4",
		TeamID:     "team_xyz",
		RepoRoot:   "/path/to/repo",
		Model:      "claude-sonnet-4-6",
		Task:       "Add unit tests for the auth module.",
		Context:    map[string]any{"files": []any{"src/auth/*.go"}, "max_tokens": float64(8192)},
		TimeoutSec: 1800,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.SpawnSubagentParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.WorkerID != want.WorkerID {
		t.Errorf("WorkerID = %q, want %q", got.WorkerID, want.WorkerID)
	}
	if got.AgentID != want.AgentID {
		t.Errorf("AgentID = %q, want %q", got.AgentID, want.AgentID)
	}
	if got.Task != want.Task {
		t.Errorf("Task = %q, want %q", got.Task, want.Task)
	}
	if got.TimeoutSec != want.TimeoutSec {
		t.Errorf("TimeoutSec = %d, want %d", got.TimeoutSec, want.TimeoutSec)
	}
	if got.Model != want.Model {
		t.Errorf("Model = %q, want %q", got.Model, want.Model)
	}
}

func TestSpawnSubagentParams_OptionalFieldsOmitted(t *testing.T) {
	params := adapterprotocol.SpawnSubagentParams{
		WorkerID: "w-OxA1b2-0001",
		AgentID:  "r7f3a2-OxA1b2",
		RepoID:   "a1b2c3d4",
		RepoRoot: "/path/to/repo",
		Task:     "Run tests",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	for _, key := range []string{"team_id", "model", "context", "timeout_sec"} {
		if _, ok := raw[key]; ok {
			t.Errorf("expected %q to be omitted when zero/empty", key)
		}
	}
}

func TestSpawnSubagentResult_RoundTrip(t *testing.T) {
	want := adapterprotocol.SpawnSubagentResult{
		WorkerID: "w-OxA1b2-0001",
		Status:   adapterprotocol.WorkerStatusStarting,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.SpawnSubagentResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestSubagentStatusParams_RoundTrip(t *testing.T) {
	want := adapterprotocol.SubagentStatusParams{
		WorkerID: "w-OxA1b2-0001",
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.SubagentStatusParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestSubagentStatusResult_RoundTrip(t *testing.T) {
	want := adapterprotocol.SubagentStatusResult{
		WorkerID:     "w-OxA1b2-0001",
		Status:       adapterprotocol.WorkerStatusRunning,
		StartedAt:    "2026-04-02T10:30:00Z",
		ElapsedSec:   47,
		OutputLines:  312,
		LastActivity: "2026-04-02T10:30:47Z",
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.SubagentStatusResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestCancelSubagentParams_RoundTrip(t *testing.T) {
	want := adapterprotocol.CancelSubagentParams{
		WorkerID: "w-OxA1b2-0001",
		Reason:   "user_requested",
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.CancelSubagentParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestCancelSubagentParams_ReasonOmittedWhenEmpty(t *testing.T) {
	params := adapterprotocol.CancelSubagentParams{
		WorkerID: "w-OxA1b2-0001",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	if _, ok := raw["reason"]; ok {
		t.Error("expected reason to be omitted when empty")
	}
}

func TestCancelSubagentResult_RoundTrip(t *testing.T) {
	want := adapterprotocol.CancelSubagentResult{
		WorkerID: "w-OxA1b2-0001",
		Status:   adapterprotocol.WorkerStatusCanceling,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.CancelSubagentResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// --- Subagent event data roundtrip tests ---

func TestSubagentProgressData_RoundTrip(t *testing.T) {
	want := adapterprotocol.SubagentProgressData{
		WorkerID:    "w-OxA1b2-0001",
		OutputType:  "tool_use",
		Tool:        "Write",
		Description: "Wrote src/auth/auth_test.go (342 lines)",
		Offset:      1024,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.SubagentProgressData
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestSubagentProgressData_OptionalFieldsOmitted(t *testing.T) {
	progress := adapterprotocol.SubagentProgressData{
		WorkerID:   "w-OxA1b2-0001",
		OutputType: "message",
	}

	data, err := json.Marshal(progress)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	for _, key := range []string{"tool", "description", "offset"} {
		if _, ok := raw[key]; ok {
			t.Errorf("expected %q to be omitted when zero/empty", key)
		}
	}
}

func TestSubagentCompletedData_RoundTrip(t *testing.T) {
	want := adapterprotocol.SubagentCompletedData{
		WorkerID:      "w-OxA1b2-0001",
		ExitCode:      0,
		DurationSec:   183,
		FilesModified: []string{"src/auth/auth_test.go", "src/auth/token_test.go"},
		Summary:       "Added 47 unit tests covering auth flows.",
		SessionFile:   "/path/to/worker-session.jsonl",
		FinalOffset:   49152,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.SubagentCompletedData
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.WorkerID != want.WorkerID {
		t.Errorf("WorkerID = %q, want %q", got.WorkerID, want.WorkerID)
	}
	if got.ExitCode != want.ExitCode {
		t.Errorf("ExitCode = %d, want %d", got.ExitCode, want.ExitCode)
	}
	if got.DurationSec != want.DurationSec {
		t.Errorf("DurationSec = %d, want %d", got.DurationSec, want.DurationSec)
	}
	if len(got.FilesModified) != len(want.FilesModified) {
		t.Errorf("FilesModified len = %d, want %d", len(got.FilesModified), len(want.FilesModified))
	}
	if got.Summary != want.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, want.Summary)
	}
	if got.SessionFile != want.SessionFile {
		t.Errorf("SessionFile = %q, want %q", got.SessionFile, want.SessionFile)
	}
	if got.FinalOffset != want.FinalOffset {
		t.Errorf("FinalOffset = %d, want %d", got.FinalOffset, want.FinalOffset)
	}
}

func TestSubagentFailedData_RoundTrip(t *testing.T) {
	want := adapterprotocol.SubagentFailedData{
		WorkerID:    "w-OxA1b2-0001",
		ExitReason:  "error",
		ExitCode:    1,
		DurationSec: 12,
		Error:       "Authentication failed: ANTHROPIC_API_KEY not set",
		SessionFile: "/path/to/worker-session.jsonl",
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got adapterprotocol.SubagentFailedData
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.WorkerID != want.WorkerID {
		t.Errorf("WorkerID = %q, want %q", got.WorkerID, want.WorkerID)
	}
	if got.ExitReason != want.ExitReason {
		t.Errorf("ExitReason = %q, want %q", got.ExitReason, want.ExitReason)
	}
	if got.ExitCode != want.ExitCode {
		t.Errorf("ExitCode = %d, want %d", got.ExitCode, want.ExitCode)
	}
	if got.Error != want.Error {
		t.Errorf("Error = %q, want %q", got.Error, want.Error)
	}
}

func TestSubagentFailedData_OptionalFieldsOmitted(t *testing.T) {
	failed := adapterprotocol.SubagentFailedData{
		WorkerID:   "w-OxA1b2-0001",
		ExitReason: "canceled",
	}

	data, err := json.Marshal(failed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	for _, key := range []string{"error", "session_file"} {
		if _, ok := raw[key]; ok {
			t.Errorf("expected %q to be omitted when empty", key)
		}
	}
}

// --- Subagent constant tests ---

func TestSubagentMethodConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"MethodSpawnSubagent", adapterprotocol.MethodSpawnSubagent, "spawn-subagent"},
		{"MethodSubagentStatus", adapterprotocol.MethodSubagentStatus, "subagent-status"},
		{"MethodCancelSubagent", adapterprotocol.MethodCancelSubagent, "cancel-subagent"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestSubagentEventConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"EventSubagentProgress", adapterprotocol.EventSubagentProgress, "subagent.progress"},
		{"EventSubagentCompleted", adapterprotocol.EventSubagentCompleted, "subagent.completed"},
		{"EventSubagentFailed", adapterprotocol.EventSubagentFailed, "subagent.failed"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestWorkerStatusConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"WorkerStatusStarting", adapterprotocol.WorkerStatusStarting, "starting"},
		{"WorkerStatusRunning", adapterprotocol.WorkerStatusRunning, "running"},
		{"WorkerStatusCompleted", adapterprotocol.WorkerStatusCompleted, "completed"},
		{"WorkerStatusFailed", adapterprotocol.WorkerStatusFailed, "failed"},
		{"WorkerStatusCanceled", adapterprotocol.WorkerStatusCanceled, "canceled"},
		{"WorkerStatusTimedOut", adapterprotocol.WorkerStatusTimedOut, "timed_out"},
		{"WorkerStatusCanceling", adapterprotocol.WorkerStatusCanceling, "canceling"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestCapSubagentController(t *testing.T) {
	if adapterprotocol.CapSubagentController != "subagent_controller" {
		t.Errorf("CapSubagentController = %q, want subagent_controller", adapterprotocol.CapSubagentController)
	}
}
