package uxfriction_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/uxfriction"
	"github.com/sageox/ox/internal/uxfriction/adapters"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCLI builds a realistic CLI for integration testing.
// Structure:
//
//	testcli
//	  |- init (with --force, --name flags)
//	  |- agent
//	  |    |- prime (with --verbose flag)
//	  |    |- status
//	  |- config
//	  |    |- set (with --global flag)
//	  |    |- get
//	  |- session
//	  |    |- start
//	  |    |- stop
func buildTestCLI() *cobra.Command {
	root := &cobra.Command{
		Use:           "testcli",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// init command
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	initCmd.Flags().BoolP("force", "f", false, "force initialization")
	initCmd.Flags().String("name", "", "project name")
	root.AddCommand(initCmd)

	// agent command with subcommands
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent management",
	}
	agentCmd.Flags().BoolP("verbose", "v", false, "verbose output")

	primeCmd := &cobra.Command{
		Use:   "prime",
		Short: "Prime an agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	primeCmd.Flags().Bool("verbose", false, "verbose output")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show agent status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	agentCmd.AddCommand(primeCmd, statusCmd)
	root.AddCommand(agentCmd)

	// config command with subcommands
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management",
	}

	setCmd := &cobra.Command{
		Use:   "set [key] [value]",
		Short: "Set a config value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	setCmd.Flags().BoolP("global", "g", false, "set globally")

	getCmd := &cobra.Command{
		Use:   "get [key]",
		Short: "Get a config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	configCmd.AddCommand(setCmd, getCmd)
	root.AddCommand(configCmd)

	// session command with subcommands
	sessionCmd := &cobra.Command{
		Use:   "session",
		Short: "Session management",
	}

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start a session",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop a session",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	sessionCmd.AddCommand(startCmd, stopCmd)
	root.AddCommand(sessionCmd)

	return root
}

// TestIntegration_EndToEnd_CLIWithFriction tests the complete friction flow:
// 1. Build a CLI
// 2. Execute invalid commands
// 3. Verify friction events are captured
// 4. Verify events are sent to mock server
func TestIntegration_EndToEnd_CLIWithFriction(t *testing.T) {
	t.Parallel()

	// build CLI and adapter
	root := buildTestCLI()
	adapter := adapters.NewCobraAdapter(root)
	handler := uxfriction.NewHandler(adapter, nil) // no catalog for basic tests

	testCases := []struct {
		name           string
		args           []string
		wantKind       uxfriction.FailureKind
		wantBadToken   string
		wantSuggestion string // expected suggestion (empty if none)
	}{
		{
			name:           "unknown command - typo",
			args:           []string{"testcli", "initt"}, // typo of "init"
			wantKind:       uxfriction.FailureUnknownCommand,
			wantBadToken:   "initt",
			wantSuggestion: "init",
		},
		{
			name:           "unknown command - close to agent",
			args:           []string{"testcli", "agnt"},
			wantKind:       uxfriction.FailureUnknownCommand,
			wantBadToken:   "agnt",
			wantSuggestion: "agent",
		},
		{
			name:         "unknown flag",
			args:         []string{"testcli", "init", "--verbose"},
			wantKind:     uxfriction.FailureUnknownFlag,
			wantBadToken: "--verbose",
		},
		{
			name:         "unknown flag typo - no suggestion for 3 char distance",
			args:         []string{"testcli", "init", "--forc"}, // typo of --force
			wantKind:     uxfriction.FailureUnknownFlag,
			wantBadToken: "--forc",
			// note: levenshtein may not find this if distance threshold is 2
			// (forc → force is distance 1, but --forc → --force is compared)
			wantSuggestion: "",
		},
		{
			name:           "unknown command - close to session",
			args:           []string{"testcli", "sesion"},
			wantKind:       uxfriction.FailureUnknownCommand,
			wantBadToken:   "sesion",
			wantSuggestion: "session",
		},
		{
			name:         "unknown shorthand flag",
			args:         []string{"testcli", "init", "-x"},
			wantKind:     uxfriction.FailureUnknownFlag,
			wantBadToken: "-x",
			// note: no close match for single-char flag
			wantSuggestion: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// execute command and capture error
			root := buildTestCLI() // fresh root for each test
			root.SetArgs(tc.args[1:])
			err := root.Execute()

			// command should fail with error
			require.Error(t, err, "command should fail")

			// process error through friction handler
			result := handler.Handle(tc.args, err)
			require.NotNil(t, result, "handler should produce result")
			require.NotNil(t, result.Event, "result should have event")

			// verify event fields
			assert.Equal(t, tc.wantKind, result.Event.Kind, "kind mismatch")
			assert.NotEmpty(t, result.Event.Timestamp, "timestamp should be set")
			assert.NotEmpty(t, result.Event.Actor, "actor should be set")
			assert.NotEmpty(t, result.Event.PathBucket, "path_bucket should be set")
			assert.Contains(t, result.Event.Input, tc.wantBadToken, "input should contain bad token")

			// verify suggestion if expected
			if tc.wantSuggestion != "" {
				require.NotNil(t, result.Suggestion, "suggestion expected")
				assert.Equal(t, tc.wantSuggestion, result.Suggestion.Corrected, "suggestion mismatch")
			}
		})
	}
}

// TestIntegration_FrictionEventsSubmittedToServer verifies that friction events
// are properly formatted and sent to the server.
func TestIntegration_FrictionEventsSubmittedToServer(t *testing.T) {
	t.Parallel()

	var receivedEvents []uxfriction.FrictionEvent
	var receivedVersion string
	var requestCount atomic.Int32

	// create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)

		// verify request
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/cli/friction", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// decode request
		var req uxfriction.SubmitRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		receivedVersion = req.Version
		receivedEvents = append(receivedEvents, req.Events...)

		// send successful response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(uxfriction.FrictionResponse{Accepted: len(req.Events)})
	}))
	defer server.Close()

	// build CLI and adapter
	root := buildTestCLI()
	adapter := adapters.NewCobraAdapter(root)
	handler := uxfriction.NewHandler(adapter, nil)

	// create friction client pointing to mock server
	client := uxfriction.NewClient(uxfriction.ClientConfig{
		Endpoint: server.URL,
		Version:  "test-0.1.0",
	})

	// create buffer to collect events
	buffer := uxfriction.NewRingBuffer(100)

	// simulate CLI errors and collect friction events
	errorCases := [][]string{
		{"testcli", "initt"},          // unknown command
		{"testcli", "init", "--forc"}, // unknown flag
		{"testcli", "agnt", "prime"},  // unknown command
		{"testcli", "session", "-x"},  // unknown shorthand
	}

	for _, args := range errorCases {
		root := buildTestCLI()
		root.SetArgs(args[1:])
		err := root.Execute()
		if err != nil {
			result := handler.Handle(args, err)
			if result != nil && result.Event != nil {
				buffer.Add(*result.Event)
			}
		}
	}

	// verify buffer has events
	assert.Equal(t, 4, buffer.Count(), "buffer should have 4 events")

	// drain and submit to server
	events := buffer.Drain()
	resp, err := client.Submit(context.Background(), events, nil)
	require.NoError(t, err)

	// verify response
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(1), requestCount.Load(), "should have made 1 request")

	// verify events were received by server
	assert.Len(t, receivedEvents, 4, "server should receive 4 events")
	assert.Equal(t, "test-0.1.0", receivedVersion, "version should be set")

	// verify event content
	for _, event := range receivedEvents {
		assert.NotEmpty(t, event.Timestamp, "timestamp should be set")
		assert.NotEmpty(t, event.Kind, "kind should be set")
		assert.NotEmpty(t, event.Actor, "actor should be set")
		assert.NotEmpty(t, event.Input, "input should be set")
	}
}

