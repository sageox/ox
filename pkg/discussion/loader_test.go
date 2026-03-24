package discussion_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/pkg/discussion"
)

func TestLoadSummary(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		wantNil   bool
		wantErr   bool
		wantChaps int
	}{
		{
			name: "valid summary",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "summary.json", `{
					"chapters": [
						{"id": "ch-1", "title": "Intro", "start_time": 0, "end_time": 60, "importance": 0.8},
						{"id": "ch-2", "title": "Details", "start_time": 60, "end_time": 120, "importance": 0.6}
					]
				}`)
			},
			wantChaps: 2,
		},
		{
			name:    "missing file returns nil",
			setup:   func(t *testing.T, dir string) {},
			wantNil: true,
		},
		{
			name: "malformed JSON returns error",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "summary.json", `{not valid json}`)
			},
			wantErr: true,
		},
		{
			name: "empty chapters array",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "summary.json", `{"chapters": []}`)
			},
			wantChaps: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			got, err := discussion.LoadSummary(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if len(got.Chapters) != tt.wantChaps {
				t.Errorf("chapters: got %d, want %d", len(got.Chapters), tt.wantChaps)
			}
		})
	}
}

func TestLoadSummaryWithAgentSummary(t *testing.T) {
	tests := []struct {
		name           string
		json           string
		wantAgent      bool
		wantDecisions  int
		wantLearnings  int
		wantQuestions  int
		wantActions    int
		wantKeyContext int
	}{
		{
			name: "full agent summary",
			json: `{
				"chapters": [],
				"agent_summary": {
					"decisions": ["use postgres", "deploy weekly"],
					"learnings": ["caching helps"],
					"open_questions": ["scale strategy?"],
					"action_items": ["write tests"],
					"key_context": ["team prefers Go"]
				}
			}`,
			wantAgent:      true,
			wantDecisions:  2,
			wantLearnings:  1,
			wantQuestions:  1,
			wantActions:    1,
			wantKeyContext: 1,
		},
		{
			name:      "no agent summary field",
			json:      `{"chapters": []}`,
			wantAgent: false,
		},
		{
			name: "agent summary with empty arrays",
			json: `{
				"chapters": [],
				"agent_summary": {
					"decisions": [],
					"learnings": []
				}
			}`,
			wantAgent:     true,
			wantDecisions: 0,
			wantLearnings: 0,
		},
		{
			name: "agent summary with partial fields",
			json: `{
				"chapters": [],
				"agent_summary": {
					"decisions": ["only decisions here"]
				}
			}`,
			wantAgent:     true,
			wantDecisions: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "summary.json", tt.json)

			got, err := discussion.LoadSummary(dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}

			if !tt.wantAgent {
				if got.AgentSummary != nil {
					t.Fatalf("expected nil AgentSummary, got %+v", got.AgentSummary)
				}
				return
			}

			if got.AgentSummary == nil {
				t.Fatal("expected non-nil AgentSummary")
			}
			assertLen(t, "decisions", len(got.AgentSummary.Decisions), tt.wantDecisions)
			assertLen(t, "learnings", len(got.AgentSummary.Learnings), tt.wantLearnings)
			assertLen(t, "open_questions", len(got.AgentSummary.OpenQuestions), tt.wantQuestions)
			assertLen(t, "action_items", len(got.AgentSummary.ActionItems), tt.wantActions)
			assertLen(t, "key_context", len(got.AgentSummary.KeyContext), tt.wantKeyContext)
		})
	}
}

func TestLoadKeyframes(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		wantNil bool
		wantErr bool
		wantLen int
	}{
		{
			name: "valid keyframes",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "keyframes.json", `{
					"keyframes": [
						{"s3_key": "frame-001.png", "timestamp_seconds": 10.5, "extraction_method": "scene-change", "chapter_id": "ch-1", "content_type": "diagram"},
						{"s3_key": "frame-002.png", "timestamp_seconds": 30.0, "extraction_method": "periodic", "chapter_id": "ch-1", "content_type": "code"}
					]
				}`)
			},
			wantLen: 2,
		},
		{
			name:    "missing file returns nil",
			setup:   func(t *testing.T, dir string) {},
			wantNil: true,
		},
		{
			name: "malformed JSON returns error",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "keyframes.json", `[broken`)
			},
			wantErr: true,
		},
		{
			name: "empty keyframes array",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "keyframes.json", `{"keyframes": []}`)
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			got, err := discussion.LoadKeyframes(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if len(got.Keyframes) != tt.wantLen {
				t.Errorf("keyframes: got %d, want %d", len(got.Keyframes), tt.wantLen)
			}
		})
	}
}

