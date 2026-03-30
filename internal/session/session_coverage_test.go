package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- summary_marker.go ---

func TestWriteAndClearNeedsSummaryMarker(t *testing.T) {
	dir := t.TempDir()

	// write marker
	err := WriteNeedsSummaryMarker(dir, "/tmp/raw.jsonl", "/tmp/ledger/session-1")
	if err != nil {
		t.Fatalf("WriteNeedsSummaryMarker: %v", err)
	}

	// verify file exists with correct content
	data, err := os.ReadFile(filepath.Join(dir, ".needs-summary"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}

	var info NeedsSummaryInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("unmarshal marker: %v", err)
	}
	if info.CacheDir != dir {
		t.Errorf("CacheDir = %q, want %q", info.CacheDir, dir)
	}
	if info.RawPath != "/tmp/raw.jsonl" {
		t.Errorf("RawPath = %q, want /tmp/raw.jsonl", info.RawPath)
	}
	if info.LedgerSessionDir != "/tmp/ledger/session-1" {
		t.Errorf("LedgerSessionDir = %q, want /tmp/ledger/session-1", info.LedgerSessionDir)
	}

	// clear marker
	err = ClearNeedsSummaryMarker(dir)
	if err != nil {
		t.Fatalf("ClearNeedsSummaryMarker: %v", err)
	}

	// verify file is gone
	if _, err := os.Stat(filepath.Join(dir, ".needs-summary")); !os.IsNotExist(err) {
		t.Errorf("marker still exists after clear")
	}
}

func TestClearNeedsSummaryMarker_NoFile(t *testing.T) {
	dir := t.TempDir()
	// clearing a non-existent marker should not error
	if err := ClearNeedsSummaryMarker(dir); err != nil {
		t.Errorf("ClearNeedsSummaryMarker on missing file: %v", err)
	}
}

