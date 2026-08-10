package adapters

import (
	"errors"
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

// IsSessionFileAllowed reports whether sessionFile is something the named
// adapter may legitimately hand the daemon: either a path under one of that
// adapter's recorded roots, or an opaque handle for the adapters that do not
// tail a file at all (see IsOpaqueSessionHandle).
//
// Paths must be absolute; relative paths are rejected outright (no implicit
// home expansion — IPC peers send absolute paths in practice).
//
// Returns false when the adapter is unknown, the path is not absolute, or
// the path falls outside every allowed root.
func IsSessionFileAllowed(adapterName, sessionFile, homeDir string) bool {
	if sessionFile == "" {
		return false
	}
	// opencode and goose read from a SQLite database, so their find-session
	// returns an id, not a path. Rejecting those as "not absolute" silently
	// killed recording for both agents.
	if IsOpaqueSessionHandle(adapterName, sessionFile) {
		return true
	}
	if !filepath.IsAbs(sessionFile) {
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

// Errors returned by SafeSessionFilePath so callers can tell "not allowed"
// from "could not be checked" and log accordingly.
var (
	ErrSessionFileNotAllowed = errors.New("session file is outside the adapter's allowed roots")
	ErrSessionFileEscapes    = errors.New("session file resolves outside the adapter's allowed roots")
)

// SafeSessionFilePath validates sessionFile and returns the path a caller
// should actually open.
//
// IsSessionFileAllowed is lexical, but the tail loop opens the path with
// os.Open, which follows symlinks. A symlink planted inside an allowed root —
// say ~/.pi/agent/sessions/notes.jsonl pointing at ~/.ssh/id_rsa — passes a
// lexical check and then gets read and uploaded through the session pipeline.
// Resolving the path and re-checking closes that.
//
// Opaque handles are returned unchanged: they are not paths and are never
// opened, so there is nothing to resolve.
//
// This narrows the window but does not close it entirely — a symlink swapped
// between this check and the open still wins. Callers that need that
// guarantee must open with O_NOFOLLOW and validate the descriptor. Tracked
// separately; this function is the containment check, not a TOCTOU defense.
func SafeSessionFilePath(adapterName, sessionFile, homeDir string) (string, error) {
	if IsOpaqueSessionHandle(adapterName, sessionFile) {
		return sessionFile, nil
	}
	if !IsSessionFileAllowed(adapterName, sessionFile, homeDir) {
		return "", ErrSessionFileNotAllowed
	}

	resolved, err := filepath.EvalSymlinks(sessionFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return pendingSessionFilePath(adapterName, sessionFile, homeDir)
		}
		return "", err
	}

	// The roots themselves may sit behind a symlink (a symlinked $HOME is
	// common on managed machines), so compare resolved against resolved
	// rather than declaring every such setup an escape.
	if !isUnderResolvedRoot(adapterName, resolved, homeDir) {
		return "", ErrSessionFileEscapes
	}
	return resolved, nil
}

// pendingSessionFilePath handles the case where EvalSymlinks reported that
// something on the path does not exist.
//
// A fresh recording names its transcript before the agent creates it, so this
// has to succeed for the ordinary case. But EvalSymlinks reports IsNotExist for
// a DANGLING SYMLINK too, and treating that as "not created yet" hands the
// caller an unresolved link: a peer plants
// ~/.pi/agent/sessions/notes.jsonl -> ~/.aws/credentials, which does not exist
// yet, gets it accepted, then creates the target and the daemon follows the
// link for the life of the session. No race needed.
//
// So: refuse anything whose final component is already a symlink, and prove the
// PARENT directory is contained — a parent that resolves outside the root is
// the same escape one level up.
func pendingSessionFilePath(adapterName, sessionFile, homeDir string) (string, error) {
	if li, err := os.Lstat(sessionFile); err == nil && li.Mode()&os.ModeSymlink != 0 {
		// it exists and is a link; EvalSymlinks failed because its TARGET is
		// missing, which is a plant, not a pending transcript
		return "", ErrSessionFileEscapes
	}

	parent, err := filepath.EvalSymlinks(filepath.Dir(sessionFile))
	if err != nil {
		// the directory is missing too — nothing to validate against, so the
		// containment claim cannot be made
		return "", ErrSessionFileEscapes
	}
	if !isUnderResolvedRoot(adapterName, parent, homeDir) {
		return "", ErrSessionFileEscapes
	}
	return filepath.Join(parent, filepath.Base(sessionFile)), nil
}

func isUnderResolvedRoot(adapterName, resolvedFile, homeDir string) bool {
	for _, root := range KnownSessionRoots(adapterName, homeDir) {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue // a root that does not exist cannot contain anything
		}
		if resolvedFile == resolvedRoot ||
			strings.HasPrefix(resolvedFile, filepath.Clean(resolvedRoot)+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// IsOpaqueSessionHandle reports whether sessionFile is a well-formed opaque
// handle for the named adapter — the "<adapter>:<id>" form used by adapters
// that read from a database rather than tailing a file on disk.
//
// The allow-list exists to stop a same-UID IPC peer naming an arbitrary file
// for the daemon to read and upload. An opaque handle is safe from that by
// construction: it is resolved by the adapter's own reader and is never opened
// as a path. That safety depends entirely on the id being unable to express a
// path, so anything that could be — a separator, a parent reference, a null
// byte — is rejected here rather than trusted downstream.
func IsOpaqueSessionHandle(adapterName, sessionFile string) bool {
	prefix, ok := adapterSessionHandles[CanonicalAdapterName(adapterName)]
	if !ok {
		return false
	}
	id, found := strings.CutPrefix(sessionFile, prefix)
	if !found || id == "" {
		return false
	}
	// no separators (either platform's), no traversal, no NUL smuggling
	if strings.ContainsAny(id, `/\`+"\x00") || strings.Contains(id, "..") {
		return false
	}
	return true
}

// adapterSessionHandles declares the adapters whose find-session returns an
// opaque "<adapter>:<id>" handle instead of a path, and the exact prefix each
// one emits. An adapter absent from this map may only supply paths.
var adapterSessionHandles = map[string]string{
	// stores sessions in ~/.local/share/opencode/opencode.db
	"opencode": "opencode:",
	// stores sessions in ~/.local/share/goose/sessions/sessions.db
	"goose": "goose:",
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
	"generic": {".sageox/cache/sessions", ".cache/sageox/sessions"},
	// Amp has no lifecycle hooks, so ox's embedded ox-bridge.ts plugin writes
	// a per-thread JSONL sidecar and ox tails it in place. Neither of those
	// paths was listed, so the daemon rejected every amp session it found.
	"amp": {
		".sageox/cache/sessions", ".cache/sageox/sessions",
		".cache/amp/ox-sessions", // written by cmd/ox-adapter-amp/plugin/ox-bridge.ts
		".amp/sessions",          // pre-2026 Amp installs
	},
	"opencode": {".sageox/cache/sessions", ".cache/sageox/sessions"},
	// Pi has no lifecycle hooks, so ox tails its transcript in place; the
	// adapter hands back a path under ~/.pi/agent/sessions and the daemon
	// rejected every one of them until this root was listed.
	"pi":    {".sageox/cache/sessions", ".cache/sageox/sessions", ".pi/agent/sessions"},
	"aider": {".sageox/cache/sessions", ".cache/sageox/sessions"},
	// Droid writes transcripts to ~/.factory/sessions/<project-slug>/<uuid>.jsonl
	// and ox tails them in place, same as pi.
	"droid": {".sageox/cache/sessions", ".cache/sageox/sessions", ".factory/sessions"},
	"goose": {".sageox/cache/sessions", ".cache/sageox/sessions", ".local/share/goose/sessions"},
}
