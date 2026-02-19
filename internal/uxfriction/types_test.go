package uxfriction

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewFrictionEvent(t *testing.T) {
	tests := []struct {
		name string
		kind FailureKind
	}{
		{
			name: "unknown command failure",
			kind: FailureUnknownCommand,
		},
		{
			name: "unknown flag failure",
			kind: FailureUnknownFlag,
		},
		{
			name: "invalid arg failure",
			kind: FailureInvalidArg,
		},
		{
			name: "parse error failure",
			kind: FailureParseError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now().UTC().Truncate(time.Second)
			event := NewFrictionEvent(tt.kind)
			after := time.Now().UTC().Add(time.Second).Truncate(time.Second)

			if event == nil {
				t.Fatal("expected non-nil event")
			}

			if event.Kind != tt.kind {
				t.Errorf("kind = %q, want %q", event.Kind, tt.kind)
			}

			// verify timestamp is valid RFC3339
			parsed, err := time.Parse(time.RFC3339, event.Timestamp)
			if err != nil {
				t.Errorf("timestamp %q is not valid RFC3339: %v", event.Timestamp, err)
			}

			// verify timestamp is within expected range (truncated to second precision)
			if parsed.Before(before) || parsed.After(after) {
				t.Errorf("timestamp %v not within expected range [%v, %v]", parsed, before, after)
			}

			// verify other fields are zero values
			if event.Actor != "" {
				t.Errorf("actor = %q, want empty", event.Actor)
			}
			if event.AgentType != "" {
				t.Errorf("agent_type = %q, want empty", event.AgentType)
			}
			if event.Input != "" {
				t.Errorf("input = %q, want empty", event.Input)
			}
			if event.ErrorMsg != "" {
				t.Errorf("error_msg = %q, want empty", event.ErrorMsg)
			}
		})
	}
}

func TestFrictionEvent_MarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		event    *FrictionEvent
		wantKeys []string
	}{
		{
			name: "minimal event",
			event: &FrictionEvent{
				Timestamp: "2024-01-15T10:30:00Z",
				Kind:      "unknown-command",
			},
			wantKeys: []string{"ts", "kind", "actor", "input", "error_msg", "path_bucket"},
		},
		{
			name: "agent event with agent_type field",
			event: &FrictionEvent{
				Timestamp:  "2024-01-15T10:30:00Z",
				Kind:       "unknown-flag",
				Actor:      "agent",
				AgentType:  "claude-code",
				PathBucket: "repo",
				Input:      "ox agent prime --unknownflag",
				ErrorMsg:   "unknown flag: --unknownflag",
			},
			wantKeys: []string{"ts", "kind", "actor", "agent_type", "input", "error_msg", "path_bucket"},
		},
		{
			name: "human actor event (no agent_type field)",
			event: &FrictionEvent{
				Timestamp:  "2024-01-15T10:30:00Z",
				Kind:       "parse-error",
				Actor:      "human",
				PathBucket: "home",
				Input:      "ox login",
				ErrorMsg:   "required flag --token not provided",
			},
			wantKeys: []string{"ts", "kind", "actor", "input", "error_msg", "path_bucket"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.event.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON() error = %v", err)
			}

			// verify it's valid JSON
			var result map[string]any
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}

			// verify specific field values
			if result["ts"] != tt.event.Timestamp {
				t.Errorf("ts = %v, want %v", result["ts"], tt.event.Timestamp)
			}
			if result["kind"] != string(tt.event.Kind) {
				t.Errorf("kind = %v, want %v", result["kind"], tt.event.Kind)
			}
			if result["actor"] != tt.event.Actor {
				t.Errorf("actor = %v, want %v", result["actor"], tt.event.Actor)
			}

			// agent_type field should be omitted when empty
			if tt.event.AgentType == "" {
				if _, ok := result["agent_type"]; ok {
					t.Errorf("agent_type field should be omitted when empty")
				}
			} else {
				if result["agent_type"] != tt.event.AgentType {
					t.Errorf("agent_type = %v, want %v", result["agent_type"], tt.event.AgentType)
				}
			}
		})
	}
}

