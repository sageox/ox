package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCredentialIntegrity_ValidFiles(t *testing.T) {
	configDir := t.TempDir()
	os.WriteFile(filepath.Join(configDir, "auth.json"), []byte(`{"tokens":{}}`), 0600)
	os.WriteFile(filepath.Join(configDir, "git-credentials.json"), []byte(`{"token":"ok"}`), 0600)

	result := checkCredentialIntegrityInDir(configDir, false)
	if !result.passed {
		t.Errorf("expected passed for valid files, got: %+v", result)
	}
}

func TestCheckCredentialIntegrity_CorruptFile(t *testing.T) {
	configDir := t.TempDir()
	os.WriteFile(filepath.Join(configDir, "auth.json"), []byte("{corrupt!!!"), 0600)
	os.WriteFile(filepath.Join(configDir, "git-credentials.json"), []byte(`{"token":"ok"}`), 0600)

	result := checkCredentialIntegrityInDir(configDir, false)
	if !result.warning {
		t.Errorf("expected warning for corrupt file, got: %+v", result)
	}
	if !strings.Contains(result.message, "1 corrupt") {
		t.Errorf("expected '1 corrupt' in message, got: %s", result.message)
	}
}

func TestCheckCredentialIntegrity_FixRemovesCorrupt(t *testing.T) {
	configDir := t.TempDir()
	corruptPath := filepath.Join(configDir, "auth.json")
	os.WriteFile(corruptPath, []byte("not json"), 0600)

	result := checkCredentialIntegrityInDir(configDir, true)
	if !result.passed {
		t.Errorf("expected passed after fix, got: %+v", result)
	}

	if _, err := os.Stat(corruptPath); !os.IsNotExist(err) {
		t.Error("corrupt file should have been removed")
	}
}

func TestCheckCredentialIntegrity_NoFiles(t *testing.T) {
	configDir := t.TempDir()
	result := checkCredentialIntegrityInDir(configDir, false)
	if !result.skipped {
		t.Errorf("expected skipped when no files exist, got: %+v", result)
	}
}

func TestCheckCredentialIntegrity_CorruptEndpointFile(t *testing.T) {
	configDir := t.TempDir()
	os.WriteFile(filepath.Join(configDir, "auth.json"), []byte(`{"tokens":{}}`), 0600)
	os.WriteFile(filepath.Join(configDir, "git-credentials-test.sageox.ai.json"), []byte("{{bad"), 0600)

	result := checkCredentialIntegrityInDir(configDir, false)
	if !result.warning {
		t.Errorf("expected warning for corrupt endpoint-specific file, got: %+v", result)
	}
}
