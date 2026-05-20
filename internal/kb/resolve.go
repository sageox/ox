package kb

// resolve.go — the canonical "which KB am I in?" resolver.
//
// ADR-017 promotes the Knowledge Bubble (KB) to the unifying workspace
// primitive. Where today's code path-walks for .sageox/ and reads repo_id
// from config.json to discover the legacy ledger binding, this resolver
// walks up from cwd looking for a .sageox/ marker that names a kb_id and
// returns a KBBinding the caller can use to scope session recording,
// murmurs, prime envelopes, and any other KB-aware behavior.
//
// The resolver intentionally does NOT make network calls. KB type and
// slug enrichment from /api/v1/kb is a separate concern (see Merger in
// merge.go); resolve only inspects on-disk markers. Callers that need
// type/slug enrichment compose this with KBClient.ListBubbles.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/sageox/ox/internal/endpoint"
)

// KBBinding is the resolved binding for the directory tree rooted at Anchor.
// It is the only sanctioned answer to "which KB does this path belong to?" —
// session recording, murmurs, prime, etc. all consume this struct.
//
// Field semantics match ADR-017 §1, with one addition (Endpoint) called out
// below.
type KBBinding struct {
	// KBID is the immutable kb identifier (kb_xxx) from the binding file.
	// Required; empty values are treated as "no binding".
	KBID string

	// KBType is the kb_type bucket (personal|profile|team|repo|custom|channel).
	// The resolver does NOT populate this — it requires a kb-API lookup or
	// merge.go output to determine type from kb_id. Callers enrich as needed;
	// the field exists on the struct so the resolver's return value can carry
	// it once enrichment has happened.
	KBType string

	// Source is the relative path of the marker file that produced this
	// binding, either ".sageox/config.yaml" (current) or ".sageox/config.json"
	// (legacy). Used by doctor and `ox kb config --show-origin` to attribute
	// values to their on-disk source.
	Source string

	// Anchor is the absolute path of the directory containing the .sageox/
	// marker — i.e. the workspace root, not the marker file itself.
	Anchor string

	// Scope is "exclusive" (default) for now. ADR-017 §6 reserves "subtree"
	// for nested overrides; the v1 resolver rejects nested markers entirely.
	Scope string

	// Endpoint is the SageOx API endpoint this binding belongs to (normalized
	// via endpoint.NormalizeEndpoint). If the binding file carries an explicit
	// `endpoint:` field, that value is used; otherwise the resolver falls back
	// to endpoint.Get() so callers always have a non-empty value.
	//
	// Note: this field is not in ADR-017 §1's struct diagram but the bead
	// description (ox-z526) requires it for downstream consumers (doctor's
	// kb-binding-endpoint-mismatch check, multi-endpoint dispatch). It is
	// always populated by ResolveCurrentKB.
	Endpoint string
}

// Scope values. Only ScopeExclusive is honored in v1; ScopeSubtree is
// reserved and rejected at resolve time (see ErrSubtreeOverridesNotSupported).
const (
	ScopeExclusive = "exclusive"
	ScopeSubtree   = "subtree"
)

// Marker file names, in priority order within a single directory.
const (
	markerDir      = ".sageox"
	markerYAMLName = "config.yaml"
	markerJSONName = "config.json"
)

// ErrSubtreeOverridesNotSupported is returned when the resolver finds a
// .sageox/ marker that is itself nested inside another .sageox/-rooted tree.
// ADR-017 §6 reserves the design space for subtree overrides but v1 rejects
// them rather than ship undefined semantics around session state straddling
// a boundary.
var ErrSubtreeOverridesNotSupported = errors.New("subtree overrides are reserved in ADR-017 v1; nested .sageox/ markers are not supported")

// bindingFile is the on-disk shape of a .sageox/config.{yaml,json} payload.
// Workspace markers carry many more fields (repo_id, ledger settings, etc.) —
// the resolver only needs kb_id and the optional endpoint. Other fields are
// ignored here; ProjectConfig owns the full schema.
type bindingFile struct {
	KBID     string `json:"kb_id" yaml:"kb_id"`
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
}

