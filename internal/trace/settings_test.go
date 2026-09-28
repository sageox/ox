package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pilotExportEnv() []string {
	return []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1", "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1",
		"OTEL_TRACES_EXPORTER=otlp", "OTEL_LOGS_EXPORTER=otlp", "OTEL_METRICS_EXPORTER=none",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/json", "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:14318",
	}
}

func writeExportSettings(t *testing.T, path string, settings any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// A shell-only launch must pass, while overrides that redirect or disable it
// must never be rendered indistinguishably from successful capture.
func TestExporterEnvironmentValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
		issue string
	}{
		{name: "environment only"},
		{name: "alias", extra: []string{"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=", "ENABLE_ENHANCED_TELEMETRY_BETA=1"}},
		{name: "telemetry off", extra: []string{"CLAUDE_CODE_ENABLE_TELEMETRY=0"}, issue: "CLAUDE_CODE_ENABLE_TELEMETRY"},
		{name: "beta off", extra: []string{"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=0"}, issue: "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA"},
		{name: "missing exporter", extra: []string{"OTEL_TRACES_EXPORTER="}, issue: "OTEL_TRACES_EXPORTER"},
		{name: "additional exporter", extra: []string{"OTEL_LOGS_EXPORTER=console,otlp"}, issue: "OTEL_LOGS_EXPORTER"},
		{name: "metrics", extra: []string{"OTEL_METRICS_EXPORTER=otlp"}, issue: "OTEL_METRICS_EXPORTER"},
		{name: "signal protocol", extra: []string{"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf"}, issue: "traces exporter protocol"},
		{name: "signal endpoint", extra: []string{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=https://remote.example/v1/logs"}, issue: "logs exporter endpoint"},
		{name: "wrong path", extra: []string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://localhost:14318"}, issue: "traces exporter endpoint"},
		{name: "valid signal overrides", extra: []string{"OTEL_EXPORTER_OTLP_PROTOCOL=grpc", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/json", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/json", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://localhost:14318/v1/traces", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://localhost:14318/v1/logs"}},
		{name: "detailed redirect", extra: []string{"ENABLE_BETA_TRACING_DETAILED=1", "BETA_TRACING_ENDPOINT=https://remote.example"}, issue: "detailed beta tracing"},
		{name: "raw body files", extra: []string{"OTEL_LOG_RAW_API_BODIES=file:/private"}, issue: "OTEL_LOG_RAW_API_BODIES"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			got := inspectExport(root, filepath.Join(root, "user"), filepath.Join(root, "managed"), 14318, append(pilotExportEnv(), tc.extra...))
			if tc.issue == "" {
				if !got.Ready {
					t.Fatalf("valid local export rejected: %v", got.Issues)
				}
			} else if got.Ready || !strings.Contains(strings.Join(got.Issues, "; "), tc.issue) {
				t.Fatalf("wanted %q issue, got %+v", tc.issue, got)
			}
			if len(got.Notes) == 0 {
				t.Fatal("must explain inability to observe running Claude environment")
			}
		})
	}
	for _, key := range []string{"OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_DETAILS", "OTEL_LOG_TOOL_CONTENT", "OTEL_LOG_RAW_API_BODIES", "OTEL_LOG_MANAGED_SETTINGS"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			got := inspectExport(root, root, root, 14318, append(pilotExportEnv(), key+"=1"))
			if got.Ready || !strings.Contains(strings.Join(got.Issues, "; "), key) {
				t.Fatalf("content capture not reported: %+v", got)
			}
		})
	}
}

// Settings env wins over shell env; local settings then override project/user,
// and managed settings can remove lower-tier per-signal routing entirely.
func TestExporterSettingsPrecedence(t *testing.T) {
	root := t.TempDir()
	user, managed := filepath.Join(root, "user"), filepath.Join(root, "managed")
	endpoint := "OTEL_EXPORTER_OTLP_ENDPOINT"
	writeExportSettings(t, filepath.Join(user, "settings.json"), exportSettings{Env: map[string]string{endpoint: "https://remote.example"}})
	inspect := func() ExportStatus { return inspectExport(root, user, managed, 14318, pilotExportEnv()) }
	if got := inspect(); got.Ready || got.Sources[endpoint] != "user" {
		t.Fatalf("user settings must override shell: %+v", got)
	}
	writeExportSettings(t, filepath.Join(root, ".claude", "settings.json"), exportSettings{Env: map[string]string{endpoint: "http://127.0.0.1:14318"}})
	if got := inspect(); !got.Ready || got.Sources[endpoint] != "project" {
		t.Fatalf("project should override user: %+v", got)
	}
	writeExportSettings(t, filepath.Join(root, ".claude", "settings.local.json"), exportSettings{Env: map[string]string{endpoint: "https://remote.example", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "grpc", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "https://remote.example/v1/logs"}})
	if got := inspect(); got.Ready || got.Sources[endpoint] != "project local" {
		t.Fatalf("local should override project: %+v", got)
	}
	writeExportSettings(t, filepath.Join(managed, "managed-settings.json"), exportSettings{Env: map[string]string{endpoint: "http://127.0.0.1:14318", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/json"}})
	if got := inspect(); !got.Ready || got.Sources[endpoint] != "managed" {
		t.Fatalf("managed should clear signal overrides: %+v", got)
	}
	writeExportSettings(t, filepath.Join(managed, "managed-settings.d", "10-telemetry.json"), exportSettings{Env: map[string]string{endpoint: "https://remote.example"}})
	if got := inspect(); got.Ready {
		t.Fatal("managed drop-in redirect was ignored")
	}
	writeExportSettings(t, filepath.Join(managed, "managed-settings.d", "20-telemetry.json"), exportSettings{Env: map[string]string{endpoint: "http://127.0.0.1:14318"}})
	if got := inspect(); !got.Ready {
		t.Fatalf("later managed drop-in should win: %+v", got)
	}
}

// Invalid/unreadable files are an unknown configuration, never an empty success.
func TestExporterUnreadableAndInvalidSettings(t *testing.T) {
	for _, corrupt := range []string{"{", `{"env":{"OTEL_LOGS_EXPORTER":true}}`} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(corrupt), 0600); err != nil {
			t.Fatal(err)
		}
		got := inspectExport("", root, filepath.Join(root, "managed"), 14318, pilotExportEnv())
		if got.Ready || !strings.Contains(strings.Join(got.Issues, "; "), "invalid user") {
			t.Fatalf("broken file silently ignored: %+v", got)
		}
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	got := inspectExport("", root, filepath.Join(root, "managed"), 14318, pilotExportEnv())
	if got.Ready || !strings.Contains(strings.Join(got.Issues, "; "), "cannot read user") {
		t.Fatalf("unreadable file silently ignored: %+v", got)
	}
}

// Status must never expose credentials or execute a configured helper.
func TestExporterCredentialsAndHelpersStayPrivate(t *testing.T) {
	root := t.TempDir()
	sentinel := "DO-NOT-EXPOSE-THIS-TOKEN"
	writeExportSettings(t, filepath.Join(root, "managed-settings.json"), exportSettings{Env: map[string]string{"OTEL_EXPORTER_OTLP_HEADERS": sentinel}, HeadersHelper: "touch " + filepath.Join(root, "executed")})
	got := inspectExport("", filepath.Join(root, "user"), root, 14318, append(pilotExportEnv(), "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://user:"+sentinel+"@localhost:14318/v1/logs"))
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready || strings.Contains(string(data), sentinel) {
		t.Fatalf("unsafe status: %s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
		t.Fatal("helper executed")
	}
	if !strings.Contains(strings.Join(got.Issues, "; "), "otelHeadersHelper") {
		t.Fatal("helper uncertainty not reported")
	}
	if !strings.Contains(strings.Join(got.Issues, "; "), "exporter endpoint") {
		t.Fatal("managed credentials must remove developer endpoint")
	}
}

// Repository files cannot activate Claude's restricted detailed/raw content env.
func TestExporterIgnoresRestrictedProjectVariables(t *testing.T) {
	root := t.TempDir()
	writeExportSettings(t, filepath.Join(root, ".claude", "settings.json"), exportSettings{Env: map[string]string{"ENABLE_BETA_TRACING_DETAILED": "1", "BETA_TRACING_ENDPOINT": "https://remote.example", "OTEL_LOG_RAW_API_BODIES": "1"}})
	got := inspectExport(root, filepath.Join(root, "user"), filepath.Join(root, "managed"), 14318, pilotExportEnv())
	if !got.Ready {
		t.Fatalf("project cannot activate restricted vars: %+v", got)
	}
}

func TestExporterEndpointTargetsReceiver(t *testing.T) {
	for _, tc := range []struct {
		value, path string
		want        bool
	}{
		{"http://127.0.0.1:14318", "", true}, {"http://localhost:14318/", "", true},
		{"http://127.0.0.1:14318/v1/traces", "/v1/traces", true},
		{"http://127.0.0.1:14318/v1/logs", "/v1/traces", false},
		{"http://127.0.0.1:14319", "", false}, {"http://127.0.0.2:14318", "", false},
		{"http://[::1]:14318", "", false}, {"http://localhost:14318?token=secret", "", false},
		{"http://localhost:14318/#fragment", "", false}, {"http://user:pass@localhost:14318", "", false},
		{"https://localhost:14318", "", false}, {"http://localhost:bad", "", false},
	} {
		if got := localExportEndpoint(tc.value, 14318, tc.path); got != tc.want {
			t.Errorf("endpoint %q: %v, want %v", tc.value, got, tc.want)
		}
	}
}

// The public inspector must use CLAUDE_CONFIG_DIR instead of silently reading
// the normal home settings when Claude is launched with an isolated config.
func TestInspectExportUsesClaudeConfigDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	got := InspectExport("", 14318)
	if got.Ready || !strings.Contains(strings.Join(got.Issues, "; "), "invalid user settings JSON") {
		t.Fatalf("custom config not inspected: %+v", got)
	}
}

// A worktree or dynamic managed policy must not look conclusively configured
// when settings outside this inspector's view can override the endpoint.
func TestExporterReportsUninspectableSources(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere"), 0600); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "managed")
	writeExportSettings(t, filepath.Join(managed, "managed-settings.json"), exportSettings{PolicyHelper: json.RawMessage(`{"command":"do-not-run"}`)})
	got := inspectExport(root, filepath.Join(root, "user"), managed, 14318, pilotExportEnv())
	for _, issue := range []string{"worktree", "policyHelper"} {
		if !strings.Contains(strings.Join(got.Issues, "; "), issue) {
			t.Fatalf("missing %s uncertainty: %+v", issue, got)
		}
	}
	if got.Ready {
		t.Fatal("uninspectable configuration should not report ready")
	}
}
