package prheader

import (
	"testing"
)

func TestWhisperParts_orderSkipZeroPlural(t *testing.T) {
	tests := []struct {
		name string
		sig  Signals
		want []string
	}{
		{"no specific counts => empty", Signals{Material: true}, nil},
		{"prior art singular", Signals{PriorArt: 1, Material: true}, []string{"1 related session"}},
		{
			"both, order prior->collision",
			Signals{PriorArt: 2, Collisions: 1, Material: true},
			[]string{"2 related sessions", "1 concurrent edit flagged"},
		},
		{"collision plural", Signals{Collisions: 2, Material: true}, []string{"2 concurrent edits flagged"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := whisperParts(tc.sig)
			if len(got) != len(tc.want) {
				t.Fatalf("parts = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("part[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestWhisperAlt_plainSentence(t *testing.T) {
	got := whisperAlt(Signals{PriorArt: 2, Collisions: 1, Material: true})
	want := "2 related sessions · 1 concurrent edit flagged"
	if got != want {
		t.Errorf("alt = %q, want %q", got, want)
	}
}

// TestWhisperMarkup_atomicFragmentsBreakableBetween verifies each stat is atomic
// (internal spaces non-breaking) but the caption can wrap BETWEEN stats, so a
// narrow (mobile) PR view reflows instead of overflowing.
// Failure prevented: an all-non-breaking caption overflows the container on phones.
func TestWhisperMarkup_atomicFragmentsBreakableBetween(t *testing.T) {
	got := whisperMarkup(Signals{PriorArt: 2, Collisions: 1, Material: true})
	mustContain(t, got, "2&nbsp;related&nbsp;sessions", "1&nbsp;concurrent&nbsp;edit&nbsp;flagged", " &middot; ")
	// The brand attribution is NOT repeated in the stats — it lives in the kicker.
	mustNotContain(t, got, "Guided", "SageOx")
}

func TestPlural(t *testing.T) {
	if plural(1) != "" {
		t.Errorf("plural(1) = %q, want empty", plural(1))
	}
	for _, n := range []int{0, 2, 3, 10} {
		if plural(n) != "s" {
			t.Errorf("plural(%d) = %q, want \"s\"", n, plural(n))
		}
	}
}
