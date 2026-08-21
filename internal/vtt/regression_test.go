package vtt

// Regression pins for the two existing callers of this package
// (cmd/ox/distill_discussions.go: Parse + FormatAsText;
// cmd/ox/agent_team_ctx.go: Parse + UniqueSpeakers).
// The timing extension must be purely additive: these tests pin the exact
// byte output those callers observed before Index/Start/End existed.
// Failure prevented: the timing extension silently changing distillation
// text or participant lists for existing team contexts.

import "testing"

// realisticVTT mirrors what recorded-discussion transcripts look like on
// disk: header, voice tags, cue identifiers, same-speaker runs, a malformed
// timestamp line, and cue settings after the end timestamp.
const realisticVTT = "WEBVTT\n" +
	"\n" +
	"1\n" +
	"00:00:00.000 --> 00:00:04.500\n" +
	"<v usr_alice>Welcome everyone to the sync.</v>\n" +
	"\n" +
	"2\n" +
	"00:00:04.500 --> 00:00:08.000\n" +
	"<v usr_alice>Let's start with the ledger work.</v>\n" +
	"\n" +
	"3\n" +
	"00:00:08.000 --> 00:00:12.250 align:start\n" +
	"<v usr_bob>I pushed the parser changes yesterday.</v>\n" +
	"\n" +
	"bogus --> stamp\n" +
	"<v usr_carol>My timestamp line is malformed.</v>\n" +
	"\n" +
	"00:01:00.000 --> 00:01:05.000\n" +
	"Untagged narration line\n" +
	"\n" +
	"00:01:05.000 --> 00:01:09.000\n" +
	"<v usr_bob>Multi line first</v>\n" +
	"<v usr_bob>and second.</v>\n"

// TestFormatAsTextByteIdentical pins the exact FormatAsText output for the
// distill-discussions caller path, including same-speaker merge behavior.
func TestFormatAsTextByteIdentical(t *testing.T) {
	cues, err := Parse([]byte(realisticVTT))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 6 {
		t.Fatalf("got %d cues, want 6 (cue delimiting must not change)", len(cues))
	}

	want := "usr_alice: Welcome everyone to the sync. Let's start with the ledger work.\n" +
		"usr_bob: I pushed the parser changes yesterday.\n" +
		"usr_carol: My timestamp line is malformed.\n" +
		"Untagged narration line\n" +
		"usr_bob: Multi line first and second."
	got := FormatAsText(cues)
	if got != want {
		t.Errorf("FormatAsText output drifted:\ngot:\n%q\nwant:\n%q", got, want)
	}
}

// TestUniqueSpeakersByteIdentical pins the participant extraction the
// agent-team-ctx caller relies on: distinct speakers in first-appearance order.
func TestUniqueSpeakersByteIdentical(t *testing.T) {
	cues, err := Parse([]byte(realisticVTT))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := UniqueSpeakers(cues)
	want := []string{"usr_alice", "usr_bob", "usr_carol"}
	if len(got) != len(want) {
		t.Fatalf("speakers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("speakers = %v, want %v", got, want)
		}
	}
}

// TestParseDelimitingUnchanged pins the pre-existing quirk that a timestamp
// line arriving while a cue is open (no blank separator) discards the
// accumulated text and starts a new cue without emitting the old one. The
// timing extension must not "fix" this — both callers' cue counts depend on it.
func TestParseDelimitingUnchanged(t *testing.T) {
	input := "WEBVTT\n" +
		"\n" +
		"00:00:00.000 --> 00:00:02.000\n" +
		"discarded text (no blank line before next timestamp)\n" +
		"00:00:02.000 --> 00:00:04.000\n" +
		"kept text\n"

	cues, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("got %d cues, want 1 (back-to-back timestamp lines discard the open cue)", len(cues))
	}
	if cues[0].Text != "kept text" {
		t.Errorf("text = %q, want %q", cues[0].Text, "kept text")
	}
	if cues[0].Index != 1 {
		t.Errorf("index = %d, want 1", cues[0].Index)
	}
}
