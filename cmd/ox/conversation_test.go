package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/conversation/read"
	"github.com/spf13/cobra"
)

// Fixture ids from internal/conversation/read/testdata/discussions.
const (
	convTestFullCnv   = "cnv_019ff2f5-2079-7be1-b05e-8caad2772e61"
	convTestFullRec   = "rec_019ff2f5-2079-7be1-b05e-8caad2772e61"
	convTestLegacyCnv = "cnv_019ff370-e195-7d1c-a727-39a1a85823f2"
	convTestTopicID   = "tp_01a012cb-9764-7555-a3f3-ce3377e47d98"
	convTestCueURI    = "sageox://" + convTestFullCnv + "/clyr_019ff2f5-deb5-77d3-b84b-04db14c601ca@2#cue=5-6"
	// Valid shape, absent from the fixture index.
	convTestUnknownCnv = "cnv_019f0000-0000-7000-8000-0000000000aa"
)

// convTestEnvelope decodes the wire envelope with the payload kept raw so
// each test asserts only the fields it owns.
type convTestEnvelope struct {
	Success       bool            `json:"success"`
	Data          json.RawMessage `json:"data"`
	Error         *read.Error     `json:"error"`
	Guidance      string          `json:"guidance"`
	Warnings      []string        `json:"warnings"`
	TokenEstimate int             `json:"token_estimate"`
	ElapsedMS     *int64          `json:"elapsed_ms"`
}

// useConversationTestReader points the command layer at the read package's
// fixture corpus for the test's duration (cwd of a cmd/ox test is cmd/ox).
func useConversationTestReader(t *testing.T) {
	t.Helper()
	orig := openConversationReader
	t.Cleanup(func() { openConversationReader = orig })
	openConversationReader = func() (*read.Reader, *read.Error) {
		return read.New("../../internal/conversation/read/testdata/discussions",
			time.Date(2026, 8, 20, 17, 41, 0, 0, time.UTC)), nil
	}
}

// resetConversationFlagSets zeroes every package-scoped conversation flag
// set so one test's parsed flags never bleed into the next in-process run.
func resetConversationFlagSets() {
	conversationListFlagSet = conversationListFlags{}
	conversationShowFlagSet = conversationFormatFlags{}
	conversationTranscriptFlagSet = conversationTranscriptFlags{}
	conversationTopicsFlagSet = conversationFormatFlags{}
	conversationTopicFlagSet = conversationTopicFlags{}
}

// runConversationInProc executes one conversation subcommand in-process on a
// fresh cobra command wired with the production RunE and flag binder, so the
// test surface cannot drift from the shipped one.
func runConversationInProc(t *testing.T, sub string, args ...string) (string, string, error) {
	t.Helper()
	resetConversationFlagSets()

	cmd := &cobra.Command{Use: sub, SilenceUsage: true, SilenceErrors: true, Args: cobra.ArbitraryArgs}
	switch sub {
	case "list":
		cmd.RunE = runConversationList
		registerConversationListFlags(cmd, &conversationListFlagSet)
	case "show":
		cmd.RunE = runConversationShow
		registerConversationFormatFlags(cmd, &conversationShowFlagSet)
	case "transcript":
		cmd.RunE = runConversationTranscript
		registerConversationTranscriptFlags(cmd, &conversationTranscriptFlagSet)
	case "topics":
		cmd.RunE = runConversationTopics
		registerConversationFormatFlags(cmd, &conversationTopicsFlagSet)
	case "topic":
		cmd.RunE = runConversationTopic
		registerConversationTopicFlags(cmd, &conversationTopicFlagSet)
	default:
		t.Fatalf("unknown subcommand %q", sub)
	}

	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

func decodeConvEnvelope(t *testing.T, stdout string) convTestEnvelope {
	t.Helper()
	var env convTestEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %v\n%s", err, stdout)
	}
	return env
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *distillHistoryExitError
	if !errors.As(err, &exit) {
		t.Fatalf("error is not the typed exit error: %v", err)
	}
	return exit.ExitCode
}