func TestFindSessionsNeedingSummary_NoDir(t *testing.T) {
	// non-existent context path returns nil, nil
	results, err := FindSessionsNeedingSummary("/nonexistent/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestFindSessionsNeedingSummary_WithMarkers(t *testing.T) {
	contextDir := t.TempDir()
	sessionsDir := filepath.Join(contextDir, "sessions")

	// create two session dirs, one with marker and one without
	sess1 := filepath.Join(sessionsDir, "session-a")
	sess2 := filepath.Join(sessionsDir, "session-b")
	os.MkdirAll(sess1, 0755)
	os.MkdirAll(sess2, 0755)

	// write marker only in session-a
	WriteNeedsSummaryMarker(sess1, "/raw/a.jsonl", "/ledger/a")

	results, err := FindSessionsNeedingSummary(contextDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].CacheDir != sess1 {
		t.Errorf("CacheDir = %q, want %q", results[0].CacheDir, sess1)
	}
}

func TestFindSessionsNeedingSummary_SkipsFiles(t *testing.T) {
	contextDir := t.TempDir()
	sessionsDir := filepath.Join(contextDir, "sessions")
	os.MkdirAll(sessionsDir, 0755)

	// create a regular file (not a directory) in sessions/
	os.WriteFile(filepath.Join(sessionsDir, "not-a-dir"), []byte("hi"), 0644)

	results, err := FindSessionsNeedingSummary(contextDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestFindSessionsNeedingSummary_InvalidJSON(t *testing.T) {
	contextDir := t.TempDir()
	sessionsDir := filepath.Join(contextDir, "sessions")
	sess := filepath.Join(sessionsDir, "bad-json")
	os.MkdirAll(sess, 0755)

	// write invalid JSON as marker
	os.WriteFile(filepath.Join(sess, ".needs-summary"), []byte("not json"), 0644)

	results, err := FindSessionsNeedingSummary(contextDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// invalid JSON markers are silently skipped
	if len(results) != 0 {
		t.Errorf("expected 0 results for invalid JSON, got %d", len(results))
	}
}

// --- metadata.go ---

func TestSessionMeta_Duration_ZeroEnd(t *testing.T) {
	meta := &SessionMeta{
		StartedAt: time.Now(),
		// EndedAt is zero
	}
	if d := meta.Duration(); d != 0 {
		t.Errorf("Duration() = %v, want 0 for unset EndedAt", d)
	}
}

func TestSessionMeta_Duration_WithEnd(t *testing.T) {
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	meta := &SessionMeta{
		StartedAt: start,
		EndedAt:   end,
	}
	d := meta.Duration()
	if d != 30*time.Minute {
		t.Errorf("Duration() = %v, want 30m", d)
	}
}

func TestSessionMeta_Close(t *testing.T) {
	meta := &SessionMeta{
		StartedAt: time.Now().Add(-5 * time.Minute),
	}
	if !meta.EndedAt.IsZero() {
		t.Fatal("EndedAt should be zero before Close()")
	}
	meta.Close()
	if meta.EndedAt.IsZero() {
		t.Error("EndedAt should be set after Close()")
	}
	if meta.EndedAt.Before(meta.StartedAt) {
		t.Error("EndedAt should be after StartedAt")
	}
}

// --- artifacts.go ---

func TestSummarizeResponseToSummaryView_Nil(t *testing.T) {
	result := SummarizeResponseToSummaryView(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %+v", result)
	}
}

func TestSummarizeResponseToSummaryView(t *testing.T) {
	resp := &SummarizeResponse{
		Summary:     "test summary",
		KeyActions:  []string{"action1", "action2"},
		Outcome:     "success",
		TopicsFound: []string{"go", "testing"},
		FinalPlan:   "the plan",
		Diagrams:    []string{"graph TD; A-->B"},
	}

	view := SummarizeResponseToSummaryView(resp)
	if view == nil {
		t.Fatal("expected non-nil view")
	}
	if view.Text != "test summary" {
		t.Errorf("Text = %q, want %q", view.Text, "test summary")
	}
	if len(view.KeyActions) != 2 {
		t.Errorf("KeyActions len = %d, want 2", len(view.KeyActions))
	}
	if view.Outcome != "success" {
		t.Errorf("Outcome = %q, want %q", view.Outcome, "success")
	}
	if len(view.TopicsFound) != 2 {
		t.Errorf("TopicsFound len = %d, want 2", len(view.TopicsFound))
	}
	if view.FinalPlan != "the plan" {
		t.Errorf("FinalPlan = %q, want %q", view.FinalPlan, "the plan")
	}
	if len(view.Diagrams) != 1 {
		t.Errorf("Diagrams len = %d, want 1", len(view.Diagrams))
	}
}

func TestWriteSessionArtifacts_Success(t *testing.T) {
	dir := t.TempDir()
	resp := &SummarizeResponse{
		Summary:    "completed testing",
		KeyActions: []string{"wrote tests"},
		Outcome:    "success",
	}
	stored := &StoredSession{
		Meta:    &StoreMeta{AgentID: "test-agent", CreatedAt: time.Now()},
		Entries: []map[string]any{},
	}

	paths, err := WriteSessionArtifacts(dir, stored, resp)
	if err != nil {
		t.Fatalf("WriteSessionArtifacts: %v", err)
	}

	// verify all paths are set
	if paths.SummaryJSON == "" {
		t.Error("SummaryJSON path is empty")
	}
	if paths.SummaryMD == "" {
		t.Error("SummaryMD path is empty")
	}
	if paths.SessionMD == "" {
		t.Error("SessionMD path is empty")
	}

	// verify summary.json is valid JSON
	data, err := os.ReadFile(paths.SummaryJSON)
	if err != nil {
		t.Fatalf("read summary.json: %v", err)
	}
	var parsed SummarizeResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal summary.json: %v", err)
	}
	if parsed.Summary != "completed testing" {
		t.Errorf("summary = %q, want %q", parsed.Summary, "completed testing")
	}
}

func TestWriteSessionArtifacts_NoSummary(t *testing.T) {
	dir := t.TempDir()
	stored := &StoredSession{
		Meta:    &StoreMeta{AgentID: "test"},
		Entries: []map[string]any{},
	}

	paths, err := WriteSessionArtifacts(dir, stored, nil)
	if err != nil {
		t.Fatalf("WriteSessionArtifacts: %v", err)
	}

	// summary paths should be empty when no summary provided
	if paths.SummaryJSON != "" {
		t.Error("SummaryJSON should be empty with nil summary")
	}
	if paths.SummaryMD != "" {
		t.Error("SummaryMD should be empty with nil summary")
	}
	if paths.SessionMD == "" {
		t.Error("SessionMD path should be set")
	}
}

// --- store.go ---

func TestInferTypeFromFilename(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"raw.jsonl", "raw"},
		{"summary.json", ""},
		{"session.html", ""},
		{"", ""},
		{"something-else.jsonl", ""},
	}
	for _, tt := range tests {
		got := inferTypeFromFilename(tt.filename)
		if got != tt.want {
			t.Errorf("inferTypeFromFilename(%q) = %q, want %q", tt.filename, got, tt.want)
		}
	}
}

// --- guidance.go ---

func TestFormatSummarizeGuidanceJSON(t *testing.T) {
	guidance := GetSummarizeGuidance("agent-1", "/tmp/context")
	summary := &SummarizeOutput{
		Success: true,
		Type:    "session_summary",
		AgentID: "agent-1",
		Summary: "test summary",
	}

	data, err := FormatSummarizeGuidanceJSON("agent-1", guidance, summary)
	if err != nil {
		t.Fatalf("FormatSummarizeGuidanceJSON: %v", err)
	}

	var output SummarizeGuidanceOutput
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !output.Success {
		t.Error("Success should be true")
	}
	if output.Type != "session_summarize_guidance" {
		t.Errorf("Type = %q, want session_summarize_guidance", output.Type)
	}
	if output.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1", output.AgentID)
	}
	if output.Summary == nil {
		t.Error("Summary should not be nil")
	}
}