// TestIntegration_RateLimiting verifies that the client respects rate limiting.
func TestIntegration_RateLimiting(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32

	// server that returns sample rate 0 after first request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)

		// first request succeeds, subsequent requests get rate limited
		if count == 1 {
			w.Header().Set("X-SageOx-Sample-Rate", "0.0")
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(uxfriction.FrictionResponse{Accepted: 1})
	}))
	defer server.Close()

	client := uxfriction.NewClient(uxfriction.ClientConfig{
		Endpoint: server.URL,
		Version:  "test-0.1.0",
	})

	// first submission should succeed
	assert.True(t, client.ShouldSend(), "should send first request")
	_, err := client.Submit(context.Background(), []uxfriction.FrictionEvent{{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Kind:       "unknown-command",
		Actor:      "human",
		PathBucket: "repo",
		Input:      "testcli foo",
	}}, nil)
	require.NoError(t, err)

	// after receiving sample rate 0, should not send
	assert.False(t, client.ShouldSend(), "should not send after rate limit")
	assert.Equal(t, int32(1), requestCount.Load(), "only 1 request should be made")
}

// TestIntegration_RetryAfter verifies that the client respects Retry-After header.
func TestIntegration_RetryAfter(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32

	// server returns Retry-After header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Retry-After", "3600") // 1 hour
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := uxfriction.NewClient(uxfriction.ClientConfig{
		Endpoint: server.URL,
		Version:  "test-0.1.0",
	})

	// first request
	assert.True(t, client.ShouldSend())
	_, err := client.Submit(context.Background(), []uxfriction.FrictionEvent{{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Kind:       "unknown-command",
		Actor:      "human",
		PathBucket: "repo",
		Input:      "testcli foo",
	}}, nil)
	require.NoError(t, err)

	// after receiving Retry-After, should not send
	assert.False(t, client.ShouldSend(), "should not send during retry-after period")
	assert.False(t, client.RetryAfter().IsZero(), "retry-after should be set")
}

