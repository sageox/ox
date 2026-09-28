//go:build integration

package receiver_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/trace/receiver"
)

var runLiveTrace = flag.Bool("trace-live", false, "run paid Claude Code trace capture against the authenticated local CLI")

// TestLiveClaudeTraceCapture verifies the actual vendor payload and export flush,
// which synthetic OTLP fixtures cannot establish. Explicitly opt in with:
// go test -tags integration ./internal/trace/receiver -run TestLiveClaudeTraceCapture -count=1 -v -args -trace-live
func TestLiveClaudeTraceCapture(t *testing.T) {
	if testing.Short() || !*runLiveTrace {
		t.Skip("requires -args -trace-live and a locally authenticated claude CLI")
	}
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI is not installed")
	}
	root := t.TempDir()
	spool := filepath.Join(root, "spool")
	r := receiver.New(receiver.Config{SpoolDir: spool, Version: "live-test"})
	server := httptest.NewServer(r.Handler())
	defer server.Close()
	ids := []string{uuid.NewString(), uuid.NewString()}
	for i, id := range ids {
		sessionRoot := filepath.Join(root, fmt.Sprintf("session-%d", i))
		if err := os.Mkdir(sessionRoot, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionRoot, "sample.txt"), []byte("local trace capture verification\n"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, binary, "-p", "Read sample.txt with the Read tool, then reply with exactly TRACE_OK. Do not run any other tools.",
			"--session-id", id, "--setting-sources", "", "--settings", `{"disableAllHooks":true}`,
			"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--disable-slash-commands",
			"--tools", "Read", "--allowedTools", "Read", "--no-session-persistence", "--output-format", "json", "--max-budget-usd", "1")
		cmd.Dir = sessionRoot
		cmd.Env = liveTraceEnv(server.URL)
		started := time.Now()
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			// Output can contain account/provider diagnostics; report only bounded
			// machine-readable result metadata, never credentials or the raw output.
			t.Fatalf("Claude session %d failed after %s: %v (output bytes=%d)", i, time.Since(started).Round(time.Millisecond), err, len(output))
		}
		var result struct {
			IsError   bool    `json:"is_error"`
			SessionID string  `json:"session_id"`
			TotalCost float64 `json:"total_cost_usd"`
			Usage     struct {
				Input       int `json:"input_tokens"`
				Output      int `json:"output_tokens"`
				CacheRead   int `json:"cache_read_input_tokens"`
				CacheCreate int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(output), &result); err != nil {
			t.Fatalf("Claude session %d did not return JSON result: %v", i, err)
		}
		if result.IsError || result.SessionID != id {
			t.Fatalf("Claude session %d unsuccessful or unexpected session ID", i)
		}
		t.Logf("session=%s wall=%s input=%d cache_read=%d cache_create=%d output=%d cost_usd=%.6f", id, time.Since(started).Round(time.Millisecond), result.Usage.Input, result.Usage.CacheRead, result.Usage.CacheCreate, result.Usage.Output, result.TotalCost)
	}
	for i, id := range ids {
		for _, signal := range []string{"traces", "logs"} {
			path := filepath.Join(spool, id, signal+".jsonl")
			records, names, err := inspectLiveOTLP(path, signal, id)
			if err != nil {
				t.Fatalf("session %s %s: %v", id, signal, err)
			}
			if records == 0 {
				t.Fatalf("session %s has no %s records", id, signal)
			}
			if signal == "traces" {
				for _, name := range []string{"claude_code.interaction", "claude_code.llm_request", "claude_code.tool"} {
					if !names[name] {
						t.Errorf("session %s missing expected %s span", id, name)
					}
				}
			}
			// Negative control: the same validator must reject a stream filed under
			// another session's directory, the failure demultiplexing must prevent.
			if _, _, err := inspectLiveOTLP(path, signal, ids[1-i]); err == nil {
				t.Fatal("negative control failed: validator accepted cross-session payload")
			}
			t.Logf("session=%s signal=%s records=%d; cross-session negative control rejected", id, signal, records)
		}
	}
	stats, err := receiver.Stats(spool)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sessions != 2 {
		t.Fatalf("spool has %d sessions, want exactly 2", stats.Sessions)
	}
	t.Logf("captured sessions=%d bytes=%d", stats.Sessions, stats.Bytes)
}

