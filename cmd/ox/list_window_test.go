package main

import (
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/errkind"
)

// TestListTimeBoundGrammar: --since and --until accept one grammar, and a bad
// value names the flag and shows a working example.
// Failure prevented: a list command rejects a form its siblings accept
// (`--since 7d` on one, only RFC3339 on the next), or a typo produces an
// error that does not say which flag or what to type instead.
func TestListTimeBoundGrammar(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		valid bool
	}{
		{"minutes", "30m", true},
		{"hours", "2h", true},
		{"days", "7d", true},
		{"weeks", "1w", true},
		{"date", "2026-10-01", true},
		{"rfc3339", "2026-10-01T12:00:00Z", true},
		{"empty means no bound", "", true},
		{"bare number", "5", false},
		{"prose", "yesterday", false},
		{"wrong unit", "3x", false},
	}
	for _, tt := range tests {
		for _, flag := range []string{"since", "until"} {
			t.Run(tt.name+" --"+flag, func(t *testing.T) {
				parse := parseListSince
				if flag == "until" {
					parse = parseListUntil
				}
				_, err := parse(tt.raw)
				if tt.valid {
					if err != nil {
						t.Fatalf("--%s %q rejected: %v", flag, tt.raw, err)
					}
					return
				}
				if err == nil {
					t.Fatalf("--%s %q accepted, want a usage error", flag, tt.raw)
				}
				if errkind.Of(err) != errkind.Usage {
					t.Errorf("error kind = %q, want usage", errkind.Of(err))
				}
				for _, want := range []string{"--" + flag, tt.raw, "2026-10-01"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q lacks %q (flag name, the bad value, and a working example)", err, want)
					}
				}
			})
		}
	}
}

// TestListUntilDateIncludesTheWholeDay: "until Oct 7" means through the 7th.
// Failure prevented: a bare-date --until drops every row from the day it names.
func TestListUntilDateIncludesTheWholeDay(t *testing.T) {
	got, err := parseListUntil("2026-10-07")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("--until 2026-10-07 = %v, want %v (exclusive bound after the whole day)", got, want)
	}

	// --since keeps the date's own midnight: "since Oct 7" starts the 7th
	since, err := parseListSince("2026-10-07")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 7, 0, 0, 0, 0, time.Local); !since.Equal(want) {
		t.Errorf("--since 2026-10-07 = %v, want %v", since, want)
	}
}

// TestResolveTimeWindowRejectsInvertedWindow: a window whose start is after
// its end is a usage error, not an empty result.
// Failure prevented: `--since 1d --until 7d` prints "no rows", which reads as
// "nothing happened" instead of "you swapped the bounds".
func TestResolveTimeWindowRejectsInvertedWindow(t *testing.T) {
	if _, _, err := resolveTimeWindow("7d", "1d"); err != nil {
		t.Fatalf("a correctly ordered window was rejected: %v", err)
	}
	_, _, err := resolveTimeWindow("1d", "7d")
	if err == nil {
		t.Fatal("inverted window accepted")
	}
	if errkind.Of(err) != errkind.Usage {
		t.Errorf("error kind = %q, want usage", errkind.Of(err))
	}
	if !strings.Contains(err.Error(), "--since") || !strings.Contains(err.Error(), "--until") {
		t.Errorf("error %q should name both flags", err)
	}
}

func TestInTimeWindow(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		t            time.Time
		since, until time.Time
		want         bool
	}{
		{"no bounds keeps everything", at, time.Time{}, time.Time{}, true},
		{"no bounds keeps an unknown instant", time.Time{}, time.Time{}, time.Time{}, true},
		{"since is inclusive", at, at, time.Time{}, true},
		{"before since", at, at.Add(time.Second), time.Time{}, false},
		{"until is exclusive", at, time.Time{}, at, false},
		{"before until", at, time.Time{}, at.Add(time.Second), true},
		{"inside both", at, at.Add(-time.Hour), at.Add(time.Hour), true},
		{"unknown instant cannot satisfy a bound", time.Time{}, at.Add(-time.Hour), time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inTimeWindow(tt.t, tt.since, tt.until); got != tt.want {
				t.Errorf("inTimeWindow = %v, want %v", got, tt.want)
			}
		})
	}
}