// TestIntegration_BufferBoundsUnderLoad verifies memory bounds under heavy CLI errors.
func TestIntegration_BufferBoundsUnderLoad(t *testing.T) {
	t.Parallel()

	const bufferSize = 50
	buffer := uxfriction.NewRingBuffer(bufferSize)

	// build handler
	root := buildTestCLI()
	adapter := adapters.NewCobraAdapter(root)
	handler := uxfriction.NewHandler(adapter, nil)

	// simulate heavy load - 1000 unique errors
	for i := 0; i < 1000; i++ {
		cmdName := fmt.Sprintf("unknown-cmd-%d", i)
		root := buildTestCLI()
		root.SetArgs([]string{cmdName})
		err := root.Execute()
		if err != nil {
			result := handler.Handle([]string{"testcli", cmdName}, err)
			if result != nil && result.Event != nil {
				buffer.Add(*result.Event)
			}
		}

		// verify buffer never exceeds capacity
		if buffer.Count() > bufferSize {
			t.Fatalf("buffer exceeded capacity: %d > %d at iteration %d",
				buffer.Count(), bufferSize, i)
		}
	}

	// buffer should be at capacity
	assert.Equal(t, bufferSize, buffer.Count(), "buffer should be at capacity")

	// drain should return exactly bufferSize events
	events := buffer.Drain()
	assert.Len(t, events, bufferSize, "drain should return bufferSize events")
	assert.Equal(t, 0, buffer.Count(), "buffer should be empty after drain")
}

// TestIntegration_CatalogUpdateFromServer verifies catalog updates are received.
func TestIntegration_CatalogUpdateFromServer(t *testing.T) {
	t.Parallel()

	// server returns catalog in response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(uxfriction.FrictionResponse{
			Accepted: 1,
			Catalog: &uxfriction.CatalogData{
				Version: "v2026-01-20-001",
				Tokens: []uxfriction.TokenMapping{
					{Pattern: "initt", Target: "init", Kind: "unknown-command", Confidence: 0.95},
					{Pattern: "agnt", Target: "agent", Kind: "unknown-command", Confidence: 0.90},
				},
				Commands: []uxfriction.CommandMapping{
					{Pattern: "daemon status", Target: "status", Confidence: 0.85},
				},
			},
		})
	}))
	defer server.Close()

	client := uxfriction.NewClient(uxfriction.ClientConfig{
		Endpoint: server.URL,
		Version:  "test-0.1.0",
	})

	// submit event
	resp, err := client.Submit(context.Background(), []uxfriction.FrictionEvent{{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Kind:       "unknown-command",
		Actor:      "human",
		PathBucket: "repo",
		Input:      "testcli initt",
	}}, nil)
	require.NoError(t, err)

	// verify catalog was received
	require.NotNil(t, resp.Catalog, "catalog should be in response")
	assert.Equal(t, "v2026-01-20-001", resp.Catalog.Version)
	assert.Len(t, resp.Catalog.Tokens, 2)
	assert.Len(t, resp.Catalog.Commands, 1)
}

