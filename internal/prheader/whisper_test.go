package prheader

import (
	"strings"
	"testing"
)

func TestWhisperParts_orderSkipZeroPlural(t *testing.T) {
	tests := []struct {
		name string
		sig  Signals
		want []string
	}{
		{"material only", Signals{Material: true}, []string{"Guided by SageOx"}},
		{"prior art singular", Signals{PriorArt: 1, Material: true}, []string{"Guided by SageOx", "1 prior art"}},
		{
			"all three, order prior->collision->route",
			Signals{PriorArt: 2, Collisions: 1, ExpertRoutes: 3, Material: true},
			[]string{"Guided by SageOx", "2 prior art", "1 collision", "3 expert routes"},
		},
		{"collision plural", Signals{Collisions: 2, Material: true}, []string{"Guided by SageOx", "2 collisions"}},
		{"expert route singular", Signals{ExpertRoutes: 1, Material: true}, []string{"Guided by SageOx", "1 expert route"}},
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
	want := "Guided by SageOx · 2 prior art · 1 collision"
	if got != want {
		t.Errorf("alt = %q, want %q", got, want)
	}
}

func TestWhisperMarkup_nonBreaking(t *testing.T) {
	got := whisperMarkup(Signals{PriorArt: 2, Material: true})
	// No raw spaces — every space is a non-breaking entity so the caption never
	// wraps mid-token.
	if strings.Contains(got, " ") {
		t.Errorf("whisper markup contains a raw space: %q", got)
	}
	mustContain(t, got, "Guided&nbsp;by&nbsp;SageOx", "2&nbsp;prior&nbsp;art", "&nbsp;&middot;&nbsp;")
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
