package uxfriction

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockCLIAdapter implements CLIAdapter for testing.
type mockCLIAdapter struct {
	commandNames []string
	flagNames    map[string][]string
	parsedError  *ParsedError
}

func (m *mockCLIAdapter) CommandNames() []string {
	return m.commandNames
}

func (m *mockCLIAdapter) FlagNames(command string) []string {
	if m.flagNames == nil {
		return nil
	}
	return m.flagNames[command]
}

func (m *mockCLIAdapter) ParseError(err error) *ParsedError {
	return m.parsedError
}

// mockCatalog is defined in chain_test.go

func TestNewHandler(t *testing.T) {
	tests := []struct {
		name    string
		adapter CLIAdapter
		catalog Catalog
	}{
		{
			name:    "creates handler with adapter and catalog",
			adapter: &mockCLIAdapter{},
			catalog: newMockCatalog(),
		},
		{
			name:    "creates handler with nil catalog",
			adapter: &mockCLIAdapter{},
			catalog: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewHandler(tt.adapter, tt.catalog)

			assert.NotNil(t, handler, "handler should not be nil")
			assert.NotNil(t, handler.engine, "engine should not be nil")
		})
	}
}

func TestHandler_Handle(t *testing.T) {
	tests := []struct {
		name              string
		args              []string
		err               error
		adapter           *mockCLIAdapter
		catalog           *mockCatalog
		wantNil           bool
		wantKind          FailureKind
		wantSuggestionSet bool
	}{
		{
			name: "returns nil for non-parseable error",
			args: []string{"ox", "invalid"},
			err:  errors.New("some error"),
			adapter: &mockCLIAdapter{
				parsedError: nil, // adapter cannot parse this error
			},
			catalog: newMockCatalog(),
			wantNil: true,
		},
		{
			name: "handles unknown command error",
			args: []string{"ox", "statu"},
			err:  errors.New("unknown command statu"),
			adapter: &mockCLIAdapter{
				parsedError: &ParsedError{
					Kind:       FailureUnknownCommand,
					BadToken:   "statu",
					Command:    "",
					RawMessage: "unknown command statu",
				},
				commandNames: []string{"status", "login", "logout", "agent"},
			},
			catalog:           newMockCatalog(),
			wantNil:           false,
			wantKind:          FailureUnknownCommand,
			wantSuggestionSet: true, // levenshtein should find "status"
		},
		{
			name: "handles unknown flag error",
			args: []string{"ox", "agent", "--verbos"},
			err:  errors.New("unknown flag --verbos"),
			adapter: &mockCLIAdapter{
				parsedError: &ParsedError{
					Kind:       FailureUnknownFlag,
					BadToken:   "--verbos",
					Command:    "agent",
					RawMessage: "unknown flag --verbos",
				},
				flagNames: map[string][]string{
					"agent": {"--verbose", "--help", "--version"},
				},
			},
			catalog:           newMockCatalog(),
			wantNil:           false,
			wantKind:          FailureUnknownFlag,
			wantSuggestionSet: true, // levenshtein should find "--verbose"
		},
		{
			name: "uses catalog command mapping when available",
			args: []string{"ox", "daemons", "list", "--every"},
			err:  errors.New("unknown flag"),
			adapter: &mockCLIAdapter{
				parsedError: &ParsedError{
					Kind:       FailureUnknownFlag,
					BadToken:   "--every",
					Command:    "daemons",
					RawMessage: "unknown flag --every",
				},
				commandNames: []string{"daemons"},
			},
			catalog: func() *mockCatalog {
				c := newMockCatalog()
				c.addCommand("daemons list --every", "daemons show --all", 0.95, "use 'show --all' instead")
				return c
			}(),
			wantNil:           false,
			wantKind:          FailureUnknownFlag,
			wantSuggestionSet: true,
		},
		{
			name: "handles missing required argument",
			args: []string{"ox", "agent", "prime"},
			err:  errors.New("missing required argument"),
			adapter: &mockCLIAdapter{
				parsedError: &ParsedError{
					Kind:       FailureMissingRequired,
					BadToken:   "",
					Command:    "agent",
					Subcommand: "prime",
					RawMessage: "missing required argument",
				},
				commandNames: []string{"agent"},
			},
			catalog:           newMockCatalog(),
			wantNil:           false,
			wantKind:          FailureMissingRequired,
			wantSuggestionSet: false, // no badtoken or valid options for levenshtein
		},
		{
			name: "uses token mapping from catalog",
			args: []string{"ox", "depliy"},
			err:  errors.New("unknown command depliy"),
			adapter: &mockCLIAdapter{
				parsedError: &ParsedError{
					Kind:       FailureUnknownCommand,
					BadToken:   "depliy",
					Command:    "",
					RawMessage: "unknown command depliy",
				},
				commandNames: []string{"deploy", "status"},
			},
			catalog: func() *mockCatalog {
				c := newMockCatalog()
				c.addToken("depliy", "deploy", FailureUnknownCommand, 0.9)
				return c
			}(),
			wantNil:           false,
			wantKind:          FailureUnknownCommand,
			wantSuggestionSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewHandler(tt.adapter, tt.catalog)
			result := handler.Handle(tt.args, tt.err)

			if tt.wantNil {
				assert.Nil(t, result, "result should be nil")
				return
			}

			require.NotNil(t, result, "result should not be nil")
			require.NotNil(t, result.Event, "event should not be nil")

			assert.Equal(t, tt.wantKind, result.Event.Kind, "kind mismatch")

			if tt.wantSuggestionSet {
				assert.NotNil(t, result.Suggestion, "suggestion should not be nil")
			}

			// verify event fields are populated
			assert.NotEmpty(t, result.Event.Timestamp, "timestamp should be set")
			assert.NotEmpty(t, result.Event.Input, "input should be set")
		})
	}
}

