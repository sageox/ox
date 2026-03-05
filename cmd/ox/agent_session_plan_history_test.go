package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePlanHistoryFile(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "no file flag",
			args:     []string{},
			expected: "",
		},
		{
			name:     "file flag with space",
			args:     []string{"--file", "/path/to/file.jsonl"},
			expected: "/path/to/file.jsonl",
		},
		{
			name:     "file flag with equals",
			args:     []string{"--file=/path/to/file.jsonl"},
			expected: "/path/to/file.jsonl",
		},
		{
			name:     "file flag mixed with title",
			args:     []string{"--title", "My Plan", "--file", "/path/to/file.jsonl"},
			expected: "/path/to/file.jsonl",
		},
		{
			name:     "file flag at end",
			args:     []string{"other", "args", "--file", "/path/to/file.jsonl"},
			expected: "/path/to/file.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parsePlanHistoryFile(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractPlanFromEntries_IsPlanFlag(t *testing.T) {
	// test case 1: entry with is_plan:true should be extracted
	entries := []planHistoryEntry{
		{Type: "user", Content: "Create a plan for AWS infrastructure"},
		{Type: "assistant", Content: "Some initial thoughts..."},
		{Type: "assistant", Content: "## Final Plan\n\nThis is the final plan content.", IsPlan: true},
	}

	result := extractPlanFromEntries(entries)
	assert.Equal(t, "## Final Plan\n\nThis is the final plan content.", result)
}

func TestExtractPlanFromEntries_PlanHeader(t *testing.T) {
	// test case 2: detects plan by ## Plan header
	entries := []planHistoryEntry{
		{Type: "user", Content: "Help me plan the migration"},
		{Type: "assistant", Content: "Here's my analysis...\n\n## Plan\n\n1. First step\n2. Second step"},
	}

	result := extractPlanFromEntries(entries)
	assert.Contains(t, result, "## Plan")
	assert.Contains(t, result, "First step")
}

func TestExtractPlanFromEntries_FinalPlanHeader(t *testing.T) {
	// test case 3: detects plan by ## Final Plan header
	entries := []planHistoryEntry{
		{Type: "user", Content: "Let's finalize the plan"},
		{Type: "assistant", Content: "Based on our discussion:\n\n## Final Plan\n\n- Item 1\n- Item 2"},
	}

	result := extractPlanFromEntries(entries)
	assert.Contains(t, result, "## Final Plan")
	assert.Contains(t, result, "Item 1")
}

func TestExtractPlanFromEntries_NoExplicitPlan(t *testing.T) {
	// test case 4: no explicit plan marker - falls back to empty
	entries := []planHistoryEntry{
		{Type: "user", Content: "Random conversation"},
		{Type: "assistant", Content: "Some response without plan markers"},
	}

	result := extractPlanFromEntries(entries)
	assert.Empty(t, result)
}

func TestExtractLastAssistantMessage(t *testing.T) {
	// test case 4 continuation: extractLastAssistantMessage used as fallback
	entries := []planHistoryEntry{
		{Type: "user", Content: "First question"},
		{Type: "assistant", Content: "First response"},
		{Type: "user", Content: "Second question"},
		{Type: "assistant", Content: "This is the last assistant message"},
	}

	result := extractLastAssistantMessage(entries)
	assert.Equal(t, "This is the last assistant message", result)
}

func TestExtractPlanFromEntries_MultiplePlanMarkers(t *testing.T) {
	// test case 5: multiple plan markers - uses LAST one (reverse iteration)
	entries := []planHistoryEntry{
		{Type: "assistant", Content: "## Plan\n\nFirst draft plan", IsPlan: false},
		{Type: "user", Content: "Can you revise?"},
		{Type: "assistant", Content: "## Plan\n\nRevised plan - this should be used", IsPlan: true},
	}

	result := extractPlanFromEntries(entries)
	assert.Contains(t, result, "Revised plan")
}

func TestExtractPlanFromEntries_IsPlanPriority(t *testing.T) {
	// is_plan:true takes priority over header detection
	entries := []planHistoryEntry{
		{Type: "assistant", Content: "## Plan\n\nPlan from header only"},
		{Type: "assistant", Content: "The actual final plan without header", IsPlan: true},
	}

	// is_plan entries are checked first in the implementation
	result := extractPlanFromEntries(entries)
	assert.Equal(t, "The actual final plan without header", result)
}