func TestConversationEnvelopeRoundTrip(t *testing.T) {
	useConversationTestReader(t)

	tests := []struct {
		name     string
		sub      string
		args     []string
		wantData string // substring of the marshaled data payload
	}{
		{"list", "list", nil, `"total_indexed":7`},
		{"show", "show", []string{convTestFullCnv}, `"available":true`},
		{"show by rec id", "show", []string{convTestFullRec}, `"conversation_id":"` + convTestFullCnv + `"`},
		{"transcript cue range", "transcript", []string{convTestFullCnv, "--cues", "2-4"}, `"cues":[2,4]`},
		{"transcript citation URI", "transcript", []string{convTestCueURI}, `"cues":[5,6]`},
		{"topics", "topics", []string{convTestFullCnv}, `"status":"draft"`},
		{"topic", "topic", []string{convTestFullCnv, convTestTopicID}, `"kind":"decision"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runConversationInProc(t, tt.sub, tt.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v\n%s", err, stdout)
			}
			env := decodeConvEnvelope(t, stdout)
			if !env.Success {
				t.Fatalf("success=false: %s", stdout)
			}
			if env.Error != nil {
				t.Fatalf("unexpected envelope error: %+v", env.Error)
			}
			if env.Guidance == "" {
				t.Error("guidance is empty; every rung names the next")
			}
			if env.TokenEstimate <= 0 {
				t.Error("token_estimate missing")
			}
			if env.ElapsedMS == nil {
				t.Error("elapsed_ms missing")
			}
			if !strings.Contains(string(env.Data), tt.wantData) {
				t.Errorf("data lacks %q:\n%s", tt.wantData, env.Data)
			}
			if strings.Count(stdout, "\n") != 1 {
				t.Errorf("stdout must be exactly one JSON line, got %q", stdout)
			}
		})
	}
}

func TestConversationUsageErrors(t *testing.T) {
	useConversationTestReader(t)

	tests := []struct {
		name     string
		sub      string
		args     []string
		wantCode string
	}{
		{"bad format", "list", []string{"--format", "yaml"}, "usage_error"},
		{"bad since", "list", []string{"--since", "yesterday"}, "usage_error"},
		{"show missing arg", "show", nil, "usage_error"},
		{"show extra args", "show", []string{convTestFullCnv, "extra"}, "usage_error"},
		{"topic one arg", "topic", []string{convTestFullCnv}, "usage_error"},
		{"cues malformed", "transcript", []string{convTestFullCnv, "--cues", "a-b"}, read.ErrCodeInvalidSelector},
		{"from without to", "transcript", []string{convTestFullCnv, "--from", "00:01"}, read.ErrCodeInvalidSelector},
		{"full with cues", "transcript", []string{convTestFullCnv, "--full", "--cues", "1-2"}, read.ErrCodeInvalidSelector},
		{"cues and window exclusive", "transcript", []string{convTestFullCnv, "--cues", "1-2", "--from", "00:01", "--to", "00:02"}, read.ErrCodeInvalidSelector},
		{"reversed cue range", "transcript", []string{convTestFullCnv, "--cues", "6-2"}, read.ErrCodeInvalidSelector},
		{"cue zero", "transcript", []string{convTestFullCnv, "--cues", "0"}, read.ErrCodeInvalidSelector},
		{"invalid id", "show", []string{"not-an-id"}, read.ErrCodeInvalidID},
		{"bare uuid rejected", "show", []string{"019ff2f5-2079-7be1-b05e-8caad2772e61"}, read.ErrCodeInvalidID},
		{"bad topic id", "topic", []string{convTestFullCnv, "hiring"}, read.ErrCodeInvalidID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runConversationInProc(t, tt.sub, tt.args...)
			if got := exitCodeOf(t, err); got != 2 {
				t.Fatalf("exit code = %d, want 2 (stdout %s)", got, stdout)
			}
			env := decodeConvEnvelope(t, stdout)
			if env.Success || env.Error == nil {
				t.Fatalf("expected error envelope, got %s", stdout)
			}
			if env.Error.Code != tt.wantCode {
				t.Errorf("error.code = %q, want %q", env.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestConversationRuntimeErrors(t *testing.T) {
	useConversationTestReader(t)

	tests := []struct {
		name     string
		sub      string
		args     []string
		wantCode string
	}{
		{"not indexed", "show", []string{convTestUnknownCnv}, read.ErrCodeNotIndexed},
		{"no distillation", "topics", []string{convTestLegacyCnv}, read.ErrCodeNoDistillation},
		{"topic not found", "topic", []string{convTestFullCnv, "tp_019f0000-0000-7000-8000-0000000000bb"}, read.ErrCodeTopicNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runConversationInProc(t, tt.sub, tt.args...)
			if got := exitCodeOf(t, err); got != 1 {
				t.Fatalf("exit code = %d, want 1 (stdout %s)", got, stdout)
			}
			env := decodeConvEnvelope(t, stdout)
			if env.Error == nil || env.Error.Code != tt.wantCode {
				t.Fatalf("error.code = %+v, want %q", env.Error, tt.wantCode)
			}
		})
	}
}

func TestConversationNoTeamContext(t *testing.T) {
	orig := openConversationReader
	t.Cleanup(func() { openConversationReader = orig })
	openConversationReader = func() (*read.Reader, *read.Error) {
		return nil, &read.Error{Code: read.ErrCodeNoTeamContext, Message: "no local team context"}
	}
	stdout, _, err := runConversationInProc(t, "list")
	if got := exitCodeOf(t, err); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
	env := decodeConvEnvelope(t, stdout)
	if env.Error == nil || env.Error.Code != read.ErrCodeNoTeamContext {
		t.Fatalf("error = %+v, want no_team_context", env.Error)
	}
}

func TestConversationTextRendering(t *testing.T) {
	useConversationTestReader(t)

	tests := []struct {
		name string
		sub  string
		args []string
		want []string
	}{
		{"list", "list", []string{"--text"}, []string{"Legacy Era Discussion", "indexed"}},
		{"list via --format", "list", []string{"--format", "text"}, []string{"Legacy Era Discussion"}},
		{"show", "show", []string{convTestFullCnv, "--text"}, []string{"id: " + convTestFullCnv}},
		{"transcript", "transcript", []string{convTestFullCnv, "--cues", "5-5", "--text"}, []string{"pinning=", "[5]"}},
		{"topics", "topics", []string{convTestFullCnv, "--text"}, []string{"episode=draft", convTestTopicID}},
		{"topic tombstones", "topic", []string{convTestFullCnv, convTestTopicID, "--include-superseded", "--text"}, []string{"superseded", "confidence"}},
		{"error is human line", "show", []string{convTestUnknownCnv, "--text"}, []string{"Error:", read.ErrCodeNotIndexed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, _ := runConversationInProc(t, tt.sub, tt.args...)
			if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
				t.Fatalf("--text produced JSON: %s", stdout)
			}
			for _, want := range tt.want {
				if !strings.Contains(stdout, want) {
					t.Errorf("text output lacks %q:\n%s", want, stdout)
				}
			}
		})
	}
}

func TestConversationCommandWiring(t *testing.T) {
	if conversationCmd.GroupID != "knowledge" {
		t.Errorf("GroupID = %q, want knowledge", conversationCmd.GroupID)
	}
	if len(conversationCmd.Aliases) != 1 || conversationCmd.Aliases[0] != "conv" {
		t.Errorf("aliases = %v, want [conv]", conversationCmd.Aliases)
	}
	if !conversationCmd.SilenceUsage || !conversationCmd.SilenceErrors {
		t.Error("parent must silence usage and errors")
	}
	if conversationCmd.RunE == nil {
		t.Error("bare `ox conversation` must run list")
	}
	subs := map[string]bool{}
	for _, c := range conversationCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, want := range []string{"list", "show", "transcript", "topics", "topic"} {
		if !subs[want] {
			t.Errorf("missing subcommand %q", want)
		}
	}
	if flag := conversationTranscriptCmd.Flags().Lookup("full"); flag == nil || !strings.Contains(flag.Usage, "humans") {
		t.Error("--full help text must say it is intended for humans")
	}
	found := false
	for _, c := range rootCmd.Commands() {
		if c == conversationCmd {
			found = true
		}
	}
	if !found {
		t.Error("conversationCmd not registered on rootCmd")
	}
}

func TestParseCueRange(t *testing.T) {
	tests := []struct {
		raw         string
		first, last int
		wantErr     bool
	}{
		{"12-16", 12, 16, false},
		{"7", 7, 7, false},
		{"a-b", 0, 0, true},
		{"1-", 0, 0, true},
		{"", 0, 0, true},
		{"1.5-2", 0, 0, true},
	}
	for _, tt := range tests {
		first, last, err := parseCueRange(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseCueRange(%q) err = %v, wantErr %v", tt.raw, err, tt.wantErr)
			continue
		}
		if err == nil && (first != tt.first || last != tt.last) {
			t.Errorf("parseCueRange(%q) = %d-%d, want %d-%d", tt.raw, first, last, tt.first, tt.last)
		}
	}
}

func TestParseMediaOffset(t *testing.T) {
	tests := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{"00:03:12.480", 3*time.Minute + 12*time.Second + 480*time.Millisecond, false},
		{"03:12", 3*time.Minute + 12*time.Second, false},
		{"3m12s", 3*time.Minute + 12*time.Second, false},
		{"90s", 90 * time.Second, false},
		{"-3s", 0, true},
		{"00:99:00", 0, true},
		{"1:2:3:4", 0, true},
		{"abc", 0, true},
		{"00:xx", 0, true},
	}
	for _, tt := range tests {
		got, err := parseMediaOffset(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseMediaOffset(%q) err = %v, wantErr %v", tt.raw, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("parseMediaOffset(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestParseConversationSince(t *testing.T) {
	tests := []struct {
		raw     string
		want    string // RFC3339 of the parsed instant; "" for zero
		wantErr bool
	}{
		{"", "", false},
		{"2026-08-13", "2026-08-13T00:00:00Z", false},
		{"2026-08-13T18:29:00Z", "2026-08-13T18:29:00Z", false},
		{"last tuesday", "", true},
	}
	for _, tt := range tests {
		got, err := parseConversationSince(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseConversationSince(%q) err = %v, wantErr %v", tt.raw, err, tt.wantErr)
			continue
		}
		if err == nil {
			gotStr := ""
			if !got.IsZero() {
				gotStr = got.UTC().Format(time.RFC3339)
			}
			if gotStr != tt.want {
				t.Errorf("parseConversationSince(%q) = %q, want %q", tt.raw, gotStr, tt.want)
			}
		}
	}
}