func TestDetectActor(t *testing.T) {
	tests := []struct {
		name          string
		setupEnv      func()
		teardownEnv   func()
		wantActor     Actor
		wantAgentType string
	}{
		{
			name: "detects CI environment as agent",
			setupEnv: func() {
				os.Setenv("CI", "true")
			},
			teardownEnv: func() {
				os.Unsetenv("CI")
			},
			wantActor:     ActorAgent,
			wantAgentType: "ci",
		},
		{
			name: "detects human when no agent or CI env",
			setupEnv: func() {
				// ensure CI is not set
				os.Unsetenv("CI")
				os.Unsetenv("CLAUDE_CODE")
				os.Unsetenv("AGENT_ENV")
			},
			teardownEnv: func() {},
			wantActor:   ActorHuman,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnv()
			defer tt.teardownEnv()

			actor, agentType := DetectActor()

			assert.Equal(t, tt.wantActor, actor, "actor mismatch")
			if tt.wantAgentType != "" {
				assert.Equal(t, tt.wantAgentType, agentType, "agent type mismatch")
			}
		})
	}
}

func TestFormatSuggestion(t *testing.T) {
	tests := []struct {
		name       string
		suggestion *Suggestion
		jsonMode   bool
		want       string
		wantJSON   map[string]any
	}{
		{
			name:       "nil suggestion returns empty string",
			suggestion: nil,
			jsonMode:   false,
			want:       "",
		},
		{
			name:       "nil suggestion returns empty string in json mode",
			suggestion: nil,
			jsonMode:   true,
			want:       "",
		},
		{
			name: "human format without description",
			suggestion: &Suggestion{
				Type:       SuggestionLevenshtein,
				Original:   "statu",
				Corrected:  "status",
				Confidence: 0.8,
			},
			jsonMode: false,
			want:     "Did you mean this?\n    status",
		},
		{
			name: "human format with description",
			suggestion: &Suggestion{
				Type:        SuggestionCommandRemap,
				Original:    "daemons list --every",
				Corrected:   "daemons show --all",
				Confidence:  0.95,
				Description: "use show --all instead",
			},
			jsonMode: false,
			want:     "Did you mean this?\n    daemons show --all",
		},
		{
			name: "json format without description",
			suggestion: &Suggestion{
				Type:       SuggestionLevenshtein,
				Original:   "statu",
				Corrected:  "status",
				Confidence: 0.8,
			},
			jsonMode: true,
			wantJSON: map[string]any{
				"type":       "levenshtein",
				"suggestion": "status",
				"confidence": 0.8,
			},
		},
		{
			name: "json format with description",
			suggestion: &Suggestion{
				Type:        SuggestionCommandRemap,
				Original:    "daemons list --every",
				Corrected:   "daemons show --all",
				Confidence:  0.95,
				Description: "use show --all instead",
			},
			jsonMode: true,
			wantJSON: map[string]any{
				"type":        "command-remap",
				"suggestion":  "daemons show --all",
				"confidence":  0.95,
				"description": "use show --all instead",
			},
		},
		{
			name: "json format with token fix type",
			suggestion: &Suggestion{
				Type:       SuggestionTokenFix,
				Original:   "depliy",
				Corrected:  "deploy",
				Confidence: 0.9,
			},
			jsonMode: true,
			wantJSON: map[string]any{
				"type":       "token-fix",
				"suggestion": "deploy",
				"confidence": 0.9,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatSuggestion(tt.suggestion, tt.jsonMode)

			if tt.jsonMode && tt.wantJSON != nil {
				// parse and compare JSON
				var got map[string]any
				err := json.Unmarshal([]byte(result), &got)
				require.NoError(t, err, "result should be valid JSON")
				assert.Equal(t, tt.wantJSON, got, "JSON mismatch")
			} else {
				assert.Equal(t, tt.want, result, "format mismatch")
			}
		})
	}
}