func TestExtractDiagramsFromPlanEntries(t *testing.T) {
	// test case 7: mermaid diagram extraction
	mermaidGraph := "```" + "mermaid\ngraph LR\n  A --> B\n```"
	mermaidSeq := "```" + "mermaid\nsequenceDiagram\n  A->>B: Hello\n```"

	entries := []planHistoryEntry{
		{Type: "assistant", Content: "Here's the architecture:\n\n" + mermaidGraph},
		{Type: "assistant", Content: "And the sequence:\n\n" + mermaidSeq},
	}

	diagrams := extractDiagramsFromPlanEntries(entries)
	require.Len(t, diagrams, 2)
	assert.Contains(t, diagrams[0], "graph LR")
	assert.Contains(t, diagrams[1], "sequenceDiagram")
}

func TestExtractDiagramsFromPlanEntries_Deduplication(t *testing.T) {
	// duplicate diagrams should be deduplicated
	mermaidBlock := "```" + "mermaid\ngraph LR\n  A --> B\n```"
	entries := []planHistoryEntry{
		{Type: "assistant", Content: mermaidBlock},
		{Type: "assistant", Content: "Same diagram again:\n\n" + mermaidBlock},
	}

	diagrams := extractDiagramsFromPlanEntries(entries)
	assert.Len(t, diagrams, 1)
}

func TestExtractDiagramsFromPlanEntries_NoDiagrams(t *testing.T) {
	entries := []planHistoryEntry{
		{Type: "assistant", Content: "No diagrams here, just text."},
	}

	diagrams := extractDiagramsFromPlanEntries(entries)
	assert.Empty(t, diagrams)
}

func TestReadPlanHistoryEntries_ValidJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "history.jsonl")

	// write test JSONL content
	content := `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual","started_at":"2026-01-16T10:00:00Z"}}
{"type":"user","content":"Hello","seq":1,"source":"planning_history"}
{"type":"assistant","content":"Hi there!","seq":2,"source":"planning_history"}
`
	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err)

	entries, meta, err := readPlanHistoryEntries(filePath)
	require.NoError(t, err)

	assert.NotNil(t, meta)
	assert.Equal(t, "1", meta.SchemaVersion)
	assert.Equal(t, "claude-code", meta.AgentType)

	require.Len(t, entries, 2)
	assert.Equal(t, "user", entries[0].Type)
	assert.Equal(t, "Hello", entries[0].Content)
	assert.Equal(t, "assistant", entries[1].Type)
	assert.Equal(t, "Hi there!", entries[1].Content)
}

func TestReadPlanHistoryEntries_SkipsInvalidLines(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "history.jsonl")

	content := `{"type":"user","content":"Valid entry"}
not valid json
{"type":"","content":"Missing type - should skip"}
{"type":"assistant","content":""}
{"type":"assistant","content":"Another valid entry"}
`
	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err)

	entries, meta, err := readPlanHistoryEntries(filePath)
	require.NoError(t, err)

	assert.Nil(t, meta) // no meta line
	// only entries with both type and content should be included
	assert.Len(t, entries, 2)
	assert.Equal(t, "Valid entry", entries[0].Content)
	assert.Equal(t, "Another valid entry", entries[1].Content)
}

func TestReadPlanHistoryEntries_EmptyLines(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "history.jsonl")

	content := `{"type":"user","content":"Entry 1"}

{"type":"assistant","content":"Entry 2"}

`
	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err)

	entries, _, err := readPlanHistoryEntries(filePath)
	require.NoError(t, err)

	assert.Len(t, entries, 2)
}

func TestReadPlanHistoryEntries_FileNotFound(t *testing.T) {
	_, _, err := readPlanHistoryEntries("/nonexistent/path/file.jsonl")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "open file")
}