func liveTraceEnv(endpoint string) []string {
	var env []string
	for _, entry := range os.Environ() { // safe: opt-in real Claude test retains login credentials but replaces telemetry destinations; settings/hooks are disabled by the caller.
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "OTEL_") || key == "CLAUDECODE" || key == "CLAUDE_CODE_ENABLE_TELEMETRY" || key == "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA" || key == "ENABLE_ENHANCED_TELEMETRY_BETA" || key == "ENABLE_BETA_TRACING_DETAILED" || key == "BETA_TRACING_ENDPOINT" || key == "OX_SESSION_RECORDING" {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"CLAUDE_CODE_ENABLE_TELEMETRY=1", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1",
		"OTEL_TRACES_EXPORTER=otlp", "OTEL_LOGS_EXPORTER=otlp", "OTEL_METRICS_EXPORTER=none",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/json", "OTEL_EXPORTER_OTLP_ENDPOINT="+endpoint,
		"OTEL_TRACES_EXPORT_INTERVAL=100", "OTEL_LOGS_EXPORT_INTERVAL=100",
		"OTEL_LOG_USER_PROMPTS=0", "OTEL_LOG_ASSISTANT_RESPONSES=0", "OTEL_LOG_TOOL_DETAILS=0", "OTEL_LOG_TOOL_CONTENT=0", "OTEL_LOG_RAW_API_BODIES=0", "OTEL_LOG_MANAGED_SETTINGS=0",
		"ENABLE_BETA_TRACING_DETAILED=0", "OX_SESSION_RECORDING=disabled")
}

type liveAttribute struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}
type liveRecord struct {
	Name       string          `json:"name"`
	Attributes []liveAttribute `json:"attributes"`
}

func inspectLiveOTLP(path, signal, wantID string) (int, map[string]bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), receiver.MaxBodyBytes)
	resourceKey, scopeKey, recordKey := "resourceSpans", "scopeSpans", "spans"
	if signal == "logs" {
		resourceKey, scopeKey, recordKey = "resourceLogs", "scopeLogs", "logRecords"
	}
	count := 0
	names := make(map[string]bool)
	resolve := func(attrs []liveAttribute, inherited string) (string, error) {
		id := inherited
		for _, a := range attrs {
			if a.Key == "session.id" {
				id = a.Value.StringValue
				if id != wantID {
					return "", fmt.Errorf("session.id mismatch: expected %s", wantID)
				}
			}
		}
		return id, nil
	}
	for scanner.Scan() {
		var top map[string][]map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &top); err != nil {
			return count, names, err
		}
		for _, resource := range top[resourceKey] {
			var resourceMeta liveRecord
			if raw, ok := resource["resource"]; ok {
				if err := json.Unmarshal(raw, &resourceMeta); err != nil {
					return count, names, err
				}
			}
			resourceID, err := resolve(resourceMeta.Attributes, "")
			if err != nil {
				return count, names, err
			}
			var scopes []map[string]json.RawMessage
			if err := json.Unmarshal(resource[scopeKey], &scopes); err != nil {
				return count, names, err
			}
			for _, scope := range scopes {
				var scopeMeta liveRecord
				if raw, ok := scope["scope"]; ok {
					if err := json.Unmarshal(raw, &scopeMeta); err != nil {
						return count, names, err
					}
				}
				scopeID, err := resolve(scopeMeta.Attributes, resourceID)
				if err != nil {
					return count, names, err
				}
				var records []liveRecord
				if err := json.Unmarshal(scope[recordKey], &records); err != nil {
					return count, names, err
				}
				for _, record := range records {
					id, err := resolve(record.Attributes, scopeID)
					if err != nil {
						return count, names, err
					}
					if id != wantID {
						return count, names, fmt.Errorf("record lacks expected session.id %s", wantID)
					}
					count++
					names[record.Name] = true
				}
			}
		}
	}
	return count, names, scanner.Err()
}