// ResolveCurrentKB walks up from cwd searching for a .sageox/ marker and
// returns the nearest binding, or (nil, nil) if no marker is found.
//
// Return semantics:
//   - (nil, nil)   — no marker found between cwd and the filesystem root.
//     This is the normal "outside any KB-bound tree" case and is not an error.
//   - (b, nil)     — marker found, binding parsed successfully.
//   - (nil, err)   — filesystem read error, malformed binding file, or a
//     subtree override (ErrSubtreeOverridesNotSupported).
//
// Marker priority within a single directory: config.yaml > config.json. Once
// the resolver finds a marker, it then checks ancestors for additional
// markers — any nested-marker arrangement returns ErrSubtreeOverridesNotSupported
// rather than silently picking one.
//
// Endpoint resolution: if the binding file carries `endpoint:`, that value is
// used (normalized). Otherwise endpoint.Get() supplies the default. Callers
// can detect "no explicit endpoint" by comparing against the file source if
// they need to surface a kb-binding-endpoint-mismatch warning.
//
// This function does NOT make network calls. KBType remains empty; callers
// enrich via the kb merger or KBClient.ListBubbles.
func ResolveCurrentKB(cwd string) (*KBBinding, error) {
	if cwd == "" {
		return nil, nil
	}

	// resolve to an absolute, symlink-evaluated path so the upward walk is
	// deterministic. EvalSymlinks failure (e.g. dangling symlink) is
	// tolerated — fall back to the cleaned absolute path.
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve cwd: %w", err)
	}
	if evaled, err := filepath.EvalSymlinks(abs); err == nil {
		abs = evaled
	} else {
		abs = filepath.Clean(abs)
	}

	// walk upward looking for the nearest marker.
	anchor, source, payload, err := findNearestMarker(abs)
	if err != nil {
		return nil, err
	}
	if anchor == "" {
		return nil, nil
	}

	// check ancestors above the anchor for additional markers — nested
	// .sageox/ trees are reserved per ADR-017 §6 and rejected in v1.
	parent := filepath.Dir(anchor)
	if parent != anchor {
		nested, _, _, err := findNearestMarker(parent)
		if err != nil {
			return nil, err
		}
		if nested != "" {
			return nil, ErrSubtreeOverridesNotSupported
		}
	}

	if payload.KBID == "" {
		// a marker with no kb_id is a legacy ledger-only binding. ADR-017 §7
		// says we still resolve it (the kb_type=repo branch is synthesized
		// from the legacy ledger row), but we cannot return a KBBinding
		// without a kb_id. Treat as "no current KB" rather than an error so
		// pre-ADR-017 repos continue to no-op cleanly.
		return nil, nil
	}

	ep := endpoint.NormalizeEndpoint(payload.Endpoint)
	if ep == "" {
		// Prefer the project-aware fallback so the resolved endpoint
		// matches the repo's recorded endpoint (.sageox/config.json), not
		// whatever the global SAGEOX_ENDPOINT env var happens to be.
		// GetForProject falls back to endpoint.Get() internally when the
		// project config is missing, so this strictly widens correctness.
		ep = endpoint.NormalizeEndpoint(endpoint.GetForProject(anchor))
	}

	return &KBBinding{
		KBID:     payload.KBID,
		Source:   filepath.Join(markerDir, source),
		Anchor:   anchor,
		Scope:    ScopeExclusive,
		Endpoint: ep,
	}, nil
}

// IsWorkspace reports whether the binding's anchor is a full workspace
// (ledger + cache + indexing state) vs a binding-only tree (just config.yaml).
//
// A workspace has at least one of:
//   - .sageox/cache/  (codedb, whisper, session state)
//   - .sageox/ledger/ (ledger checkout symlink target)
//   - .sageox/kb/*    (per-project kb symlinks)
//
// Binding-only trees omit all three. Per ADR-017 §5, binding-only trees do
// NOT spawn a daemon; workspace trees do.
func (b *KBBinding) IsWorkspace() bool {
	if b == nil || b.Anchor == "" {
		return false
	}
	base := filepath.Join(b.Anchor, markerDir)

	// cache/ and ledger/ are direct workspace markers.
	for _, name := range []string{"cache", "ledger"} {
		if info, err := os.Stat(filepath.Join(base, name)); err == nil && info.IsDir() {
			return true
		}
	}

	// any entry under kb/ counts (symlinks are the canonical kind, but a
	// real directory created by a future tool also qualifies).
	if entries, err := os.ReadDir(filepath.Join(base, "kb")); err == nil && len(entries) > 0 {
		return true
	}

	return false
}

// findNearestMarker walks up from startDir looking for the nearest directory
// containing a parseable .sageox/ marker. Returns the anchor (the directory
// containing .sageox/), the marker filename (config.yaml or config.json), and
// the parsed payload. Returns ("", "", _, nil) if no marker is found between
// startDir and the filesystem root.
//
// Within a single directory, config.yaml is tried before config.json. A
// directory that contains .sageox/ but neither marker file is skipped (we
// keep walking upward) — this matches today's behavior where bare .sageox/
// directories left over from partial init don't constitute a binding.
func findNearestMarker(startDir string) (anchor, source string, payload bindingFile, err error) {
	current := startDir
	for {
		sageoxPath := filepath.Join(current, markerDir)
		info, statErr := os.Stat(sageoxPath)
		if statErr == nil && info.IsDir() {
			// try yaml first, then json
			for _, name := range []string{markerYAMLName, markerJSONName} {
				path := filepath.Join(sageoxPath, name)
				data, readErr := os.ReadFile(path)
				if errors.Is(readErr, os.ErrNotExist) {
					continue
				}
				if readErr != nil {
					return "", "", bindingFile{}, fmt.Errorf("read %s: %w", path, readErr)
				}
				parsed, parseErr := parseBinding(name, data)
				if parseErr != nil {
					return "", "", bindingFile{}, fmt.Errorf("parse %s: %w", path, parseErr)
				}
				return current, name, parsed, nil
			}
			// .sageox/ exists but neither marker is present — keep walking.
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			// surface unexpected stat errors (e.g. permission denied) rather
			// than silently skipping a directory.
			return "", "", bindingFile{}, fmt.Errorf("stat %s: %w", sageoxPath, statErr)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", "", bindingFile{}, nil
		}
		current = parent
	}
}

// parseBinding decodes a marker payload. The two formats share a struct shape
// since we only consume kb_id + endpoint; ProjectConfig owns the rest.
func parseBinding(name string, data []byte) (bindingFile, error) {
	var b bindingFile
	switch name {
	case markerYAMLName:
		if err := yaml.Unmarshal(data, &b); err != nil {
			return bindingFile{}, err
		}
	case markerJSONName:
		if err := json.Unmarshal(data, &b); err != nil {
			return bindingFile{}, err
		}
	default:
		return bindingFile{}, fmt.Errorf("unknown marker format: %s", name)
	}
	return b, nil
}
