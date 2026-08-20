package read

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/conversation/format"
)

func transcriptData(t *testing.T, env *Envelope) *TranscriptData {
	t.Helper()
	if !env.Success {
		t.Fatalf("Transcript failed: %+v", env.Error)
	}
	return env.Data.(*TranscriptData)
}

func cueNumbers(data *TranscriptData) []int {
	var out []int
	for _, c := range data.Cues {
		out = append(out, c.N)
	}
	return out
}

func TestTranscriptCueRange(t *testing.T) {
	env := testReader(t).Transcript(fullCnv, TranscriptOptions{CueFirst: 5, CueLast: 6})
	data := transcriptData(t, env)
	if got := cueNumbers(data); len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("cues = %v, want [5 6]", got)
	}
	if data.Cues[0].Start != "00:00:21.000" || data.Cues[0].End != "00:00:27.250" {
		t.Errorf("cue 5 timing = %s-%s", data.Cues[0].Start, data.Cues[0].End)
	}
	if !strings.Contains(data.Cues[0].Text, "forward deployed engineer") {
		t.Errorf("cue 5 text = %q", data.Cues[0].Text)
	}
	if data.Cues[0].Speaker != "usr_9f8e7d6c5b4a39281706f5e4" {
		t.Errorf("speaker passes through opaque, got %q", data.Cues[0].Speaker)
	}
	if data.RevisionCurrent != 2 || data.Pinning != PinningUnpinned {
		t.Errorf("revision/pinning = %d/%s, want 2/unpinned", data.RevisionCurrent, data.Pinning)
	}
	if w := data.Window; len(w.Cues) != 2 || w.Cues[0] != 5 || w.Cues[1] != 6 || w.Truncated || w.Clamped {
		t.Errorf("window = %+v", w)
	}
}

func TestTranscriptCitationURIPinning(t *testing.T) {
	r := testReader(t)
	base := "sageox://" + fullCnv + "/" + fullClyr

	t.Run("matching revision is pinned", func(t *testing.T) {
		data := transcriptData(t, r.Transcript(base+"@2#cue=5-6", TranscriptOptions{}))
		if data.Pinning != PinningPinned || data.RevisionRequested != 2 || data.RevisionCurrent != 2 {
			t.Errorf("pinning = %+v", data)
		}
		if got := cueNumbers(data); len(got) != 2 || got[0] != 5 {
			t.Errorf("URI cue selector not honored: %v", got)
		}
	})
	t.Run("stale revision is served with revision_mismatch", func(t *testing.T) {
		env := r.Transcript(base+"@1#cue=5-6", TranscriptOptions{})
		data := transcriptData(t, env)
		if data.Pinning != PinningRevisionMismatch {
			t.Errorf("pinning = %s, want revision_mismatch", data.Pinning)
		}
		if len(data.Cues) != 2 {
			t.Errorf("D8: range must still be served, got %d cues", len(data.Cues))
		}
		if !hasWarningContaining(env.Warnings, "drifted") {
			t.Errorf("warnings = %v, want drift note", env.Warnings)
		}
	})
	t.Run("no revision pin is unpinned", func(t *testing.T) {
		data := transcriptData(t, r.Transcript(base+"#cue=1", TranscriptOptions{}))
		if data.Pinning != PinningUnpinned || data.RevisionRequested != 0 {
			t.Errorf("pinning = %+v", data)
		}
	})
	t.Run("explicit options win over URI selectors", func(t *testing.T) {
		data := transcriptData(t, r.Transcript(base+"@2#cue=1-2", TranscriptOptions{CueFirst: 3, CueLast: 3}))
		if got := cueNumbers(data); len(got) != 1 || got[0] != 3 {
			t.Errorf("cues = %v, want [3]", got)
		}
	})
}

