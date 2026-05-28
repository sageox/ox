package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoCustomerFacingOxTokenReintroduction guards against re-introduction of the
// legacy customer-facing OX_TOKEN / OX_ENDPOINT env var names anywhere in the
// source tree outside an allowlist that documents the rename.
//
// See sageox-mono ADR-047 ("Customer-Facing Env Var Namespace") for the
// canonical rule, and the ephemeral-mode ADR for the migration note.
//
// Failure prevented: a future contributor adds OX_TOKEN or OX_ENDPOINT thinking
// "ox CLI = OX_*", silently reintroducing the customer-facing namespace
// confusion this ADR was written to eliminate.
func TestNoCustomerFacingOxTokenReintroduction(t *testing.T) {
	repoRoot := findRepoRootForNamingTest(t)

	// Files that may legitimately mention the legacy names: the legacy-ignore
	// regression test, this guard, the ADR documenting the rename, the
	// ephemeral-mode ADR's migration note, and the CHANGELOG rename entry
	// (CHANGELOG.md is a symlink to cmd/ox/release_notes.md — filepath.Walk
	// visits the real file, so allowlist the real path).
	allowlist := map[string]bool{
		"internal/auth/env_token_test.go":   true,
		"internal/auth/env_naming_test.go":  true,
		"docs/ai/adr/adr-ephemeral-mode.md": true,
		"cmd/ox/release_notes.md":           true,
		"CHANGELOG.md":                      true,
	}

	bannedPatterns := []string{`"OX_TOKEN"`, `"OX_ENDPOINT"`, "`OX_TOKEN`", "`OX_ENDPOINT`"}

	skipDirs := map[string]bool{
		".git":         true,
		".context":     true, // conductor workspace scratch, gitignored
		"node_modules": true,
		"vendor":       true,
		"third_party":  true,
		"dist":         true,
		"build":        true,
	}

	var violations []string
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// only scan Go sources and Markdown docs
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if allowlist[rel] {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		text := string(data)
		for _, pat := range bannedPatterns {
			if strings.Contains(text, pat) {
				violations = append(violations, rel+": "+pat)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("customer-facing OX_TOKEN/OX_ENDPOINT references found "+
			"(use SAGEOX_TOKEN/SAGEOX_ENDPOINT; see sageox-mono ADR-047):\n%s",
			strings.Join(violations, "\n"))
	}
}

func findRepoRootForNamingTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// walk upward looking for go.mod (repo root marker)
	dir := wd
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find repo root (go.mod) starting from %s", wd)
	return ""
}