func TestFormatSummarizeGuidanceJSON_NilSummary(t *testing.T) {
	guidance := GetSummarizeGuidance("agent-2", "/tmp/ctx")

	data, err := FormatSummarizeGuidanceJSON("agent-2", guidance, nil)
	if err != nil {
		t.Fatalf("FormatSummarizeGuidanceJSON: %v", err)
	}

	var output SummarizeGuidanceOutput
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if output.Summary != nil {
		t.Error("Summary should be nil")
	}
}

func TestFormatSummaryJSON(t *testing.T) {
	resp := &SummarizeResponse{
		Summary:     "built feature X",
		KeyActions:  []string{"wrote code", "ran tests"},
		Outcome:     "success",
		TopicsFound: []string{"go"},
		FinalPlan:   "ship it",
		Diagrams:    []string{"graph LR; A-->B"},
	}

	data, err := FormatSummaryJSON("agent-x", resp, 42, "/tmp/summary.json", "done")
	if err != nil {
		t.Fatalf("FormatSummaryJSON: %v", err)
	}

	var output SummarizeOutput
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !output.Success {
		t.Error("Success should be true")
	}
	if output.Type != "session_summary" {
		t.Errorf("Type = %q, want session_summary", output.Type)
	}
	if output.AgentID != "agent-x" {
		t.Errorf("AgentID = %q, want agent-x", output.AgentID)
	}
	if output.Summary != "built feature X" {
		t.Errorf("Summary = %q, want 'built feature X'", output.Summary)
	}
	if output.EntryCount != 42 {
		t.Errorf("EntryCount = %d, want 42", output.EntryCount)
	}
	if output.FilePath != "/tmp/summary.json" {
		t.Errorf("FilePath = %q, want /tmp/summary.json", output.FilePath)
	}
	if output.Message != "done" {
		t.Errorf("Message = %q, want done", output.Message)
	}
	if output.FinalPlan != "ship it" {
		t.Errorf("FinalPlan = %q, want 'ship it'", output.FinalPlan)
	}
	if len(output.Diagrams) != 1 {
		t.Errorf("Diagrams len = %d, want 1", len(output.Diagrams))
	}
}

// --- summary_md.go ---

