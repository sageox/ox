package adapters

import (
	"os"
	"path/filepath"
	"strings"
)

// KnownSessionRoots returns the absolute path prefixes that legitimately hold
// session files for the named adapter. Used by the daemon to gate IPC-supplied
// SessionFile values against a same-UID peer that wants the watcher to tail
// arbitrary absolute paths (e.g., ~/.ssh/id_rsa, ~/.sageox/auth.json) and
// exfiltrate them via the normal session-finalize pipeline.
//
// All ox adapters are shipped in the same release as the ox CLI itself, so the
// set of legitimate session-file roots is fully known at compile time. This
// keeps the trust boundary tight: the daemon does not need to consult adapter
// binaries for path policy, and a malicious external adapter cannot widen the
// allow-list at runtime.
//
// Returns nil for unknown adapter names; callers MUST treat nil as "reject"
// rather than "allow any" (fail-closed).
//
// homeDir is the absolute path to the caller's home directory ($HOME). Passing
// it in keeps the function pure and testable; production callers should pass
// os.UserHomeDir() — KnownSessionRootsForCurrentUser does that for them.
func KnownSessionRoots(adapterName, homeDir string) []string {
	if homeDir == "" {
		return nil
	}
	canonical := CanonicalAdapterName(adapterName)
	roots, ok := adapterSessionRoots[canonical]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, filepath.Join(homeDir, r))
	}
	return out
}

// KnownSessionRootsForCurrentUser is a convenience wrapper that resolves
// $HOME via os.UserHomeDir.
func KnownSessionRootsForCurrentUser(adapterName string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return KnownSessionRoots(adapterName, home)
}

// IsSessionFileAllowed reports whether sessionFile is under one of the
// recorded roots for the named adapter. sessionFile must be an absolute
// path; relative paths are rejected outright (no implicit home expansion
// — IPC peers send absolute paths in practice).
//
// Returns false when the adapter is unknown, the path is not absolute, or
// the path falls outside every allowed root.
func IsSessionFileAllowed(adapterName, sessionFile, homeDir string) bool {
	if sessionFile == "" || !filepath.IsAbs(sessionFile) {
		return false
	}
	roots := KnownSessionRoots(adapterName, homeDir)
	if len(roots) == 0 {
		return false
	}
	clean := filepath.Clean(sessionFile)
	for _, root := range roots {
		root = filepath.Clean(root)
		if clean == root {
			return true
		}
		if strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// adapterSessionRoots is the canonical map of adapter name → ~-relative path
// prefixes where that adapter's session files live. New adapters MUST add an
// entry here or the daemon will reject all SessionFile values supplied for
// that adapter (fail-closed).
//
// Keys are canonical adapter names as returned by CanonicalAdapterName.
// Values are slash-separated, ~-relative; KnownSessionRoots joins them onto
// homeDir using the OS-appropriate separator.
var adapterSessionRoots = map[string][]string{
	"claude-code": {".claude/projects"},
	"codex":       {".codex/sessions"},
	"gemini":      {".gemini/tmp", ".gemini/sessions"},
	// Generic / non-deep adapters store recordings inside the ox cache directory
	// under the user's home — same fail-closed root as the deep adapters.
	"generic":  {".sageox/cache/sessions", ".cache/sageox/sessions"},
	"amp":      {".sageox/cache/sessions", ".cache/sageox/sessions"},
	"opencode": {".sageox/cache/sessions", ".cache/sageox/sessions"},
	"pi":       {".sageox/cache/sessions", ".cache/sageox/sessions"},
	"aider":    {".sageox/cache/sessions", ".cache/sageox/sessions"},
	"droid":    {".sageox/cache/sessions", ".cache/sageox/sessions"},
}
