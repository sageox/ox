package attest

import (
	"testing"
	"time"
)

func TestParseStampComment(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantOK    bool
		wantDate  string
		wantRunID string
		wantEnv   string
	}{
		{
			name:      "the canonical form",
			line:      "    # validated: 2026-08-12 · Tilt · run_msqd0fj4-2223",
			wantOK:    true,
			wantDate:  "2026-08-12",
			wantRunID: "run_msqd0fj4-2223",
			wantEnv:   "Tilt",
		},
		{
			// Separator drift must not make a stamp unreadable — the date and the
			// run id are the only fields that carry meaning.
			name:      "hyphen separators still parse",
			line:      "# validated: 2026-08-13 - Tilt - run_msr1to7j-c67b",
			wantOK:    true,
			wantDate:  "2026-08-13",
			wantRunID: "run_msr1to7j-c67b",
		},
		{
			name:      "no environment field",
			line:      "  # validated: 2026-01-02 · run_abc123-ffff",
			wantOK:    true,
			wantDate:  "2026-01-02",
			wantRunID: "run_abc123-ffff",
		},
		{
			// A malformed run id must NOT be silently coerced into a valid one.
			// Reporting a stamp with no run id is the honest answer.
			name:     "malformed run id yields no run id, not a wrong one",
			line:     "# validated: 2026-08-12 · Tilt · msqd0fj4-2223",
			wantOK:   true,
			wantDate: "2026-08-12",
		},
		{
			// The whole point of returning ok=true here: a stamp we cannot read
			// is a finding, not a non-event. Returning false would let it fall
			// through to the generic-comment branch and disappear.
			name:   "unparseable body is still recognized as a stamp",
			line:   "# validated: someone said it works",
			wantOK: true,
		},
		{name: "an ordinary comment is not a stamp", line: "# validated stuff happened here"},
		{name: "a tag line is not a stamp", line: "  @validated"},
		{name: "a scenario line is not a stamp", line: "  Scenario: validated: things"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseStampComment(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Date != tc.wantDate {
				t.Errorf("Date = %q, want %q", got.Date, tc.wantDate)
			}
			if got.RunID != tc.wantRunID {
				t.Errorf("RunID = %q, want %q", got.RunID, tc.wantRunID)
			}
			if tc.wantEnv != "" && got.Env != tc.wantEnv {
				t.Errorf("Env = %q, want %q", got.Env, tc.wantEnv)
			}
			if got.Raw == "" {
				t.Error("Raw must always carry the comment text verbatim")
			}
		})
	}
}

func TestStampAge(t *testing.T) {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)

	s := &Stamp{Date: "2026-08-01"}
	age, ok := s.Age(now)
	if !ok {
		t.Fatal("a dated stamp must report an age")
	}
	if days := int(age.Hours() / 24); days != 12 {
		t.Errorf("age = %d days, want 12", days)
	}

	// Undated and old must not collapse into the same answer: one is a stale
	// claim, the other is a claim we cannot even age.
	for _, undated := range []*Stamp{nil, {Raw: "someone said it works"}, {Date: "not-a-date"}} {
		if _, ok := undated.Age(now); ok {
			t.Errorf("stamp %+v reported an age it cannot know", undated)
		}
	}
}

// A @validated tag with no provenance comment is a distinct, worse state than a
// stale stamp — it cannot be aged at all. It must stay separately countable.
func TestScanCorpus_ValidatedTagWithoutStampIsCountedSeparately(t *testing.T) {
	c := scan(t, map[string]string{
		"team-management/visibility.feature": `Feature: Visibility

  Rule: A member can view the restricted sections of their team
    @critical @validated
    # validated: 2026-08-12 · Tilt · run_msqd0fj4-2223
    Scenario: With provenance
      Given a member

    @critical @validated
    Scenario: Claimed with no date and no run id
      Given a member
`,
	})

	cap := c.Capabilities[0]
	if len(cap.Scenarios) != 2 {
		t.Fatalf("scenarios = %d, want 2", len(cap.Scenarios))
	}
	withProv, without := cap.Scenarios[0], cap.Scenarios[1]

	if !withProv.Validated || withProv.Stamp == nil {
		t.Errorf("first scenario: Validated=%v Stamp=%v, want tagged and stamped", withProv.Validated, withProv.Stamp)
	}
	if !without.Validated {
		t.Error("second scenario carries @validated and must be counted as a claim")
	}
	if without.Stamp != nil {
		t.Errorf("second scenario has no provenance comment, got Stamp=%+v", without.Stamp)
	}

	a := Assess(cap, &Plans{}, &Records{})
	if a.Stamped != 2 {
		t.Errorf("Stamped = %d, want 2 (both carry the tag)", a.Stamped)
	}
	if a.Unprovenanced != 1 {
		t.Errorf("Unprovenanced = %d, want 1", a.Unprovenanced)
	}
}

// The claim count must come from real tag lines only. A naive grep over the
// SageOx corpus returns 33 where the truth is 26, because the string appears
// inside explanatory prose — and over-counting the claim is the one direction
// this tool must never err in.
func TestScanCorpus_ValidatedInProseIsNotAClaim(t *testing.T) {
	c := scan(t, map[string]string{
		"sharing/links.feature": `Feature: Links

  # Deliberately: nothing here is tagged @validated until it runs on Tilt.
  Rule: Share links honor an expiry window
    # This scenario is not yet @validated — see the note above.
    Scenario: It expires
      Given a link
`,
	})

	cap := c.Capabilities[0]
	if cap.Scenarios[0].Validated {
		t.Fatal("@validated appearing in a comment was counted as a claim")
	}
	if a := Assess(cap, &Plans{}, &Records{}); a.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0", a.Stamped)
	}
}
