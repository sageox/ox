package html

import (
	"html/template"
	"testing"
)

func TestGroupIntoChapters_Empty(t *testing.T) {
	chapters := GroupIntoChapters(nil, nil)
	if chapters != nil {
		t.Errorf("expected nil for empty messages, got %d chapters", len(chapters))
	}
}

func TestGroupIntoChapters_AllConversation(t *testing.T) {
	messages := []MessageView{
		{ID: 1, Type: "user", Content: "Hello"},
		{ID: 2, Type: "assistant", Content: "Hi there"},
		{ID: 3, Type: "user", Content: "Next question"},
		{ID: 4, Type: "assistant", Content: "Answer"},
	}

	chapters := GroupIntoChapters(messages, nil)
	if len(chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(chapters))
	}

	// first chapter: user + assistant
	if len(chapters[0].Items) != 2 {
		t.Errorf("chapter 1: expected 2 items, got %d", len(chapters[0].Items))
	}
	if chapters[0].Items[0].IsWorkBlock {
		t.Error("chapter 1 item 0: expected conversation message, got work block")
	}

	// second chapter: user + assistant
	if len(chapters[1].Items) != 2 {
		t.Errorf("chapter 2: expected 2 items, got %d", len(chapters[1].Items))
	}
}

func TestGroupIntoChapters_ToolsGroupedIntoWorkBlocks(t *testing.T) {
	messages := []MessageView{
		{ID: 1, Type: "user", Content: "Fix the bug"},
		{ID: 2, Type: "assistant", Content: "I'll look into it"},
		{ID: 3, Type: "tool", ToolCall: &ToolCallView{Name: "Read"}},
		{ID: 4, Type: "tool", ToolCall: &ToolCallView{Name: "Read"}},
		{ID: 5, Type: "tool", ToolCall: &ToolCallView{Name: "Grep"}},
		{ID: 6, Type: "assistant", Content: "Found the issue"},
	}

	chapters := GroupIntoChapters(messages, nil)
	if len(chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(chapters))
	}

	items := chapters[0].Items
	// user, assistant, work block (3 tools), assistant
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	if items[2].IsWorkBlock != true {
		t.Error("item 2: expected work block")
	}
	if items[2].WorkBlock.TotalTools != 3 {
		t.Errorf("work block: expected 3 tools, got %d", items[2].WorkBlock.TotalTools)
	}
	if items[2].WorkBlock.ToolCounts["Read"] != 2 {
		t.Errorf("work block: expected Read=2, got %d", items[2].WorkBlock.ToolCounts["Read"])
	}
}

func TestGroupIntoChapters_SystemMessagesInWorkBlock(t *testing.T) {
	messages := []MessageView{
		{ID: 1, Type: "user", Content: "Start"},
		{ID: 2, Type: "system", Content: "System reminder"},
		{ID: 3, Type: "assistant", Content: "Response"},
	}

	chapters := GroupIntoChapters(messages, nil)
	if len(chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(chapters))
	}

	items := chapters[0].Items
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	// system message becomes a work block
	if !items[1].IsWorkBlock {
		t.Error("item 1: expected system message in work block")
	}
}

func TestGroupIntoChapters_LLMTitlesOverrideHeuristics(t *testing.T) {
	messages := []MessageView{
		{ID: 1, Type: "user", Content: "First question"},
		{ID: 2, Type: "assistant", Content: "Answer"},
		{ID: 3, Type: "user", Content: "Second question"},
		{ID: 4, Type: "assistant", Content: "Another answer"},
	}

	titles := []string{"Problem Discovery", "Deep Dive"}
	chapters := GroupIntoChapters(messages, titles)

	if len(chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(chapters))
	}
	if chapters[0].Title != "Problem Discovery" {
		t.Errorf("chapter 1 title: expected 'Problem Discovery', got '%s'", chapters[0].Title)
	}
	if chapters[1].Title != "Deep Dive" {
		t.Errorf("chapter 2 title: expected 'Deep Dive', got '%s'", chapters[1].Title)
	}
}

func TestGroupIntoChapters_PartialLLMTitles(t *testing.T) {
	messages := []MessageView{
		{ID: 1, Type: "user", Content: "Q1"},
		{ID: 2, Type: "assistant", Content: "A1"},
		{ID: 3, Type: "user", Content: "Q2"},
		{ID: 4, Type: "assistant", Content: "A2"},
		{ID: 5, Type: "user", Content: "Q3"},
		{ID: 6, Type: "assistant", Content: "A3"},
	}

	// only 1 title provided for 3 chapters
	titles := []string{"Setup"}
	chapters := GroupIntoChapters(messages, titles)

	if len(chapters) != 3 {
		t.Fatalf("expected 3 chapters, got %d", len(chapters))
	}
	if chapters[0].Title != "Setup" {
		t.Errorf("chapter 1 title: expected 'Setup', got '%s'", chapters[0].Title)
	}
	// chapters 2 and 3 should have heuristic titles
	if chapters[1].Title == "Setup" {
		t.Error("chapter 2 should not use LLM title")
	}
}

func TestGroupIntoChapters_EmptyContentAssistantInWorkBlock(t *testing.T) {
	// assistant messages with no content (just tool call wrappers) should go to work block
	messages := []MessageView{
		{ID: 1, Type: "user", Content: "Do something"},
		{ID: 2, Type: "assistant", Content: ""},
		{ID: 3, Type: "tool", ToolCall: &ToolCallView{Name: "Bash"}},
		{ID: 4, Type: "assistant", Content: "Done!"},
	}

	chapters := GroupIntoChapters(messages, nil)
	if len(chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(chapters))
	}

	items := chapters[0].Items
	// user, work block (empty assistant + tool), assistant
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if !items[1].IsWorkBlock {
		t.Error("item 1: expected work block for empty assistant + tool")
	}
}