func TestTranscriptTimeSelectors(t *testing.T) {
	r := testReader(t)
	t0, err := time.Parse(time.RFC3339, "2026-08-11T22:32:58Z")
	if err != nil {
		t.Fatal(err)
	}
	ms := func(offset time.Duration) int64 { return t0.Add(offset).UnixMilli() }

	t.Run("epoch-ms range maps through the recording clock", func(t *testing.T) {
		uri := fmt.Sprintf("sageox://%s#t=%d--%d", fullCnv, ms(9*time.Second), ms(15*time.Second))
		data := transcriptData(t, r.Transcript(uri, TranscriptOptions{}))
		// Window [9s,15s]: cue 3 is [9s,15.5s); cue 2 ends exactly at 9s
		// (exclusive) and cue 4 starts at 15.5s — both excluded.
		if got := cueNumbers(data); len(got) != 1 || got[0] != 3 {
			t.Fatalf("cues = %v, want [3]", got)
		}
	})
	t.Run("RFC 3339 spelling reads forever", func(t *testing.T) {
		uri := "sageox://" + fullCnv + "#t=2026-08-11T22:33:07Z--2026-08-11T22:33:13Z"
		data := transcriptData(t, r.Transcript(uri, TranscriptOptions{}))
		if got := cueNumbers(data); len(got) != 1 || got[0] != 3 {
			t.Fatalf("cues = %v, want [3]", got)
		}
	})
	t.Run("bare instant resolves to the containing cue", func(t *testing.T) {
		uri := fmt.Sprintf("sageox://%s#t=%d", fullCnv, ms(5*time.Second))
		data := transcriptData(t, r.Transcript(uri, TranscriptOptions{}))
		if got := cueNumbers(data); len(got) != 1 || got[0] != 2 {
			t.Fatalf("cues = %v, want [2]", got)
		}
	})
	t.Run("explicit media-clock window option", func(t *testing.T) {
		data := transcriptData(t, r.Transcript(fullCnv, TranscriptOptions{
			FromOffset: 0, ToOffset: 4 * time.Second, HasWindow: true,
		}))
		if got := cueNumbers(data); len(got) != 2 || got[0] != 1 || got[1] != 2 {
			t.Fatalf("cues = %v, want [1 2] (closed window touches cue 2 start)", got)
		}
	})
	t.Run("t selector without a recording clock falls back with a warning", func(t *testing.T) {
		uri := fmt.Sprintf("sageox://%s#t=%d", legacyCnv, ms(0))
		env := r.Transcript(uri, TranscriptOptions{})
		data := transcriptData(t, env)
		if len(data.Cues) != 2 {
			t.Errorf("fallback window cues = %v", cueNumbers(data))
		}
		if !hasWarningContaining(env.Warnings, "t= selector cannot be resolved") {
			t.Errorf("warnings = %v", env.Warnings)
		}
	})
}

func TestTranscriptDefaultAndFullWindows(t *testing.T) {
	r := testReader(t)

	t.Run("no selector serves the default window", func(t *testing.T) {
		data := transcriptData(t, r.Transcript(fullCnv, TranscriptOptions{}))
		if len(data.Cues) != 6 || data.Window.Truncated {
			t.Errorf("6-cue file: cues = %d truncated = %v", len(data.Cues), data.Window.Truncated)
		}
	})
	t.Run("default window truncates a long transcript", func(t *testing.T) {
		root := stageLongTranscript(t, 120)
		data := transcriptData(t, New(root, time.Time{}).Transcript(fullCnv, TranscriptOptions{}))
		if len(data.Cues) != DefaultCueWindow || !data.Window.Truncated {
			t.Errorf("120-cue file: cues = %d truncated = %v", len(data.Cues), data.Window.Truncated)
		}
	})
	t.Run("full serves everything", func(t *testing.T) {
		root := stageLongTranscript(t, 120)
		data := transcriptData(t, New(root, time.Time{}).Transcript(fullCnv, TranscriptOptions{Full: true}))
		if len(data.Cues) != 120 || data.Window.Truncated {
			t.Errorf("--full: cues = %d truncated = %v", len(data.Cues), data.Window.Truncated)
		}
	})
	t.Run("out-of-range cue range clamps and reports", func(t *testing.T) {
		data := transcriptData(t, r.Transcript(fullCnv, TranscriptOptions{CueFirst: 5, CueLast: 40}))
		if got := cueNumbers(data); len(got) != 2 || !data.Window.Clamped {
			t.Errorf("cues = %v clamped = %v", got, data.Window.Clamped)
		}
	})
}

func TestTranscriptBothManifestsWarning(t *testing.T) {
	env := testReader(t).Transcript(bothCnv, TranscriptOptions{})
	transcriptData(t, env)
	if !hasWarningContaining(env.Warnings, format.WarnBothManifestNames) {
		t.Fatalf("warnings = %v, want %q", env.Warnings, format.WarnBothManifestNames)
	}
}