func TestHandler_Handle_BuildsFrictionEventCorrectly(t *testing.T) {
	adapter := &mockCLIAdapter{
		parsedError: &ParsedError{
			Kind:       FailureUnknownCommand,
			BadToken:   "statu",
			Command:    "ox",
			Subcommand: "",
			RawMessage: "unknown command 'statu' for 'ox'",
		},
		commandNames: []string{"status", "login", "agent"},
	}
	catalog := newMockCatalog()

	handler := NewHandler(adapter, catalog)
	result := handler.Handle([]string{"ox", "statu"}, errors.New("unknown command"))

	require.NotNil(t, result)
	require.NotNil(t, result.Event)

	event := result.Event

	// verify FrictionEvent fields are populated correctly
	assert.NotEmpty(t, event.Timestamp, "timestamp should be set")
	assert.Equal(t, FailureUnknownCommand, event.Kind, "kind should match parsed error")
	assert.NotEmpty(t, event.Actor, "actor should be set")
	assert.Contains(t, event.Input, "ox statu", "input should contain original args")
	assert.Contains(t, event.ErrorMsg, "unknown command", "error_msg should contain raw message")

	// suggestion should be set from levenshtein match
	assert.Equal(t, "status", result.Suggestion.Corrected, "suggestion should be 'status'")
}

func TestHandler_Handle_NoSuggestionWhenNoMatch(t *testing.T) {
	adapter := &mockCLIAdapter{
		parsedError: &ParsedError{
			Kind:       FailureUnknownCommand,
			BadToken:   "xyzabc", // completely unrelated
			Command:    "",
			RawMessage: "unknown command xyzabc",
		},
		commandNames: []string{"status", "login", "agent"},
	}
	catalog := newMockCatalog() // no mappings

	handler := NewHandler(adapter, catalog)
	result := handler.Handle([]string{"ox", "xyzabc"}, errors.New("unknown command"))

	require.NotNil(t, result, "result should not be nil")
	require.NotNil(t, result.Event, "event should not be nil")

	// levenshtein distance from "xyzabc" to any command is > 2 (maxDist)
	assert.Nil(t, result.Suggestion, "suggestion should be nil for unmatched input")
}

func TestFormatSuggestion_AllSuggestionTypes(t *testing.T) {
	suggestionTypes := []SuggestionType{
		SuggestionCommandRemap,
		SuggestionTokenFix,
		SuggestionLevenshtein,
	}

	for _, st := range suggestionTypes {
		t.Run(string(st), func(t *testing.T) {
			suggestion := &Suggestion{
				Type:       st,
				Original:   "original",
				Corrected:  "corrected",
				Confidence: 0.85,
			}

			// human format
			humanResult := FormatSuggestion(suggestion, false)
			assert.Contains(t, humanResult, "corrected", "human format should contain corrected value")
			assert.Contains(t, humanResult, "Did you mean", "human format should contain prompt")

			// json format
			jsonResult := FormatSuggestion(suggestion, true)
			var parsed map[string]any
			err := json.Unmarshal([]byte(jsonResult), &parsed)
			require.NoError(t, err, "should be valid JSON")
			assert.Equal(t, string(st), parsed["type"], "type should match")
			assert.Equal(t, "corrected", parsed["suggestion"], "suggestion should match")
			assert.Equal(t, 0.85, parsed["confidence"], "confidence should match")
		})
	}
}

