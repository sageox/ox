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
						{"id": "ch-1", "title": "Intro", "time_range": [0, 60], "importance": 0.8},
						{"id": "ch-2", "title": "Details", "time_range": [60, 120], "importance": 0.6}
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

func TestLoadSummaryWithCategorizedFacts(t *testing.T) {
	tests := []struct {
		name          string
		json          string
		wantFacts     bool
		wantDecisions int
		wantLearnings int
		wantQuestions int
		wantActions   int
	}{
		{
			name: "full categorized facts",
			json: `{
				"schema_version": 2,
				"recording_id": "rec-1",
				"title": "Test",
				"human_summary": "summary",
				"decisions": [{"description": "use postgres"}, {"description": "deploy weekly"}],
				"learnings": [{"description": "caching helps"}],
				"open_questions": [{"question": "scale strategy?"}],
				"action_items": [{"description": "write tests"}],
				"constraints": ["team prefers Go"]
			}`,
			wantFacts:     true,
			wantDecisions: 2,
			wantLearnings: 1,
			wantQuestions: 1,
			wantActions:   1,
		},
		{
			name:      "no categorized facts",
			json:      `{"schema_version": 2, "recording_id": "rec-1", "title": "T", "human_summary": "s"}`,
			wantFacts: false,
		},
		{
			name: "empty arrays",
			json: `{
				"schema_version": 2,
				"recording_id": "rec-1",
				"title": "T",
				"human_summary": "s",
				"decisions": [],
				"learnings": []
			}`,
			wantFacts:     false,
			wantDecisions: 0,
			wantLearnings: 0,
		},
		{
			name: "partial fields",
			json: `{
				"schema_version": 2,
				"recording_id": "rec-1",
				"title": "T",
				"human_summary": "s",
				"decisions": [{"description": "only decisions here"}]
			}`,
			wantFacts:     true,
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

			if !tt.wantFacts {
				if got.HasCategorizedFacts() {
					t.Fatalf("expected no categorized facts, got decisions=%d learnings=%d",
						len(got.Decisions), len(got.Learnings))
				}
				return
			}

			if !got.HasCategorizedFacts() {
				t.Fatal("expected categorized facts to be present")
			}
			assertLen(t, "decisions", len(got.Decisions), tt.wantDecisions)
			assertLen(t, "learnings", len(got.Learnings), tt.wantLearnings)
			assertLen(t, "open_questions", len(got.OpenQuestions), tt.wantQuestions)
			assertLen(t, "action_items", len(got.ActionItems), tt.wantActions)
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
		mf   *discussion.KeyframesManifest
		want []string
	}{
		{
			name: "deduplicates and sorts",
			mf: &discussion.KeyframesManifest{
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
			mf:   &discussion.KeyframesManifest{Keyframes: []discussion.Keyframe{}},
			want: nil,
		},
		{
			name: "keyframes with no content_type",
			mf: &discussion.KeyframesManifest{
				Keyframes: []discussion.Keyframe{
					{S3Key: "a.png", ContentType: ""},
					{S3Key: "b.png"},
				},
			},
			want: nil,
		},
		{
			name: "single type",
			mf: &discussion.KeyframesManifest{
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
		mf        *discussion.KeyframesManifest
		chapterID string
		want      []string
	}{
		{
			name: "filters by chapter and sorts",
			mf: &discussion.KeyframesManifest{
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
			mf: &discussion.KeyframesManifest{
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
			mf: &discussion.KeyframesManifest{
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
			mf:        &discussion.KeyframesManifest{},
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
		{"question", discussion.AnnotationQuestion, "question"},
		{"tangent", discussion.AnnotationTangent, "tangent"},
		{"consensus", discussion.AnnotationConsensus, "consensus"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.constant != tt.want {
				t.Errorf("got %q, want %q", tt.constant, tt.want)
			}
		})
	}
}

func TestRichStructTextMethods(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"decision basic", discussion.Decision{Description: "use postgres"}.Text(), "use postgres"},
		{"decision with owner", discussion.Decision{Description: "use postgres", Owner: "Person A"}.Text(), "use postgres"},
		{"action basic", discussion.ActionItem{Description: "write tests"}.Text(), "write tests"},
		{"action with assignee", discussion.ActionItem{Description: "write tests", Assignee: "Person A"}.Text(), "write tests"},
		{"question basic", discussion.OpenQuestion{Question: "scale strategy?"}.Text(), "scale strategy?"},
		{"learning basic", discussion.Learning{Description: "caching helps"}.Text(), "caching helps"},
		{"requirement basic", discussion.Requirement{Description: "must support SSO"}.Text(), "must support SSO"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.text != tt.want {
				t.Errorf("got %q, want %q", tt.text, tt.want)
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