func TestTranscriptTypedErrors(t *testing.T) {
	r := testReader(t)
	tests := []struct {
		name     string
		id       string
		opts     TranscriptOptions
		wantCode string
	}{
		{name: "missing transcript is not a bad id", id: noTranscriptCnv, wantCode: ErrCodeTranscriptNotAvailable},
		{name: "invalid id", id: "junk", wantCode: ErrCodeInvalidID},
		{name: "unindexed id", id: unknownCnv, wantCode: ErrCodeNotIndexed},
		{name: "reversed cue range", id: fullCnv, opts: TranscriptOptions{CueFirst: 6, CueLast: 5}, wantCode: ErrCodeInvalidSelector},
		{name: "zero cue ordinal", id: fullCnv, opts: TranscriptOptions{CueFirst: 0, CueLast: 5}, wantCode: ErrCodeInvalidSelector},
		{name: "reversed time window", id: fullCnv, opts: TranscriptOptions{FromOffset: time.Minute, ToOffset: 0, HasWindow: true}, wantCode: ErrCodeInvalidSelector},
		{
			name: "cue range and window are exclusive", id: fullCnv,
			opts:     TranscriptOptions{CueFirst: 1, CueLast: 2, FromOffset: 0, ToOffset: time.Minute, HasWindow: true},
			wantCode: ErrCodeInvalidSelector,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := r.Transcript(tt.id, tt.opts)
			if env.Success || env.Error == nil {
				t.Fatalf("Transcript(%q) succeeded, want %s", tt.id, tt.wantCode)
			}
			if env.Error.Code != tt.wantCode {
				t.Errorf("code = %s, want %s", env.Error.Code, tt.wantCode)
			}
		})
	}
}

func hasWarningContaining(warnings []string, sub string) bool {
	for _, w := range warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// stageLongTranscript builds a temp discussions root whose single indexed
// conversation carries n cues.
func stageLongTranscript(t *testing.T, n int) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "discussions")
	folder := filepath.Join(root, "2026-08-11-22-32-long")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	index := fmt.Sprintf(`[{"folder":"2026-08-11-22-32-long","recording_id":%q,"title":"Long","decision_count":0,"action_item_count":0,"has_keyframes":false}]`, fullRec)
	if err := os.WriteFile(filepath.Join(root, "INDEX.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("WEBVTT\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "\n%s --> %s\nCue number %d says something useful.\n",
			formatVTTTimestamp(time.Duration(i)*time.Second),
			formatVTTTimestamp(time.Duration(i+1)*time.Second), i+1)
	}
	if err := os.WriteFile(filepath.Join(folder, "transcript.vtt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestTranscriptNeverFollowsSymlinkedPayload verifies the transcript read
// goes through an os.Root over the discussion folder. Failure prevented: an
// otherwise-clean discussion folder committed into the customer-writable,
// git-synced team context carries a symlink at transcript.vtt pointing
// outside the discussions root — a bare os.ReadFile would follow it and
// serve arbitrary file content as cues (read-escape / exfiltration).
func TestTranscriptNeverFollowsSymlinkedPayload(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside.vtt")
	if err := os.WriteFile(outside, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nsecret outside content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "discussions")
	folder := filepath.Join(root, "2026-08-11-22-32-evil")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	index := fmt.Sprintf(`[{"folder":"2026-08-11-22-32-evil","recording_id":%q,"title":"Evil"}]`, fullRec)
	if err := os.WriteFile(filepath.Join(root, "INDEX.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(folder, format.TranscriptFileName)); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}

	env := New(root, time.Time{}).Transcript(fullCnv, TranscriptOptions{})
	if env.Success {
		t.Fatalf("symlinked transcript.vtt served: %+v", env.Data)
	}
	if env.Error.Code != ErrCodeReadError {
		t.Fatalf("code = %s, want %s (escape is a read failure, not a typed absence)", env.Error.Code, ErrCodeReadError)
	}
	if !env.Error.Retryable {
		t.Errorf("read_error must be retryable per the package contract")
	}
	if strings.Contains(env.Error.Message, "secret outside content") {
		t.Errorf("outside content leaked into the error message: %q", env.Error.Message)
	}
}