func TestFormatWorkBlockSummary(t *testing.T) {
	tests := []struct {
		name     string
		wb       WorkBlockView
		expected string
	}{
		{
			name: "single tool",
			wb: WorkBlockView{
				TotalTools: 1,
				ToolCounts: map[string]int{"Read": 1},
			},
			expected: "1 tool call (Read 1)",
		},
		{
			name: "multiple tools",
			wb: WorkBlockView{
				TotalTools: 12,
				ToolCounts: map[string]int{"Read": 5, "Grep": 4, "Bash": 3},
			},
			expected: "12 tool calls (Read 5, Grep 4, Bash 3)",
		},
		{
			name: "more than 4 tool types",
			wb: WorkBlockView{
				TotalTools: 15,
				ToolCounts: map[string]int{"Read": 5, "Grep": 3, "Bash": 3, "Edit": 2, "Write": 1, "Glob": 1},
			},
			expected: "15 tool calls (Read 5, Bash 3, Grep 3, Edit 2, +2 more)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatWorkBlockSummary(&tt.wb)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestExtractFilesChanged(t *testing.T) {
	messages := []MessageView{
		{
			ID:   1,
			Type: "tool",
			ToolCall: &ToolCallView{
				Name:   "Edit",
				Input:  `{"file_path": "/Users/dev/project/internal/auth/refresh.go"}`,
				Output: "+added line 1\n+added line 2\n-removed line 1",
			},
		},
		{
			ID:   2,
			Type: "tool",
			ToolCall: &ToolCallView{
				Name:   "Write",
				Input:  `{"file_path": "/Users/dev/project/internal/auth/refresh_test.go"}`,
				Output: "+test line 1\n+test line 2\n+test line 3",
			},
		},
		{
			ID:   3,
			Type: "tool",
			ToolCall: &ToolCallView{
				Name:  "Read",
				Input: `{"file_path": "/Users/dev/project/internal/auth/client.go"}`,
			},
		},
	}

	files := ExtractFilesChanged(messages)
	if len(files) != 2 {
		t.Fatalf("expected 2 files changed, got %d", len(files))
	}

	// sorted by path
	if files[0].Path != "project/internal/auth/refresh.go" {
		t.Errorf("file 0: expected 'project/internal/auth/refresh.go', got '%s'", files[0].Path)
	}
	if files[0].Added != 2 || files[0].Removed != 1 {
		t.Errorf("file 0: expected +2/-1, got +%d/-%d", files[0].Added, files[0].Removed)
	}

	if files[1].Path != "project/internal/auth/refresh_test.go" {
		t.Errorf("file 1: expected 'project/internal/auth/refresh_test.go', got '%s'", files[1].Path)
	}
	if files[1].Added != 3 {
		t.Errorf("file 1: expected +3, got +%d", files[1].Added)
	}
}

func TestExtractFilesChanged_DuplicateFileAggregates(t *testing.T) {
	messages := []MessageView{
		{
			ID:   1,
			Type: "tool",
			ToolCall: &ToolCallView{
				Name:   "Edit",
				Input:  `{"file_path": "/Users/dev/project/main.go"}`,
				Output: "+line1\n-line2",
			},
		},
		{
			ID:   2,
			Type: "tool",
			ToolCall: &ToolCallView{
				Name:   "Edit",
				Input:  `{"file_path": "/Users/dev/project/main.go"}`,
				Output: "+line3\n+line4",
			},
		},
	}

	files := ExtractFilesChanged(messages)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (aggregated), got %d", len(files))
	}
	if files[0].Added != 3 || files[0].Removed != 1 {
		t.Errorf("expected +3/-1, got +%d/-%d", files[0].Added, files[0].Removed)
	}
}

func TestAutoChapterTitle_Exploration(t *testing.T) {
	items := []ChapterItem{
		{Message: &MessageView{Type: "user", Content: template.HTML("investigate")}},
		{IsWorkBlock: true, WorkBlock: &WorkBlockView{
			ToolCounts: map[string]int{"Read": 10, "Grep": 5},
			TotalTools: 15,
		}},
	}

	title := autoChapterTitle(items, 1)
	if title != "Exploration" {
		t.Errorf("expected 'Exploration', got '%s'", title)
	}
}

func TestAutoChapterTitle_Implementation(t *testing.T) {
	items := []ChapterItem{
		{Message: &MessageView{Type: "user", Content: template.HTML("implement")}},
		{IsWorkBlock: true, WorkBlock: &WorkBlockView{
			ToolCounts: map[string]int{"Edit": 8, "Read": 2, "Bash": 1},
			TotalTools: 11,
		}},
	}

	title := autoChapterTitle(items, 2)
	if title != "Implementation" {
		t.Errorf("expected 'Implementation', got '%s'", title)
	}
}

func TestAutoChapterTitle_PureConversation(t *testing.T) {
	items := []ChapterItem{
		{Message: &MessageView{Type: "user", Content: template.HTML("question")}},
		{Message: &MessageView{Type: "assistant", Content: template.HTML("answer")}},
	}

	title := autoChapterTitle(items, 1)
	if title != "Discussion" {
		t.Errorf("expected 'Discussion' for first chapter, got '%s'", title)
	}

	title = autoChapterTitle(items, 3)
	if title != "Turn 3" {
		t.Errorf("expected 'Turn 3' for non-first chapter, got '%s'", title)
	}
}