func TestWriteFinalPlan(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()
	meta := &StoreMeta{AgentID: "test", CreatedAt: time.Now()}
	summary := &SummaryView{
		Text:      "summary",
		FinalPlan: "Step 1: Do thing\nStep 2: Do other thing",
	}

	md, err := gen.Generate(meta, summary, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := string(md)
	if !contains(content, "## Final Plan") {
		t.Error("missing Final Plan heading")
	}
	if !contains(content, "Step 1: Do thing") {
		t.Error("missing plan content")
	}
}

func TestWriteDiagrams(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()
	meta := &StoreMeta{AgentID: "test", CreatedAt: time.Now()}
	summary := &SummaryView{
		Text:     "summary",
		Diagrams: []string{"graph TD; A-->B"},
	}

	md, err := gen.Generate(meta, summary, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := string(md)
	if !contains(content, "## Diagrams") {
		t.Error("missing Diagrams heading")
	}
	if !contains(content, "Mermaid Source") {
		t.Error("missing Mermaid Source details tag")
	}
	if !contains(content, "graph TD; A-->B") {
		t.Error("missing mermaid code in output")
	}
}

func TestExtractPathFromToolInput(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()

	tests := []struct {
		name string
		data map[string]any
		want string
	}{
		{
			name: "path from tool_input string",
			data: map[string]any{
				"tool_input": "editing /src/main.go with changes",
			},
			want: "/src/main.go",
		},
		{
			name: "path from content field",
			data: map[string]any{
				"content": "modified config/settings.yaml",
			},
			want: "config/settings.yaml",
		},
		{
			name: "no path found",
			data: map[string]any{
				"tool_input": "no file path here",
			},
			want: "",
		},
		{
			name: "empty data",
			data: map[string]any{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gen.extractPathFromToolInput(tt.data)
			if got != tt.want {
				t.Errorf("extractPathFromToolInput() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- summarize.go ---

func TestEntriesToPkg_RoundTrip(t *testing.T) {
	now := time.Now()
	entries := []Entry{
		{
			Timestamp: now,
			Type:      SessionEntryTypeUser,
			Content:   "hello",
		},
		{
			Timestamp:  now,
			Type:       SessionEntryTypeTool,
			Content:    "",
			ToolName:   "bash",
			ToolInput:  "ls",
			ToolOutput: "file1\nfile2",
		},
		{
			Timestamp: now,
			Type:      SessionEntryTypeAssistant,
			Content:   "response",
		},
	}

	// convert to pkg and back
	pkg := entriesToPkg(entries)
	if len(pkg) != 3 {
		t.Fatalf("entriesToPkg returned %d entries, want 3", len(pkg))
	}

	// verify pkg fields
	if pkg[0].Type != "user" {
		t.Errorf("pkg[0].Type = %q, want user", pkg[0].Type)
	}
	if pkg[1].ToolName != "bash" {
		t.Errorf("pkg[1].ToolName = %q, want bash", pkg[1].ToolName)
	}
	if pkg[1].ToolOutput != "file1\nfile2" {
		t.Errorf("pkg[1].ToolOutput = %q, want file1\\nfile2", pkg[1].ToolOutput)
	}
	if pkg[2].Content != "response" {
		t.Errorf("pkg[2].Content = %q, want response", pkg[2].Content)
	}

	// convert back
	roundTripped := pkgToEntries(pkg)
	if len(roundTripped) != 3 {
		t.Fatalf("pkgToEntries returned %d entries, want 3", len(roundTripped))
	}

	if roundTripped[0].Type != SessionEntryTypeUser {
		t.Errorf("roundTripped[0].Type = %q, want user", roundTripped[0].Type)
	}
	if roundTripped[1].ToolName != "bash" {
		t.Errorf("roundTripped[1].ToolName = %q, want bash", roundTripped[1].ToolName)
	}
	if roundTripped[2].Content != "response" {
		t.Errorf("roundTripped[2].Content = %q, want response", roundTripped[2].Content)
	}
}

func TestEntriesToPkg_Empty(t *testing.T) {
	pkg := entriesToPkg(nil)
	if len(pkg) != 0 {
		t.Errorf("entriesToPkg(nil) returned %d entries, want 0", len(pkg))
	}

	entries := pkgToEntries(nil)
	if len(entries) != 0 {
		t.Errorf("pkgToEntries(nil) returned %d entries, want 0", len(entries))
	}
}

func TestEntriesToPkg_TimestampPreserved(t *testing.T) {
	ts := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	entries := []Entry{
		{Timestamp: ts, Type: SessionEntryTypeUser, Content: "test"},
	}

	pkg := entriesToPkg(entries)
	if !pkg[0].Timestamp.Equal(ts) {
		t.Errorf("timestamp not preserved: got %v, want %v", pkg[0].Timestamp, ts)
	}

	back := pkgToEntries(pkg)
	if !back[0].Timestamp.Equal(ts) {
		t.Errorf("round-trip timestamp not preserved: got %v, want %v", back[0].Timestamp, ts)
	}
}

// --- summary_md.go: file modification extraction ---

func TestExtractFileModifications_ToolEntries(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()

	entries := []map[string]any{
		{
			"type": "tool",
			"data": map[string]any{
				"tool_name":  "Write",
				"tool_input": "writing to /src/app.go",
			},
		},
		{
			"type": "tool",
			"data": map[string]any{
				"tool_name":  "Edit",
				"tool_input": "editing /src/app.go",
			},
		},
		{
			"type": "tool",
			"data": map[string]any{
				"tool_name":  "Bash",
				"tool_input": "touch /tmp/newfile.txt",
			},
		},
	}

	mods := gen.extractFileModifications(entries)
	if len(mods) < 1 {
		t.Fatalf("expected at least 1 modification, got %d", len(mods))
	}

	// /src/app.go should be present (Written then Edited -> Modified)
	found := false
	for _, m := range mods {
		if m.Path == "/src/app.go" {
			found = true
			if m.Action != "Modified" {
				t.Errorf("expected Modified for /src/app.go, got %s", m.Action)
			}
		}
	}
	if !found {
		t.Error("expected /src/app.go in modifications")
	}
}

func TestExtractFileModifications_CommandEntries(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()

	entries := []map[string]any{
		{
			"type":    "command_run",
			"summary": "rm /tmp/old.txt",
		},
		{
			"type":    "command_run",
			"summary": "mv /tmp/a.go /tmp/b.go",
		},
	}

	mods := gen.extractFileModifications(entries)

	// verify rm creates Deleted entry
	deletedFound := false
	for _, m := range mods {
		if m.Path == "/tmp/old.txt" && m.Action == "Deleted" {
			deletedFound = true
		}
	}
	if !deletedFound {
		t.Error("expected /tmp/old.txt Deleted in modifications")
	}
}

// --- summary_md.go: Generate with topics ---

func TestSummaryMarkdownGenerator_Topics(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()
	summary := &SummaryView{
		Text:        "built tests",
		TopicsFound: []string{"testing", "coverage"},
	}

	md, err := gen.Generate(nil, summary, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := string(md)
	if !contains(content, "## Topics") {
		t.Error("missing Topics section")
	}
	if !contains(content, "`testing`") {
		t.Error("missing testing topic tag")
	}
	if !contains(content, "`coverage`") {
		t.Error("missing coverage topic tag")
	}
}

func TestSummaryMarkdownGenerator_NoMetadata(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()
	md, err := gen.Generate(nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := string(md)
	if !contains(content, "_No metadata available_") {
		t.Error("missing 'No metadata available' fallback")
	}
}

func TestSummaryMarkdownGenerator_FullMetadata(t *testing.T) {
	gen := NewSummaryMarkdownGenerator()
	meta := &StoreMeta{
		CreatedAt:    time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		AgentID:      "Ox1234",
		AgentType:    "claude-code",
		AgentVersion: "1.2.3",
		Model:        "claude-sonnet-4-20250514",
		Username:     "testuser",
	}
	summary := &SummaryView{
		Text:    "test",
		Outcome: "success",
	}

	md, err := gen.Generate(meta, summary, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	content := string(md)
	if !contains(content, "2026-03-15") {
		t.Error("missing date in metadata")
	}
	if !contains(content, "claude-code 1.2.3") {
		t.Error("missing agent type+version")
	}
	if !contains(content, "claude-sonnet-4-20250514") {
		t.Error("missing model")
	}
	if !contains(content, "testuser") {
		t.Error("missing username")
	}
	if !contains(content, "Ox1234") {
		t.Error("missing agent ID")
	}
	if !contains(content, "success") {
		t.Error("missing outcome")
	}
}

// --- guidance.go: FormatGuidanceJSON edge cases ---

func TestFormatGuidanceJSON_AllPhases(t *testing.T) {
	phases := []GuidancePhase{GuidancePhaseStart, GuidancePhaseStop, GuidancePhaseRemind}
	for _, phase := range phases {
		guidance := SessionGuidance{
			Include: []string{"test"},
		}
		data, err := FormatGuidanceJSON(phase, "agent-1", guidance, "msg")
		if err != nil {
			t.Fatalf("FormatGuidanceJSON(%s): %v", phase, err)
		}

		var output GuidanceOutput
		if err := json.Unmarshal(data, &output); err != nil {
			t.Fatalf("unmarshal(%s): %v", phase, err)
		}
		if output.Phase != phase {
			t.Errorf("Phase = %q, want %q", output.Phase, phase)
		}
		if output.Message != "msg" {
			t.Errorf("Message = %q, want msg", output.Message)
		}
	}
}

// --- guidance.go: FormatHTMLGuidanceJSON ---

func TestFormatHTMLGuidanceJSON_WithGenerated(t *testing.T) {
	guidance := GetHTMLGuidance("agent-1", "/raw.jsonl", "/out.html")

	data, err := FormatHTMLGuidanceJSON("agent-1", guidance, true, "/out.html", "generated ok")
	if err != nil {
		t.Fatalf("FormatHTMLGuidanceJSON: %v", err)
	}

	var output HTMLGuidanceOutput
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !output.Success {
		t.Error("Success should be true")
	}
	if output.Type != "session_html_guidance" {
		t.Errorf("Type = %q, want session_html_guidance", output.Type)
	}
	if !output.Generated {
		t.Error("Generated should be true")
	}
	if output.HTMLPath != "/out.html" {
		t.Errorf("HTMLPath = %q, want /out.html", output.HTMLPath)
	}
	if output.Message != "generated ok" {
		t.Errorf("Message = %q, want 'generated ok'", output.Message)
	}
}

// --- guidance.go: StartGuidanceWithOptions ---

func TestStartGuidanceWithOptions_AutoStarted(t *testing.T) {
	g := StartGuidanceWithOptions(StartGuidanceOptions{AutoStarted: true})
	if !contains(g.UserNotification, "Disable auto-start") {
		t.Error("auto-started guidance should mention disabling auto-start")
	}
}

func TestStartGuidanceWithOptions_ManualStart(t *testing.T) {
	g := StartGuidanceWithOptions(StartGuidanceOptions{AutoStarted: false})
	if contains(g.UserNotification, "Disable auto-start") {
		t.Error("manual-start guidance should not mention disabling auto-start")
	}
	if !contains(g.UserNotification, "Recording session") {
		t.Error("should contain recording notification")
	}
}

// --- markdown.go: mdExtractEndTime, mdCountEntryTypes, mdGetPreview ---

func TestMdExtractEndTime_FromFooter(t *testing.T) {
	ts := time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC)
	stored := &StoredSession{
		Footer: map[string]any{
			"closed_at": ts.Format(time.RFC3339Nano),
		},
	}
	got := mdExtractEndTime(stored)
	if !got.Equal(ts) {
		t.Errorf("mdExtractEndTime = %v, want %v", got, ts)
	}
}

func TestMdExtractEndTime_FromModTime(t *testing.T) {
	modTime := time.Date(2026, 3, 15, 11, 0, 0, 0, time.UTC)
	stored := &StoredSession{
		Info: SessionInfo{ModTime: modTime},
	}
	got := mdExtractEndTime(stored)
	if !got.Equal(modTime) {
		t.Errorf("mdExtractEndTime = %v, want %v", got, modTime)
	}
}

func TestMdExtractEndTime_Zero(t *testing.T) {
	stored := &StoredSession{}
	got := mdExtractEndTime(stored)
	if !got.IsZero() {
		t.Errorf("mdExtractEndTime = %v, want zero", got)
	}
}

func TestMdCountEntryTypes(t *testing.T) {
	entries := []map[string]any{
		{"type": "user"},
		{"type": "human"},
		{"type": "assistant"},
		{"type": "ai"},
		{"type": "tool"},
		{"type": "tool_use"},
		{"type": "tool_result"},
		{"type": "system"},
		{"type": "unknown"},
	}

	counts := mdCountEntryTypes(entries)
	if counts["user"] != 2 {
		t.Errorf("user count = %d, want 2", counts["user"])
	}
	if counts["assistant"] != 2 {
		t.Errorf("assistant count = %d, want 2", counts["assistant"])
	}
	if counts["tool_call"] != 2 {
		t.Errorf("tool_call count = %d, want 2", counts["tool_call"])
	}
	if counts["tool_result"] != 1 {
		t.Errorf("tool_result count = %d, want 1", counts["tool_result"])
	}
	if counts["system"] != 1 {
		t.Errorf("system count = %d, want 1", counts["system"])
	}
}

func TestMdGetPreview_Short(t *testing.T) {
	got := mdGetPreview("hello world", 100)
	if got != "hello world" {
		t.Errorf("mdGetPreview = %q, want %q", got, "hello world")
	}
}

func TestMdGetPreview_Truncated(t *testing.T) {
	content := "the quick brown fox jumps over the lazy dog"
	got := mdGetPreview(content, 20)
	if len(got) > 20 {
		t.Errorf("mdGetPreview length = %d, want <= 20", len(got))
	}
}

func TestMdGetPreview_NormalizesWhitespace(t *testing.T) {
	content := "hello   world\n\ttab"
	got := mdGetPreview(content, 100)
	if got != "hello world tab" {
		t.Errorf("mdGetPreview = %q, want %q", got, "hello world tab")
	}
}

// --- mermaid.go: ExtractMermaidBlocks ---

func TestExtractMermaidBlocks(t *testing.T) {
	content := "some text\n```mermaid\ngraph TD; A-->B\n```\nmore text\n```mermaid\nsequenceDiagram\nA->>B: Hello\n```\n"
	blocks := ExtractMermaidBlocks(content)
	if len(blocks) != 2 {
		t.Fatalf("ExtractMermaidBlocks returned %d blocks, want 2", len(blocks))
	}
	if blocks[0] != "graph TD; A-->B" {
		t.Errorf("blocks[0] = %q, want %q", blocks[0], "graph TD; A-->B")
	}
}

func TestExtractMermaidBlocks_None(t *testing.T) {
	blocks := ExtractMermaidBlocks("no mermaid here")
	if len(blocks) != 0 {
		t.Errorf("ExtractMermaidBlocks returned %d blocks, want 0", len(blocks))
	}
}

// --- session.go: backward compat aliases ---

func TestNewCoworkerLoadEntry(t *testing.T) {
	entry := NewCoworkerLoadEntry("reviewer", "sonnet")
	if entry.CoworkerName != "reviewer" {
		t.Errorf("CoworkerName = %q, want reviewer", entry.CoworkerName)
	}
	if entry.CoworkerModel != "sonnet" {
		t.Errorf("CoworkerModel = %q, want sonnet", entry.CoworkerModel)
	}
	if entry.Type != SessionEntryTypeSystem {
		t.Errorf("Type = %q, want system", entry.Type)
	}
	if !containsSubstring(entry.Content, "reviewer") {
		t.Error("Content should mention coworker name")
	}
	if !containsSubstring(entry.Content, "sonnet") {
		t.Error("Content should mention model")
	}
}

func TestNewCoworkerLoadEntry_NoModel(t *testing.T) {
	entry := NewCoworkerLoadEntry("debugger", "")
	if containsSubstring(entry.Content, "model") {
		t.Error("Content should not mention model when empty")
	}
}

func TestBackwardCompatAliases(t *testing.T) {
	// these are trivial wrappers but at 0% they drag coverage down
	e := NewEntry(SessionEntryTypeUser, "hi")
	if e.Type != SessionEntryTypeUser {
		t.Errorf("NewEntry type = %q, want user", e.Type)
	}

	te := NewToolEntry("bash", "ls", "files")
	if te.ToolName != "bash" {
		t.Errorf("NewToolEntry tool = %q, want bash", te.ToolName)
	}

	ue := NewUserEntry("prompt")
	if ue.Type != SessionEntryTypeUser {
		t.Errorf("NewUserEntry type = %q, want user", ue.Type)
	}

	ae := NewAssistantEntry("response")
	if ae.Type != SessionEntryTypeAssistant {
		t.Errorf("NewAssistantEntry type = %q, want assistant", ae.Type)
	}

	se := NewSystemEntry("sys msg")
	if se.Type != SessionEntryTypeSystem {
		t.Errorf("NewSystemEntry type = %q, want system", se.Type)
	}
}

// --- store.go: ListSessions, BasePath, ResolveSessionName ---

func TestStore_BasePath(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	bp := store.BasePath()
	if bp == "" {
		t.Error("BasePath should not be empty")
	}
	if !containsSubstring(bp, "sessions") {
		t.Error("BasePath should contain 'sessions'")
	}
}

func TestStore_ListSessions_DefaultWindow(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessions, err := store.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestStore_ResolveSessionName_ExactMatch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// create a session directory
	sessDir := filepath.Join(store.BasePath(), "2026-03-15T10-00-user-OxABC")
	os.MkdirAll(sessDir, 0755)

	resolved, err := store.ResolveSessionName("2026-03-15T10-00-user-OxABC")
	if err != nil {
		t.Fatalf("ResolveSessionName: %v", err)
	}
	if resolved != "2026-03-15T10-00-user-OxABC" {
		t.Errorf("resolved = %q, want exact match", resolved)
	}
}

func TestStore_ResolveSessionName_SuffixMatch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// create session with raw.jsonl so it shows up in listing
	sessName := "2026-03-15T10-00-user-OxDEF"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte(`{"_meta":{}}`+"\n"), 0644)

	resolved, err := store.ResolveSessionName("OxDEF")
	if err != nil {
		t.Fatalf("ResolveSessionName: %v", err)
	}
	if resolved != sessName {
		t.Errorf("resolved = %q, want %q", resolved, sessName)
	}
}

func TestStore_ResolveSessionName_NoMatch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	resolved, err := store.ResolveSessionName("nonexistent")
	if err != nil {
		t.Fatalf("ResolveSessionName: %v", err)
	}
	// returns input as-is when no match
	if resolved != "nonexistent" {
		t.Errorf("resolved = %q, want input as-is", resolved)
	}
}

func TestStore_ResolveSessionName_Ambiguous(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// create two sessions with same suffix
	for _, name := range []string{
		"2026-03-15T10-00-user1-OxSAME",
		"2026-03-15T11-00-user2-OxSAME",
	} {
		sessDir := filepath.Join(store.BasePath(), name)
		os.MkdirAll(sessDir, 0755)
		os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte(`{"_meta":{}}`+"\n"), 0644)
	}

	_, err = store.ResolveSessionName("OxSAME")
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}
	if !containsSubstring(err.Error(), "ambiguous") {
		t.Errorf("error = %q, want to contain 'ambiguous'", err.Error())
	}
}

// --- summarize.go: BuildSummaryPrompt ---

func TestBuildSummaryPrompt(t *testing.T) {
	entries := []Entry{
		{Type: SessionEntryTypeUser, Content: "fix the bug"},
		{Type: SessionEntryTypeAssistant, Content: "I found the issue"},
	}

	prompt := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "/ledger/session-1")
	if prompt == "" {
		t.Fatal("BuildSummaryPrompt returned empty string")
	}
	// prompt must include the raw path so the LLM knows where to read
	if !strings.Contains(prompt, "/tmp/raw.jsonl") {
		t.Error("prompt should contain the raw file path")
	}
	// prompt should mention the entry count
	if !strings.Contains(prompt, "2 entries") {
		t.Error("prompt should include entry count")
	}
	// prompt should reference the ledger session dir for push-summary
	if !strings.Contains(prompt, "/ledger/session-1") {
		t.Error("prompt should reference ledger session dir")
	}
}

// --- store.go: ReadRawSession ---

func TestStore_ReadRawSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-tester-OxRaw"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)

	// write a valid raw.jsonl with meta + entries
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"OxRaw","started_at":"2026-03-15T10:00:00Z"}}
{"type":"user","content":"hello","ts":"2026-03-15T10:01:00Z"}
{"type":"assistant","content":"hi there","ts":"2026-03-15T10:02:00Z"}
`
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte(rawContent), 0644)

	stored, err := store.ReadRawSession(sessName)
	if err != nil {
		t.Fatalf("ReadRawSession: %v", err)
	}
	if stored == nil {
		t.Fatal("expected non-nil stored session")
	}
	if len(stored.Entries) < 2 {
		t.Errorf("expected at least 2 entries, got %d", len(stored.Entries))
	}
}

// --- history_store.go: LoadHistoryFromSession ---

func TestLoadHistoryFromSession_NoFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadHistoryFromSession(dir)
	if err == nil {
		t.Error("expected error for missing history file")
	}
}

func TestLoadHistoryFromSession_ValidFile(t *testing.T) {
	dir := t.TempDir()
	historyContent := `{"_meta":{"schema_version":"1","source":"agent_reconstruction","agent_id":"OxTest","agent_type":"claude-code","captured_at":"2026-03-15T10:00:00Z"}}
{"seq":1,"type":"user","content":"hello","ts":"2026-03-15T10:01:00Z"}
`
	os.WriteFile(filepath.Join(dir, "prior-history.jsonl"), []byte(historyContent), 0644)

	history, err := LoadHistoryFromSession(dir)
	if err != nil {
		t.Fatalf("LoadHistoryFromSession: %v", err)
	}
	if history == nil {
		t.Fatal("expected non-nil history")
	}
}

// --- redact_rules.go: RedactParseError.Error ---

func TestRedactParseError_Error(t *testing.T) {
	e := RedactParseError{
		Path:    "/tmp/REDACT.md",
		Line:    5,
		Message: "invalid pattern",
	}
	got := e.Error()
	want := "/tmp/REDACT.md:5: invalid pattern"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// --- signing.go ---

func TestVerifyRedactionSignature(t *testing.T) {
	// just exercise the function - it delegates to signing package
	result := VerifyRedactionSignature()
	// result may vary based on signing state, but should not panic
	_ = result
}

func TestIsRedactionSigned(t *testing.T) {
	// exercise the function
	_ = IsRedactionSigned()
}

// --- storage.go: List and Exists ---

func TestStore_List(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestStore_Exists_NotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if store.Exists("nonexistent") {
		t.Error("Exists should return false for missing session")
	}
}

func TestStore_Exists_Found(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// create a valid session
	sessName := "2026-03-15T10-00-user-OxExists"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"OxExists","started_at":"2026-03-15T10:00:00Z"}}
{"type":"user","content":"test","ts":"2026-03-15T10:01:00Z"}
`
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte(rawContent), 0644)

	if !store.Exists(sessName) {
		t.Error("Exists should return true for existing session")
	}
}