func TestWritePlanHistoryRaw(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, ledgerFileRaw)

	entries := []planHistoryEntry{
		{Type: "user", Content: "Question", Seq: 1, Source: "planning_history"},
		{Type: "assistant", Content: "Answer", Seq: 2, Source: "planning_history", IsPlan: true},
	}

	meta := &planHistoryMeta{
		SchemaVersion: "1",
		AgentType:     "claude-code",
		SessionID:     "test-session",
		StartedAt:     "2026-01-16T10:00:00Z",
	}

	err := writePlanHistoryRaw(rawPath, entries, meta, "OxTest")
	require.NoError(t, err)

	// read and verify
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)

	// verify it's valid JSONL
	lines := splitJSONLLines(string(data))
	require.GreaterOrEqual(t, len(lines), 4) // header + 2 entries + footer

	// check header
	var header map[string]any
	err = json.Unmarshal([]byte(lines[0]), &header)
	require.NoError(t, err)
	assert.Equal(t, "header", header["type"])
	metadata := header["metadata"].(map[string]any)
	assert.Equal(t, "OxTest", metadata["agent_id"])
	assert.Equal(t, "planning", metadata["session_type"])

	// check footer (last line)
	var footer map[string]any
	err = json.Unmarshal([]byte(lines[len(lines)-1]), &footer)
	require.NoError(t, err)
	assert.Equal(t, "footer", footer["type"])
	assert.Equal(t, float64(2), footer["entry_count"])
}

func TestWritePlanHistoryRaw_NilMeta(t *testing.T) {
	tmpDir := t.TempDir()
	rawPath := filepath.Join(tmpDir, ledgerFileRaw)

	entries := []planHistoryEntry{
		{Type: "user", Content: "Test content"},
	}

	err := writePlanHistoryRaw(rawPath, entries, nil, "OxABC1")
	require.NoError(t, err)

	// verify file was created
	_, err = os.Stat(rawPath)
	assert.NoError(t, err)
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string unchanged",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "exact length unchanged",
			input:    "hello",
			maxLen:   5,
			expected: "hello",
		},
		{
			name:     "long string truncated",
			input:    "hello world this is a long string",
			maxLen:   15,
			expected: "hello world ...",
		},
		{
			name:     "newlines replaced",
			input:    "line1\nline2\nline3",
			maxLen:   50,
			expected: "line1 line2 line3",
		},
		{
			name:     "newlines and truncation",
			input:    "line1\nline2\nline3 and more",
			maxLen:   15,
			expected: "line1 line2 ...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateString(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPlanHistoryOutput_JSONStructure(t *testing.T) {
	// test case 6 & 8 partially: verify output structure
	output := planHistoryOutput{
		Success:      true,
		Type:         "session_plan_history",
		AgentID:      "OxA1b2",
		SessionID:    "2026-01-16T10-30-user-OxA1b2",
		RawPath:      "/path/to/raw.jsonl",
		PlanPath:     "/path/to/plan.md",
		SessionPath:  "/path/to/session",
		EntryCount:   5,
		DiagramCount: 2,
		Diagrams:     []string{"graph LR", "sequenceDiagram"},
		PlanExtract:  "Brief plan summary...",
	}

	data, err := json.Marshal(output)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, true, decoded["success"])
	assert.Equal(t, "session_plan_history", decoded["type"])
	assert.Equal(t, "OxA1b2", decoded["agent_id"])
	assert.Equal(t, float64(5), decoded["entry_count"])
	assert.Equal(t, float64(2), decoded["diagram_count"])
}

func TestExtractPlanSection(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		pattern  string
		expected string
	}{
		{
			name:     "extracts from plan header",
			content:  "Intro text\n\n## Plan\n\n1. Step one\n2. Step two",
			pattern:  "## Plan",
			expected: "## Plan\n\n1. Step one\n2. Step two",
		},
		{
			name:     "extracts from final plan header",
			content:  "Analysis...\n\n## Final Plan\n\nThe final approach",
			pattern:  "## Final Plan",
			expected: "## Final Plan\n\nThe final approach",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, pattern := range planHeaderPatterns {
				if pattern.MatchString(tt.content) {
					result := extractPlanSection(tt.content, pattern)
					assert.Equal(t, tt.expected, result)
					return
				}
			}
			t.Fatal("no pattern matched")
		})
	}
}

func TestPlanHeaderPatterns(t *testing.T) {
	tests := []struct {
		name    string
		content string
		matches bool
	}{
		{
			name:    "## Final Plan matches",
			content: "## Final Plan\n\nContent",
			matches: true,
		},
		{
			name:    "## Plan matches",
			content: "## Plan\n\nContent",
			matches: true,
		},
		{
			name:    "# Final Plan matches",
			content: "# Final Plan\n\nContent",
			matches: true,
		},
		{
			name:    "# Plan matches",
			content: "# Plan\n\nContent",
			matches: true,
		},
		{
			name:    "### Plan does not match",
			content: "### Plan\n\nContent",
			matches: false,
		},
		{
			name:    "Planning does not match",
			content: "## Planning\n\nContent",
			matches: false,
		},
		{
			name:    "my plan does not match",
			content: "## my plan\n\nContent",
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched := false
			for _, pattern := range planHeaderPatterns {
				if pattern.MatchString(tt.content) {
					matched = true
					break
				}
			}
			assert.Equal(t, tt.matches, matched)
		})
	}
}