func TestFrictionEvent_MarshalJSON_Roundtrip(t *testing.T) {
	original := &FrictionEvent{
		Timestamp:  "2024-01-15T10:30:00Z",
		Kind:       "invalid-arg",
		Command:    "config",
		Subcommand: "set",
		Actor:      "agent",
		AgentType:  "cursor",
		PathBucket: "repo",
		Input:      "ox config set key",
		ErrorMsg:   "invalid argument format",
	}

	data, err := original.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}

	var restored FrictionEvent
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	// verify roundtrip
	if restored.Timestamp != original.Timestamp {
		t.Errorf("Timestamp = %q, want %q", restored.Timestamp, original.Timestamp)
	}
	if restored.Kind != original.Kind {
		t.Errorf("Kind = %q, want %q", restored.Kind, original.Kind)
	}
	if restored.Command != original.Command {
		t.Errorf("Command = %q, want %q", restored.Command, original.Command)
	}
	if restored.Subcommand != original.Subcommand {
		t.Errorf("Subcommand = %q, want %q", restored.Subcommand, original.Subcommand)
	}
	if restored.Actor != original.Actor {
		t.Errorf("Actor = %q, want %q", restored.Actor, original.Actor)
	}
	if restored.AgentType != original.AgentType {
		t.Errorf("AgentType = %q, want %q", restored.AgentType, original.AgentType)
	}
	if restored.PathBucket != original.PathBucket {
		t.Errorf("PathBucket = %q, want %q", restored.PathBucket, original.PathBucket)
	}
	if restored.Input != original.Input {
		t.Errorf("Input = %q, want %q", restored.Input, original.Input)
	}
	if restored.ErrorMsg != original.ErrorMsg {
		t.Errorf("ErrorMsg = %q, want %q", restored.ErrorMsg, original.ErrorMsg)
	}
}

func TestFrictionEvent_Truncate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		errorMsg  string
		wantInput int
		wantError int
	}{
		{
			name:      "short fields unchanged",
			input:     "ox agent prime",
			errorMsg:  "unknown command",
			wantInput: 14,
			wantError: 15,
		},
		{
			name:      "input truncated at 500",
			input:     strings.Repeat("x", 600),
			errorMsg:  "short error",
			wantInput: MaxInputLength,
			wantError: 11,
		},
		{
			name:      "error truncated at 200",
			input:     "short input",
			errorMsg:  strings.Repeat("e", 300),
			wantInput: 11,
			wantError: MaxErrorLength,
		},
		{
			name:      "both truncated",
			input:     strings.Repeat("i", 600),
			errorMsg:  strings.Repeat("e", 300),
			wantInput: MaxInputLength,
			wantError: MaxErrorLength,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &FrictionEvent{
				Input:    tt.input,
				ErrorMsg: tt.errorMsg,
			}
			event.Truncate()

			if len(event.Input) != tt.wantInput {
				t.Errorf("input length = %d, want %d", len(event.Input), tt.wantInput)
			}
			if len(event.ErrorMsg) != tt.wantError {
				t.Errorf("error_msg length = %d, want %d", len(event.ErrorMsg), tt.wantError)
			}
		})
	}
}

func TestFailureKind_Constants(t *testing.T) {
	// verify constant values match expected strings
	tests := []struct {
		kind FailureKind
		want string
	}{
		{FailureUnknownCommand, "unknown-command"},
		{FailureUnknownFlag, "unknown-flag"},
		{FailureMissingRequired, "missing-required"},
		{FailureInvalidArg, "invalid-arg"},
		{FailureParseError, "parse-error"},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if string(tt.kind) != tt.want {
				t.Errorf("FailureKind = %q, want %q", tt.kind, tt.want)
			}
		})
	}
}

func TestActor_Constants(t *testing.T) {
	// verify constant values match expected strings
	tests := []struct {
		actor Actor
		want  string
	}{
		{ActorHuman, "human"},
		{ActorAgent, "agent"},
		{ActorUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.actor), func(t *testing.T) {
			if string(tt.actor) != tt.want {
				t.Errorf("Actor = %q, want %q", tt.actor, tt.want)
			}
		})
	}
}

func TestSuggestionType_Constants(t *testing.T) {
	// verify constant values match expected strings
	tests := []struct {
		suggType SuggestionType
		want     string
	}{
		{SuggestionCommandRemap, "command-remap"},
		{SuggestionTokenFix, "token-fix"},
		{SuggestionLevenshtein, "levenshtein"},
	}

	for _, tt := range tests {
		t.Run(string(tt.suggType), func(t *testing.T) {
			if string(tt.suggType) != tt.want {
				t.Errorf("SuggestionType = %q, want %q", tt.suggType, tt.want)
			}
		})
	}
}

func TestMaxLengthConstants(t *testing.T) {
	if MaxInputLength != 500 {
		t.Errorf("MaxInputLength = %d, want 500", MaxInputLength)
	}
	if MaxErrorLength != 200 {
		t.Errorf("MaxErrorLength = %d, want 200", MaxErrorLength)
	}
}