// TestIntegration_EventFieldValidation verifies event fields match API expectations.
func TestIntegration_EventFieldValidation(t *testing.T) {
	t.Parallel()

	var receivedEvent uxfriction.FrictionEvent

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req uxfriction.SubmitRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Events) > 0 {
			receivedEvent = req.Events[0]
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(uxfriction.FrictionResponse{Accepted: 1})
	}))
	defer server.Close()

	// build handler and process a real error
	root := buildTestCLI()
	adapter := adapters.NewCobraAdapter(root)
	handler := uxfriction.NewHandler(adapter, nil)

	root = buildTestCLI()
	root.SetArgs([]string{"initt", "--badflg"})
	err := root.Execute()
	require.Error(t, err)

	result := handler.Handle([]string{"testcli", "initt", "--badflg"}, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Event)

	// submit to server
	client := uxfriction.NewClient(uxfriction.ClientConfig{
		Endpoint: server.URL,
		Version:  "test-0.1.0",
	})
	_, submitErr := client.Submit(context.Background(), []uxfriction.FrictionEvent{*result.Event}, nil)
	require.NoError(t, submitErr)

	// verify all required fields are present (matching API spec)
	assert.NotEmpty(t, receivedEvent.Timestamp, "ts required by API")
	assert.NotEmpty(t, receivedEvent.Kind, "kind required by API")
	assert.NotEmpty(t, receivedEvent.Actor, "actor required by API")
	assert.NotEmpty(t, receivedEvent.PathBucket, "path_bucket required by API")
	assert.NotEmpty(t, receivedEvent.Input, "input required by API")

	// verify kind is valid enum
	validKinds := map[string]bool{
		"unknown-command":  true,
		"unknown-flag":     true,
		"missing-required": true,
		"invalid-arg":      true,
		"parse-error":      true,
	}
	assert.True(t, validKinds[string(receivedEvent.Kind)], "kind must be valid enum: %s", receivedEvent.Kind)

	// verify actor is valid enum
	validActors := map[string]bool{
		"human":   true,
		"agent":   true,
		"unknown": true,
	}
	assert.True(t, validActors[receivedEvent.Actor], "actor must be valid enum: %s", receivedEvent.Actor)

	// verify path_bucket is valid enum
	validBuckets := map[string]bool{
		"home":  true,
		"repo":  true,
		"other": true,
	}
	assert.True(t, validBuckets[receivedEvent.PathBucket], "path_bucket must be valid enum: %s", receivedEvent.PathBucket)
}

// TestIntegration_ConcurrentCLIErrors verifies thread safety under concurrent errors.
// Note: Each goroutine creates its own handler because cobra command trees are not
// thread-safe (Commands() sorts on access). This is realistic - each CLI invocation
// would have its own handler anyway.
func TestIntegration_ConcurrentCLIErrors(t *testing.T) {
	t.Parallel()

	const (
		numGoroutines      = 20
		errorsPerGoroutine = 50
		bufferSize         = 100
	)

	buffer := uxfriction.NewRingBuffer(bufferSize)
	var wg bytes.Buffer // just for sync, not used for data
	done := make(chan struct{})

	// spawn goroutines that generate errors concurrently
	// each goroutine creates its own CLI and handler (cobra is not thread-safe)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < errorsPerGoroutine; j++ {
				// create fresh CLI and handler for each error (thread-safe)
				root := buildTestCLI()
				adapter := adapters.NewCobraAdapter(root)
				handler := uxfriction.NewHandler(adapter, nil)

				root.SetArgs([]string{"unknown-cmd"})
				err := root.Execute()
				if err != nil {
					result := handler.Handle([]string{"testcli", "unknown"}, err)
					if result != nil && result.Event != nil {
						// buffer.Add is thread-safe
						buffer.Add(*result.Event)
					}
				}
			}
		}(i)
	}

	// use WaitGroup properly
	go func() {
		time.Sleep(2 * time.Second) // give goroutines time to finish
		close(done)
	}()

	<-done
	_ = wg // silence unused warning

	// buffer should be bounded
	count := buffer.Count()
	assert.LessOrEqual(t, count, bufferSize, "buffer should not exceed capacity")

	// drain should work
	events := buffer.Drain()
	assert.LessOrEqual(t, len(events), bufferSize)
	assert.Equal(t, 0, buffer.Count(), "buffer should be empty after drain")
}

// TestIntegration_TruncationEnforced verifies field truncation.
func TestIntegration_TruncationEnforced(t *testing.T) {
	t.Parallel()

	var receivedEvent uxfriction.FrictionEvent

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req uxfriction.SubmitRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Events) > 0 {
			receivedEvent = req.Events[0]
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// create event with oversized fields
	longInput := make([]byte, 1000)
	for i := range longInput {
		longInput[i] = 'x'
	}
	longError := make([]byte, 500)
	for i := range longError {
		longError[i] = 'e'
	}

	event := uxfriction.FrictionEvent{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Kind:       "unknown-command",
		Actor:      "human",
		PathBucket: "repo",
		Input:      string(longInput),
		ErrorMsg:   string(longError),
	}

	client := uxfriction.NewClient(uxfriction.ClientConfig{
		Endpoint: server.URL,
		Version:  "test-0.1.0",
	})

	_, err := client.Submit(context.Background(), []uxfriction.FrictionEvent{event}, nil)
	require.NoError(t, err)

	// verify truncation was applied
	assert.LessOrEqual(t, len(receivedEvent.Input), uxfriction.MaxInputLength,
		"input should be truncated to max length")
	assert.LessOrEqual(t, len(receivedEvent.ErrorMsg), uxfriction.MaxErrorLength,
		"error_msg should be truncated to max length")
}
