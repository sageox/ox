package consultscan

import (
	"reflect"
	"testing"
)

// TestLexShell pins the shell-word split the `ox` call matcher relies on:
// quotes are removed but keep spaces inside one word, backslashes escape, and
// every separator is its own word. Failure prevented: an argument split or
// joined wrongly, so a re-open of a surfaced item is missed or misattributed.
func TestLexShell(t *testing.T) {
	tests := []struct {
		cmd  string
		want []string
	}{
		{`ox query "a b"`, []string{"ox", "query", "a b"}},
		{`ox query 'it''s'`, []string{"ox", "query", "its"}},
		{`ox query "say \"hi\""`, []string{"ox", "query", `say "hi"`}},
		{`ox query a\ b`, []string{"ox", "query", "a b"}},
		{`a&&b||c;d|e&f`, []string{"a", "&&", "b", "||", "c", ";", "d", "|", "e", "&", "f"}},
		{"a\nb", []string{"a", "\n", "b"}},
		{`ox query "unterminated`, []string{"ox", "query", "unterminated"}},
		{`trailing\`, []string{`trailing\`}},
		{"  \t ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			var got []string
			for _, w := range lexShell(tt.cmd) {
				got = append(got, w.text)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("lexShell(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestFindOxCall covers the prefix matcher's boundaries directly.
func TestFindOxCall(t *testing.T) {
	tests := []struct {
		name, cmd, prefix string
		ok                bool
		args              []string
	}{
		{"plain", "ox plan view slug --json", "ox plan view", true, []string{"slug", "--json"}},
		{"cut at separator", "ox plan view slug | head", "ox plan view", true, []string{"slug"}},
		{"second call in chain", "ox status; ox plan view slug", "ox plan view", true, []string{"slug"}},
		{"agent id skipped once", "ox agent Ox1 team-ctx x", "ox agent team-ctx", true, []string{"x"}},
		{"two tokens between agent and subcommand", "ox agent Ox1 Ox2 team-ctx x", "ox agent team-ctx", false, nil},
		{"flag is not an agent id", "ox agent --json team-ctx", "ox agent team-ctx", false, nil},
		{"separator inside subcommand", "ox plan && view", "ox plan view", false, nil},
		{"command ends early", "ox plan", "ox plan view", false, nil},
		{"prefix not starting with ox", "git log", "git log", false, nil},
		{"empty prefix", "ox", "", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call, ok := findOxCall(tt.cmd, tt.prefix)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			var got []string
			for _, a := range call.args {
				got = append(got, a.text)
			}
			if !reflect.DeepEqual(got, tt.args) {
				t.Errorf("args = %q, want %q", got, tt.args)
			}
		})
	}
}
