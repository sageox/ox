package main

import (
	"strings"
	"testing"
)

func TestDetectInstallMethod_DevBuild(t *testing.T) {
	// detectInstallMethod checks version.BuildDate and version.Version
	// which are set via ldflags. In test builds, BuildDate is "unknown"
	// so it should return installSource.
	method := detectInstallMethod()
	if method != installSource {
		t.Errorf("expected installSource for test build, got %s", method)
	}
}

func TestIsHomebrewInstall(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		expect bool
	}{
		{"homebrew arm64", "/opt/homebrew/bin/ox", true},
		{"homebrew x86", "/usr/local/Cellar/ox/0.5.1/bin/ox", true},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/bin/ox", true},
		{"gobin", "/Users/dev/go/bin/ox", false},
		{"random", "/usr/local/bin/ox", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// test the prefix-based fast path using the real homebrewPrefixes var
			got := hasHomebrewPrefix(tt.path)
			if got != tt.expect {
				t.Errorf("hasHomebrewPrefix(%q) = %v, want %v", tt.path, got, tt.expect)
			}
		})
	}
}

// hasHomebrewPrefix checks the fast-path prefix match using the real
// homebrewPrefixes package var, without shelling out to brew.
func hasHomebrewPrefix(oxPath string) bool {
	for _, prefix := range homebrewPrefixes {
		if strings.HasPrefix(oxPath, prefix) {
			return true
		}
	}
	return false
}