// --- store.go: IsSessionHydrated, CheckNeedsDownload ---

func TestStore_IsSessionHydrated_RawExists(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxHydrated"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte("data\n"), 0644)

	if !store.IsSessionHydrated(sessName) {
		t.Error("IsSessionHydrated should return true when raw.jsonl exists")
	}
}

func TestStore_IsSessionHydrated_NoFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxEmpty"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)

	if store.IsSessionHydrated(sessName) {
		t.Error("IsSessionHydrated should return false when no files exist")
	}
}

func TestStore_CheckNeedsDownload_NoSession(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	result := store.CheckNeedsDownload("nonexistent")
	if result != "" {
		t.Errorf("CheckNeedsDownload = %q, want empty for missing session", result)
	}
}

func TestStore_CheckNeedsDownload_NoMeta(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxNoMeta"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)

	result := store.CheckNeedsDownload(sessName)
	if result != "" {
		t.Errorf("CheckNeedsDownload = %q, want empty when no meta.json", result)
	}
}

func TestStore_CheckNeedsDownload_Hydrated(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxHyd"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)
	// meta.json must list the files that exist, and those files must not be LFS pointers
	metaJSON := `{"version":"1.0","session_name":"test","files":{"raw.jsonl":{"oid":"sha256:abc","size":8}}}`
	os.WriteFile(filepath.Join(sessDir, "meta.json"), []byte(metaJSON), 0644)
	os.WriteFile(filepath.Join(sessDir, "raw.jsonl"), []byte("content\n"), 0644)

	result := store.CheckNeedsDownload(sessName)
	if result != "" {
		t.Errorf("CheckNeedsDownload = %q, want empty for hydrated session", result)
	}
}

// --- store.go: ReadLFSSessionMeta ---

func TestStore_ReadLFSSessionMeta_NoMeta(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sessName := "2026-03-15T10-00-user-OxNoLFS"
	sessDir := filepath.Join(store.BasePath(), sessName)
	os.MkdirAll(sessDir, 0755)

	meta, err := store.ReadLFSSessionMeta(sessName)
	if err == nil && meta != nil {
		t.Error("expected nil meta or error for session without meta.json")
	}
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
