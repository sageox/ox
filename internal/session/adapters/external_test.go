package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeBinary creates a shell script that echoes canned responses.
func fakeBinary(t *testing.T, responses map[string]string) string {
	t.Helper()
	script := "#!/bin/sh\ncase \"$1\" in\n"
	for cmd, resp := range responses {
		script += "  " + cmd + ") echo '" + resp + "';;\n"
	}
	script += "  *) echo '{\"error\":\"unknown subcommand\"}'; exit 1;;\nesac\n"
	f := filepath.Join(t.TempDir(), "ox-adapter-fake")
	if err := os.WriteFile(f, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestExternalAdapter_InfoAndName(t *testing.T) {
	binary := fakeBinary(t, map[string]string{
		"info": `{"protocol_version":1,"name":"test-agent","display_name":"Test Agent","version":"0.1.0","type":"session","capabilities":["session_reader"],"serve_mode":false}`,
	})

	ea, err := NewExternalAdapter(binary)
	if err != nil {
		t.Fatalf("NewExternalAdapter: %v", err)
	}
	if ea.Name() != "test-agent" {
		t.Errorf("Name = %q, want test-agent", ea.Name())
	}
	if ea.Info().Type != "session" {
		t.Errorf("Type = %q, want session", ea.Info().Type)
	}
}

func TestExternalAdapter_Detect(t *testing.T) {
	binary := fakeBinary(t, map[string]string{
		"info":   `{"protocol_version":1,"name":"test","version":"0.1.0","type":"session"}`,
		"detect": `{"detected":true,"reason":"found config"}`,
	})

	ea, err := NewExternalAdapter(binary)
	if err != nil {
		t.Fatalf("NewExternalAdapter: %v", err)
	}
	if !ea.Detect() {
		t.Error("expected Detect() = true")
	}
}

func TestExternalAdapter_DetectFalse(t *testing.T) {
	binary := fakeBinary(t, map[string]string{
		"info":   `{"protocol_version":1,"name":"test","version":"0.1.0","type":"session"}`,
		"detect": `{"detected":false,"reason":"not found"}`,
	})

	ea, err := NewExternalAdapter(binary)
	if err != nil {
		t.Fatalf("NewExternalAdapter: %v", err)
	}
	if ea.Detect() {
		t.Error("expected Detect() = false")
	}
}

func TestExternalAdapter_Read(t *testing.T) {
	binary := fakeBinary(t, map[string]string{
		"info": `{"protocol_version":1,"name":"test","version":"0.1.0","type":"session"}`,
		"read": `{"entries":[{"timestamp":"2026-04-02T10:30:00Z","role":"user","content":"hello"}],"metadata":{"agent_version":"1.0"}}`,
	})

	ea, err := NewExternalAdapter(binary)
	if err != nil {
		t.Fatalf("NewExternalAdapter: %v", err)
	}

	entries, err := ea.Read("/any/path")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Role != "user" {
		t.Errorf("Role = %q, want user", entries[0].Role)
	}
	if entries[0].Content != "hello" {
		t.Errorf("Content = %q, want hello", entries[0].Content)
	}
}

func TestExternalAdapter_ReadMetadata(t *testing.T) {
	binary := fakeBinary(t, map[string]string{
		"info":          `{"protocol_version":1,"name":"test","version":"0.1.0","type":"session"}`,
		"read-metadata": `{"agent_version":"1.2.3","model":"claude-sonnet-4-20250514"}`,
	})

	ea, err := NewExternalAdapter(binary)
	if err != nil {
		t.Fatalf("NewExternalAdapter: %v", err)
	}

	meta, err := ea.ReadMetadata("/any/path")
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}
	if meta.AgentVersion != "1.2.3" {
		t.Errorf("AgentVersion = %q, want 1.2.3", meta.AgentVersion)
	}
	if meta.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want claude-sonnet-4-20250514", meta.Model)
	}
}

func TestExternalAdapter_ReadFromOffset(t *testing.T) {
	binary := fakeBinary(t, map[string]string{
		"info":             `{"protocol_version":1,"name":"test","version":"0.1.0","type":"session","capabilities":["incremental_reader"]}`,
		"read-from-offset": `{"entries":[{"timestamp":"2026-04-02T10:30:00Z","role":"user","content":"hi"}],"new_offset":42}`,
	})

	ea, err := NewExternalAdapter(binary)
	if err != nil {
		t.Fatalf("NewExternalAdapter: %v", err)
	}

	entries, newOffset, err := ea.ReadFromOffset("/any/path", 0)
	if err != nil {
		t.Fatalf("ReadFromOffset: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if newOffset != 42 {
		t.Errorf("newOffset = %d, want 42", newOffset)
	}
}

func TestExternalAdapter_BinaryNotFound(t *testing.T) {
	_, err := NewExternalAdapter("/nonexistent/ox-adapter-ghost")
	if err == nil {
		t.Error("expected error for missing binary")
	}
}

func TestExternalAdapter_BinaryNotExecutable(t *testing.T) {
	f := filepath.Join(t.TempDir(), "ox-adapter-noexec")
	if err := os.WriteFile(f, []byte("#!/bin/sh\necho hi"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := NewExternalAdapter(f)
	if err == nil {
		t.Error("expected error for non-executable binary")
	}
}

func TestExternalAdapter_InvalidJSON(t *testing.T) {
	script := filepath.Join(t.TempDir(), "ox-adapter-badjson")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'not json'"), 0755); err != nil {
		t.Fatal(err)
	}

	_, err := NewExternalAdapter(script)
	if err == nil {
		t.Error("expected error for invalid JSON response")
	}
}

func TestExternalAdapter_WrongProtocolVersion(t *testing.T) {
	// protocol version 0 should be rejected (we require >= 1)
	binary := fakeBinary(t, map[string]string{
		"info": `{"protocol_version":0,"name":"old","version":"0.1.0","type":"session"}`,
	})

	ea, err := NewExternalAdapter(binary)
	if err != nil {
		t.Fatalf("NewExternalAdapter: %v", err)
	}

	// the adapter is created but discovery would reject it
	if ea.Info().ProtocolVersion >= 1 {
		t.Error("expected protocol version < 1")
	}
}

func TestExternalAdapter_Diagnose(t *testing.T) {
	binary := fakeBinary(t, map[string]string{
		"info":     `{"protocol_version":1,"name":"test","version":"0.1.0","type":"session"}`,
		"diagnose": `{"ok":false,"issues":[{"slug":"hooks-missing","severity":"error","title":"hooks missing","detail":"no hooks","fix":"ox integrate install","fix_safe":true}]}`,
	})

	ea, err := NewExternalAdapter(binary)
	if err != nil {
		t.Fatalf("NewExternalAdapter: %v", err)
	}

	result, err := ea.Diagnose("/tmp/repo", "project")
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false")
	}
	if len(result.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(result.Issues))
	}
	if result.Issues[0].Slug != "hooks-missing" {
		t.Errorf("slug = %q, want hooks-missing", result.Issues[0].Slug)
	}
}

func TestExternalAdapter_Watch_NotSupported(t *testing.T) {
	binary := fakeBinary(t, map[string]string{
		"info": `{"protocol_version":1,"name":"test","version":"0.1.0","type":"session"}`,
	})

	ea, err := NewExternalAdapter(binary)
	if err != nil {
		t.Fatalf("NewExternalAdapter: %v", err)
	}

	_, err = ea.Watch(nil, "/any/path")
	if err != ErrWatchNotSupported {
		t.Errorf("Watch error = %v, want ErrWatchNotSupported", err)
	}
}