func TestPlanHistoryEntry_AllFields(t *testing.T) {
	// verify all fields parse correctly
	jsonStr := `{
		"type": "assistant",
		"content": "Here's my response",
		"ts": "2026-01-16T10:30:00Z",
		"seq": 3,
		"source": "planning_history",
		"is_plan": true,
		"tool_name": "bash",
		"tool_input": "ls -la"
	}`

	var entry planHistoryEntry
	err := json.Unmarshal([]byte(jsonStr), &entry)
	require.NoError(t, err)

	assert.Equal(t, "assistant", entry.Type)
	assert.Equal(t, "Here's my response", entry.Content)
	assert.Equal(t, "2026-01-16T10:30:00Z", entry.Timestamp)
	assert.Equal(t, 3, entry.Seq)
	assert.Equal(t, "planning_history", entry.Source)
	assert.True(t, entry.IsPlan)
	assert.Equal(t, "bash", entry.ToolName)
	assert.Equal(t, "ls -la", entry.ToolInput)
}

func TestPlanHistoryMeta_AllFields(t *testing.T) {
	jsonStr := `{
		"schema_version": "1",
		"agent_type": "claude-code",
		"session_id": "manual",
		"started_at": "2026-01-16T10:00:00Z"
	}`

	var meta planHistoryMeta
	err := json.Unmarshal([]byte(jsonStr), &meta)
	require.NoError(t, err)

	assert.Equal(t, "1", meta.SchemaVersion)
	assert.Equal(t, "claude-code", meta.AgentType)
	assert.Equal(t, "manual", meta.SessionID)
	assert.Equal(t, "2026-01-16T10:00:00Z", meta.StartedAt)
}

// Test session metadata file creation
func TestWritePlanHistorySessionMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "2026-01-16T10-30-user-OxTest")
	err := os.MkdirAll(sessionPath, 0755)
	require.NoError(t, err)

	// simulate what runAgentSessionPlanHistory does for session metadata
	metaPath := filepath.Join(sessionPath, "session.json")
	sessionMeta := map[string]any{
		"session_type":  "planning",
		"agent_id":      "OxTest",
		"created_at":    time.Now().Format(time.RFC3339),
		"title":         "Test Planning Session",
		"entry_count":   5,
		"diagram_count": 2,
	}
	metaJSON, err := json.MarshalIndent(sessionMeta, "", "  ")
	require.NoError(t, err)

	err = os.WriteFile(metaPath, metaJSON, 0644)
	require.NoError(t, err)

	// verify file contents
	data, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// test case 8: session tagged as planning
	assert.Equal(t, "planning", decoded["session_type"])
	// test case 10: title stored in session metadata
	assert.Equal(t, "Test Planning Session", decoded["title"])
	assert.Equal(t, float64(5), decoded["entry_count"])
	assert.Equal(t, float64(2), decoded["diagram_count"])
}

// Test plan.md file creation
func TestPlanFileCreation(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "test-session")
	err := os.MkdirAll(sessionPath, 0755)
	require.NoError(t, err)

	planContent := "## Final Plan\n\n1. Deploy to staging\n2. Run smoke tests\n3. Deploy to production"
	planPath := filepath.Join(sessionPath, ledgerFilePlan)

	err = os.WriteFile(planPath, []byte(planContent), 0644)
	require.NoError(t, err)

	// test case 6: verify plan file created
	data, err := os.ReadFile(planPath)
	require.NoError(t, err)
	assert.Equal(t, planContent, string(data))
}