func TestHandler_Handle_ActorAndAgentTypePopulated(t *testing.T) {
	// save and restore CI env
	originalCI := os.Getenv("CI")
	defer func() {
		if originalCI == "" {
			os.Unsetenv("CI")
		} else {
			os.Setenv("CI", originalCI)
		}
	}()

	os.Setenv("CI", "true")

	adapter := &mockCLIAdapter{
		parsedError: &ParsedError{
			Kind:       FailureUnknownCommand,
			BadToken:   "test",
			Command:    "",
			RawMessage: "unknown command test",
		},
		commandNames: []string{"status"},
	}
	catalog := newMockCatalog()

	handler := NewHandler(adapter, catalog)
	result := handler.Handle([]string{"ox", "test"}, errors.New("unknown command"))

	require.NotNil(t, result)
	require.NotNil(t, result.Event)

	// CI env should result in agent actor
	assert.Equal(t, string(ActorAgent), result.Event.Actor, "actor should be agent in CI")
	assert.Equal(t, "ci", result.Event.AgentType, "agent_type should be ci")
}

func TestHandler_Handle_EmptyArgs(t *testing.T) {
	adapter := &mockCLIAdapter{
		parsedError: &ParsedError{
			Kind:       FailureUnknownCommand,
			BadToken:   "",
			Command:    "",
			RawMessage: "no command specified",
		},
		commandNames: []string{"status", "login"},
	}
	catalog := newMockCatalog()

	handler := NewHandler(adapter, catalog)
	result := handler.Handle([]string{}, errors.New("no command"))

	require.NotNil(t, result, "result should not be nil even with empty args")
	require.NotNil(t, result.Event, "event should not be nil")

	// with empty args and no bad token, no suggestion expected
	assert.Nil(t, result.Suggestion, "suggestion should be nil with empty args")
}

func TestHandler_Handle_SpecialCharactersInArgs(t *testing.T) {
	adapter := &mockCLIAdapter{
		parsedError: &ParsedError{
			Kind:       FailureUnknownCommand,
			BadToken:   "test--cmd",
			Command:    "",
			RawMessage: "unknown command test--cmd",
		},
		commandNames: []string{"test-cmd", "test"},
	}
	catalog := newMockCatalog()

	handler := NewHandler(adapter, catalog)
	result := handler.Handle([]string{"ox", "test--cmd", "--flag=value"}, errors.New("unknown"))

	require.NotNil(t, result)
	require.NotNil(t, result.Event)

	// input should be properly joined and present
	assert.Contains(t, result.Event.Input, "test--cmd", "input should contain args with special chars")
	assert.Contains(t, result.Event.Input, "--flag=value", "input should contain flag with value")
}

func TestNewHandler_EngineConfigured(t *testing.T) {
	adapter := &mockCLIAdapter{}
	catalog := newMockCatalog()

	handler := NewHandler(adapter, catalog)

	// verify engine is properly configured
	assert.NotNil(t, handler.engine, "engine should be created")
	assert.NotNil(t, handler.engine.levenshtein, "levenshtein suggester should be created")
}

func TestHandler_Handle_SuggestionPriority(t *testing.T) {
	// when both command mapping and levenshtein would match,
	// command mapping should take priority
	adapter := &mockCLIAdapter{
		parsedError: &ParsedError{
			Kind:       FailureUnknownCommand,
			BadToken:   "statu",
			Command:    "",
			RawMessage: "unknown command statu",
		},
		commandNames: []string{"status"}, // levenshtein would find "status"
	}

	catalog := func() *mockCatalog {
		c := newMockCatalog()
		// catalog has a different suggestion
		c.addCommand("statu", "status --verbose", 0.95, "common pattern")
		return c
	}()

	handler := NewHandler(adapter, catalog)
	result := handler.Handle([]string{"ox", "statu"}, errors.New("unknown command"))

	require.NotNil(t, result)
	require.NotNil(t, result.Suggestion)

	// catalog command mapping should win over levenshtein
	assert.Equal(t, SuggestionCommandRemap, result.Suggestion.Type, "command remap should take priority")
	assert.Equal(t, "status --verbose", result.Suggestion.Corrected, "should use catalog suggestion")
}