func TestLoadAnnotations(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		wantNil bool
		wantErr bool
		wantLen int
	}{
		{
			name: "valid annotations",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "annotations.json", `{
					"annotations": [
						{"type": "decision", "content": "use postgres", "chapter_id": "ch-1"},
						{"type": "action-item", "content": "write migration", "chapter_id": "ch-2"}
					]
				}`)
			},
			wantLen: 2,
		},
		{
			name:    "missing file returns nil",
			setup:   func(t *testing.T, dir string) {},
			wantNil: true,
		},
		{
			name: "malformed JSON returns error",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "annotations.json", `{"annotations": invalid}`)
			},
			wantErr: true,
		},
		{
			name: "empty annotations array",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "annotations.json", `{"annotations": []}`)
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			got, err := discussion.LoadAnnotations(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if len(got.Annotations) != tt.wantLen {
				t.Errorf("annotations: got %d, want %d", len(got.Annotations), tt.wantLen)
			}
		})
	}
}

func TestAllVisualTypes(t *testing.T) {
	tests := []struct {
		name string
		mf   *discussion.KeyframeManifest
		want []string
	}{
		{
			name: "deduplicates and sorts",
			mf: &discussion.KeyframeManifest{
				Keyframes: []discussion.Keyframe{
					{S3Key: "a.png", ContentType: "diagram"},
					{S3Key: "b.png", ContentType: "code"},
					{S3Key: "c.png", ContentType: "diagram"},
					{S3Key: "d.png", ContentType: "terminal"},
				},
			},
			want: []string{"code", "diagram", "terminal"},
		},
		{
			name: "nil manifest",
			mf:   nil,
			want: nil,
		},
		{
			name: "empty keyframes",
			mf:   &discussion.KeyframeManifest{Keyframes: []discussion.Keyframe{}},
			want: nil,
		},
		{
			name: "keyframes with no content_type",
			mf: &discussion.KeyframeManifest{
				Keyframes: []discussion.Keyframe{
					{S3Key: "a.png", ContentType: ""},
					{S3Key: "b.png"},
				},
			},
			want: nil,
		},
		{
			name: "single type",
			mf: &discussion.KeyframeManifest{
				Keyframes: []discussion.Keyframe{
					{S3Key: "a.png", ContentType: "slide"},
				},
			},
			want: []string{"slide"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := discussion.AllVisualTypes(tt.mf)
			assertStringSlice(t, got, tt.want)
		})
	}
}

func TestVisualTypes(t *testing.T) {
	tests := []struct {
		name      string
		mf        *discussion.KeyframeManifest
		chapterID string
		want      []string
	}{
		{
			name: "filters by chapter and sorts",
			mf: &discussion.KeyframeManifest{
				Keyframes: []discussion.Keyframe{
					{S3Key: "a.png", ChapterID: "ch-1", ContentType: "diagram"},
					{S3Key: "b.png", ChapterID: "ch-2", ContentType: "code"},
					{S3Key: "c.png", ChapterID: "ch-1", ContentType: "terminal"},
					{S3Key: "d.png", ChapterID: "ch-1", ContentType: "diagram"},
				},
			},
			chapterID: "ch-1",
			want:      []string{"diagram", "terminal"},
		},
		{
			name: "no matches for chapter",
			mf: &discussion.KeyframeManifest{
				Keyframes: []discussion.Keyframe{
					{S3Key: "a.png", ChapterID: "ch-1", ContentType: "diagram"},
				},
			},
			chapterID: "ch-99",
			want:      nil,
		},
		{
			name:      "nil manifest",
			mf:        nil,
			chapterID: "ch-1",
			want:      nil,
		},
		{
			name: "skips keyframes with empty content_type",
			mf: &discussion.KeyframeManifest{
				Keyframes: []discussion.Keyframe{
					{S3Key: "a.png", ChapterID: "ch-1", ContentType: ""},
					{S3Key: "b.png", ChapterID: "ch-1", ContentType: "code"},
				},
			},
			chapterID: "ch-1",
			want:      []string{"code"},
		},
		{
			name:      "empty manifest",
			mf:        &discussion.KeyframeManifest{},
			chapterID: "ch-1",
			want:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := discussion.VisualTypes(tt.mf, tt.chapterID)
			assertStringSlice(t, got, tt.want)
		})
	}
}

func TestAnnotationTypeConstants(t *testing.T) {
	tests := []struct {
		name     string
		constant string
		want     string
	}{
		{"decision", discussion.AnnotationDecision, "decision"},
		{"action-item", discussion.AnnotationActionItem, "action-item"},
		{"disagreement", discussion.AnnotationDisagreement, "disagreement"},
		{"insight", discussion.AnnotationInsight, "insight"},
		{"learning", discussion.AnnotationLearning, "learning"},
		{"open-question", discussion.AnnotationOpenQuestion, "open-question"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.constant != tt.want {
				t.Errorf("got %q, want %q", tt.constant, tt.want)
			}
		})
	}
}

// helpers

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func assertLen(t *testing.T, field string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %d, want %d", field, got, want)
	}
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
		return
	}
	if got == nil {
		t.Errorf("got nil, want %v", want)
		return
	}
	if len(got) != len(want) {
		t.Errorf("got %v (len %d), want %v (len %d)", got, len(got), want, len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}