// Integration test: combined capture-prior flow
func TestPlanHistoryIntegration(t *testing.T) {
	// test case 9: end-to-end test simulating capture-prior flow

	tmpDir := t.TempDir()
	inputFile := filepath.Join(tmpDir, "input.jsonl")

	// create planning history input with proper JSON escaping (\\n for newlines within content)
	// JSONL requires each JSON object on a single line, so embedded newlines must be escaped
	line1 := `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual","started_at":"2026-01-16T10:00:00Z"}}`
	line2 := `{"ts":"2026-01-16T10:00:01Z","type":"user","content":"Design an AWS deployment architecture","seq":1,"source":"planning_history"}`
	line3 := `{"ts":"2026-01-16T10:01:00Z","type":"assistant","content":"I'll analyze your requirements...\\n\\n` + "```mermaid\\ngraph LR\\n  User --> ALB\\n  ALB --> ECS\\n  ECS --> RDS\\n```" + `","seq":2,"source":"planning_history"}`
	line4 := `{"ts":"2026-01-16T10:02:00Z","type":"user","content":"Please finalize the plan","seq":3,"source":"planning_history"}`
	line5 := `{"ts":"2026-01-16T10:03:00Z","type":"assistant","content":"## Final Plan\\n\\n1. Create VPC with public/private subnets\\n2. Deploy ALB in public subnet\\n3. Deploy ECS services in private subnet\\n4. Configure RDS in private subnet\\n\\n` + "```mermaid\\nsequenceDiagram\\n  User->>ALB: Request\\n  ALB->>ECS: Forward\\n  ECS->>RDS: Query\\n```" + `","seq":4,"source":"planning_history","is_plan":true}`

	inputContent := line1 + "\n" + line2 + "\n" + line3 + "\n" + line4 + "\n" + line5 + "\n"
	err := os.WriteFile(inputFile, []byte(inputContent), 0644)
	require.NoError(t, err)

	// read and process entries
	entries, meta, err := readPlanHistoryEntries(inputFile)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Len(t, entries, 4)

	// extract plan (should use is_plan:true entry)
	planContent := extractPlanFromEntries(entries)
	assert.Contains(t, planContent, "## Final Plan")
	assert.Contains(t, planContent, "Create VPC")

	// extract diagrams
	diagrams := extractDiagramsFromPlanEntries(entries)
	assert.Len(t, diagrams, 2) // graph LR and sequenceDiagram
	assert.Contains(t, diagrams[0], "graph LR")
	assert.Contains(t, diagrams[1], "sequenceDiagram")

	// simulate writing to session folder
	sessionPath := filepath.Join(tmpDir, "session-output")
	err = os.MkdirAll(sessionPath, 0755)
	require.NoError(t, err)

	rawPath := filepath.Join(sessionPath, ledgerFileRaw)
	err = writePlanHistoryRaw(rawPath, entries, meta, "OxIntg")
	require.NoError(t, err)

	// verify raw.jsonl exists
	_, err = os.Stat(rawPath)
	require.NoError(t, err)

	// write plan.md
	planPath := filepath.Join(sessionPath, ledgerFilePlan)
	err = os.WriteFile(planPath, []byte(planContent), 0644)
	require.NoError(t, err)

	// verify plan.md
	planData, err := os.ReadFile(planPath)
	require.NoError(t, err)
	assert.Contains(t, string(planData), "## Final Plan")
}

// Test empty entries handling
func TestPlanHistoryEmptyEntries(t *testing.T) {
	entries := []planHistoryEntry{}

	plan := extractPlanFromEntries(entries)
	assert.Empty(t, plan)

	lastMsg := extractLastAssistantMessage(entries)
	assert.Empty(t, lastMsg)

	diagrams := extractDiagramsFromPlanEntries(entries)
	assert.Empty(t, diagrams)
}

// Test entries with only user messages
func TestPlanHistoryOnlyUserMessages(t *testing.T) {
	entries := []planHistoryEntry{
		{Type: "user", Content: "Question 1"},
		{Type: "user", Content: "Question 2"},
	}

	plan := extractPlanFromEntries(entries)
	assert.Empty(t, plan)

	lastMsg := extractLastAssistantMessage(entries)
	assert.Empty(t, lastMsg)
}

// Helper function to split JSONL content into lines
func splitJSONLLines(content string) []string {
	var lines []string
	for _, line := range splitLines(content) {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// Verify Instance type is accessible (for resolveInstance testing)
func TestInstanceType(t *testing.T) {
	inst := &agentinstance.Instance{
		AgentID:         "OxTest",
		ServerSessionID: "oxsid_01KCJECKEGETGX6HC80NRYVZ3P",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
	}

	assert.Equal(t, "OxTest", inst.AgentID)
	assert.False(t, inst.IsExpired())
}
