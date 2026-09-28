package trace

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// ExportStatus describes configuration visible to this ox process, not the
// environment of a running Claude Code session. It deliberately contains no
// environment values, URLs, headers, or helper commands.
type ExportStatus struct {
	Ready   bool              `json:"ready"`
	Issues  []string          `json:"issues,omitempty"`
	Notes   []string          `json:"notes,omitempty"`
	Sources map[string]string `json:"sources,omitempty"`
}

type exportSettings struct {
	Env           map[string]string `json:"env"`
	HeadersHelper string            `json:"otelHeadersHelper"`
	PolicyHelper  json.RawMessage   `json:"policyHelper"`
}

// InspectExport reads local Claude settings and this process's environment.
// It never edits settings or executes configured helper commands. Settings env
// overrides shell env (user < project < local < managed); see Claude Code's
// settings-reference#env and monitoring-usage#administrator-configuration.
func InspectExport(projectRoot string, port int) ExportStatus {
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ExportStatus{Issues: []string{"cannot locate Claude Code user settings"}}
		}
		configDir = filepath.Join(home, ".claude")
	}
	return inspectExport(projectRoot, configDir, managedSettingsDir(), port, os.Environ())
}

func managedSettingsDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default:
		return "/etc/claude-code"
	}
}

func inspectExport(projectRoot, configDir, managedDir string, port int, environ []string) ExportStatus {
	status := ExportStatus{
		Sources: make(map[string]string),
		Notes:   []string{"Checks this ox process and local files only; cannot observe a running Claude Code session, launcher/--settings overrides, or server-managed/MDM policy. Confirm Claude Code's /status and receipt of traces.", "Claude Code removes OTEL_* variables from hooks and tool subprocesses; run status from the same shell environment used to launch Claude Code."},
	}
	values := make(map[string]string)
	merge := func(env map[string]string, source string, project bool) {
		for key, value := range env {
			if !exportVariable(key) {
				continue
			}
			if project && (key == "BETA_TRACING_ENDPOINT" || key == "ENABLE_BETA_TRACING_DETAILED" || key == "OTEL_LOG_RAW_API_BODIES" || key == "OTEL_LOG_MANAGED_SETTINGS") {
				continue
			}
			values[key], status.Sources[key] = value, source
		}
	}
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			merge(map[string]string{key: value}, "environment", false)
		}
	}
	read := func(path, source string) exportSettings {
		var settings exportSettings
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return settings
		}
		if err != nil {
			status.Issues = append(status.Issues, "cannot read "+source+" settings")
			return settings
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			status.Issues = append(status.Issues, "invalid "+source+" settings JSON")
			return exportSettings{}
		}
		return settings
	}
	helper := ""
	apply := func(path, source string, project bool) {
		settings := read(path, source)
		merge(settings.Env, source, project)
		if settings.HeadersHelper != "" {
			helper = source
		}
	}
	apply(filepath.Join(configDir, "settings.json"), "user", false)
	if projectRoot != "" {
		apply(filepath.Join(projectRoot, ".claude", "settings.json"), "project", true)
		apply(filepath.Join(projectRoot, ".claude", "settings.local.json"), "project local", true)
		// Worktree local settings can come from the main checkout. Do not present
		// a partial inspection as definitive when that location isn't inspected.
		if st, err := os.Stat(filepath.Join(projectRoot, ".git")); err == nil && !st.IsDir() {
			status.Issues = append(status.Issues, "worktree: Claude Code may use main-checkout local settings; verify them in Claude Code /status")
		}
	}
	managed := read(filepath.Join(managedDir, "managed-settings.json"), "managed")
	entries, err := os.ReadDir(filepath.Join(managedDir, "managed-settings.d"))
	if err != nil && !os.IsNotExist(err) {
		status.Issues = append(status.Issues, "cannot read managed settings drop-ins")
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		dropin := read(filepath.Join(managedDir, "managed-settings.d", entry.Name()), "managed drop-in")
		if managed.Env == nil {
			managed.Env = make(map[string]string)
		}
		for key, value := range dropin.Env {
			managed.Env[key] = value
		}
		if dropin.HeadersHelper != "" {
			managed.HeadersHelper = dropin.HeadersHelper
		}
		if len(dropin.PolicyHelper) > 0 {
			managed.PolicyHelper = dropin.PolicyHelper
		}
	}
	// A managed generic destination/protocol removes developer per-signal
	// overrides. Managed credentials can also remove developer destinations.
	remove := func(key string) { delete(values, key); delete(status.Sources, key) }
	for _, field := range []string{"ENDPOINT", "PROTOCOL"} {
		if _, set := managed.Env["OTEL_EXPORTER_OTLP_"+field]; set {
			for _, signal := range []string{"TRACES", "LOGS", "METRICS"} {
				remove("OTEL_EXPORTER_OTLP_" + signal + "_" + field)
			}
		}
	}
	for _, field := range []string{"HEADERS", "CLIENT_KEY", "CLIENT_CERTIFICATE"} {
		if _, set := managed.Env["OTEL_EXPORTER_OTLP_"+field]; set {
			remove("OTEL_EXPORTER_OTLP_ENDPOINT")
			for _, signal := range []string{"TRACES", "LOGS", "METRICS"} {
				remove("OTEL_EXPORTER_OTLP_" + signal + "_ENDPOINT")
				remove("OTEL_EXPORTER_OTLP_" + signal + "_" + field)
			}
		}
		for _, signal := range []string{"TRACES", "LOGS", "METRICS"} {
			if _, set := managed.Env["OTEL_EXPORTER_OTLP_"+signal+"_"+field]; set {
				remove("OTEL_EXPORTER_OTLP_" + signal + "_ENDPOINT")
			}
		}
	}
	merge(managed.Env, "managed", false)
	if managed.HeadersHelper != "" {
		helper = "managed"
	}
	if helper != "" {
		status.Issues = append(status.Issues, "otelHeadersHelper is configured in "+helper+" settings; its execution and export behavior cannot be verified")
	}
	if len(managed.PolicyHelper) > 0 && string(managed.PolicyHelper) != "null" {
		status.Issues = append(status.Issues, "managed policyHelper may change exporter settings; inspect the resolved policy in Claude Code")
	}
	if !exportEnabled(values["CLAUDE_CODE_ENABLE_TELEMETRY"]) {
		status.Issues = append(status.Issues, "CLAUDE_CODE_ENABLE_TELEMETRY must be enabled")
	}
	if !exportEnabled(values["CLAUDE_CODE_ENHANCED_TELEMETRY_BETA"]) && !exportEnabled(values["ENABLE_ENHANCED_TELEMETRY_BETA"]) {
		status.Issues = append(status.Issues, "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA must be enabled")
	}
	for _, signal := range []string{"TRACES", "LOGS"} {
		if strings.TrimSpace(values["OTEL_"+signal+"_EXPORTER"]) != "otlp" {
			status.Issues = append(status.Issues, "OTEL_"+signal+"_EXPORTER must be otlp for local capture")
		}
		protocol := values["OTEL_EXPORTER_OTLP_"+signal+"_PROTOCOL"]
		if protocol == "" {
			protocol = values["OTEL_EXPORTER_OTLP_PROTOCOL"]
		}
		if protocol != "http/json" {
			status.Issues = append(status.Issues, strings.ToLower(signal)+" exporter protocol must be http/json")
		}
		endpoint := values["OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT"]
		specific := endpoint != ""
		if !specific {
			endpoint = values["OTEL_EXPORTER_OTLP_ENDPOINT"]
		}
		path := ""
		if specific {
			path = "/v1/" + strings.ToLower(signal)
		}
		if !localExportEndpoint(endpoint, port, path) {
			status.Issues = append(status.Issues, fmt.Sprintf("%s exporter endpoint must target http://127.0.0.1:%d%s", strings.ToLower(signal), port, path))
		}
	}
	if values["OTEL_METRICS_EXPORTER"] != "none" {
		status.Issues = append(status.Issues, "OTEL_METRICS_EXPORTER must be none; the local receiver does not store metrics")
	}
	for _, key := range []string{"OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_DETAILS", "OTEL_LOG_TOOL_CONTENT", "OTEL_LOG_MANAGED_SETTINGS", "OTEL_LOG_RAW_API_BODIES"} {
		if exportEnabled(values[key]) || (key == "OTEL_LOG_RAW_API_BODIES" && strings.HasPrefix(values[key], "file:")) {
			status.Issues = append(status.Issues, key+" enables content capture; disable it for the local trace pilot")
		}
	}
	if exportEnabled(values["ENABLE_BETA_TRACING_DETAILED"]) && values["BETA_TRACING_ENDPOINT"] != "" {
		status.Issues = append(status.Issues, "detailed beta tracing can override the logs and traces destination; disable ENABLE_BETA_TRACING_DETAILED for this pilot")
	}
	status.Ready = len(status.Issues) == 0
	return status
}

func exportVariable(key string) bool {
	return strings.HasPrefix(key, "OTEL_") || key == "CLAUDE_CODE_ENABLE_TELEMETRY" || key == "CLAUDE_CODE_ENHANCED_TELEMETRY_BETA" || key == "ENABLE_ENHANCED_TELEMETRY_BETA" || key == "ENABLE_BETA_TRACING_DETAILED" || key == "BETA_TRACING_ENDPOINT"
}

func exportEnabled(value string) bool { return value == "1" || strings.EqualFold(value, "true") }

func localExportEndpoint(value string, port int, path string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != strconv.Itoa(port) {
		return false
	}
	// The receiver binds IPv4 loopback. Other 127/8 addresses and ::1 need not
	// reach that listener; localhost is supported by the HTTP client's fallback.
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		return false
	}
	if path == "" {
		return u.Path == "" || u.Path == "/"
	}
	return u.Path == path
}
