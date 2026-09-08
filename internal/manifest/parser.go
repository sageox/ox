package manifest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

var (
	ErrNoVersion      = errors.New("manifest: missing version directive")
	ErrUnknownVersion = errors.New("manifest: unknown version")
)

// Parse reads a sync manifest from r and returns the parsed config.
// Returns an error if the version is missing or unsupported.
func Parse(r io.Reader) (*ManifestConfig, error) {
	cfg := &ManifestConfig{
		SyncIntervalMin: DefaultSyncIntervalMin,
		GCIntervalDays:  DefaultGCIntervalDays,
	}

	// track include/deny sets for last-one-wins semantics
	includeSet := make(map[string]bool)
	denySet := make(map[string]bool)
	var resolveRules []ResolveRule // explicit `resolve <mode> <path>` directives
	hasResolve := false            // tracks whether any resolve directive was specified
	versionSeen := false

	scanner := bufio.NewScanner(r)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			slog.Warn("manifest: skipping malformed line", "line", lineNum, "content", line)
			continue
		}

		directive := parts[0]
		value := strings.Join(parts[1:], " ")

		switch directive {
		case "version":
			v, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("%w: %q", ErrUnknownVersion, value)
			}
			if v != SupportedVersion {
				return nil, fmt.Errorf("%w: %d", ErrUnknownVersion, v)
			}
			cfg.Version = v
			versionSeen = true

		case "include":
			if err := validatePath(value, lineNum); err != nil {
				continue
			}
			includeSet[value] = true
			delete(denySet, value) // last-one-wins

		case "deny":
			if err := validatePath(value, lineNum); err != nil {
				continue
			}
			denySet[value] = true
			delete(includeSet, value) // last-one-wins

		case "sync_interval_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				slog.Warn("manifest: invalid sync_interval_minutes", "line", lineNum, "value", value)
				continue
			}
			if n < MinSyncIntervalMin {
				n = MinSyncIntervalMin
			}
			cfg.SyncIntervalMin = n

		case "gc_interval_days":
			n, err := strconv.Atoi(value)
			if err != nil {
				slog.Warn("manifest: invalid gc_interval_days", "line", lineNum, "value", value)
				continue
			}
			if n < MinGCIntervalDays {
				n = MinGCIntervalDays
			}
			if n > MaxGCIntervalDays {
				n = MaxGCIntervalDays
			}
			cfg.GCIntervalDays = n

		case "resolve":
			// resolve <mode> <path>
			if len(parts) < 3 {
				slog.Warn("manifest: resolve directive requires mode and path", "line", lineNum)
				continue
			}
			mode := ResolveMode(parts[1])
			rpath := parts[2]
			if mode != ResolveModeAuto && mode != ResolveModeNone {
				slog.Warn("manifest: unknown resolve mode, skipping", "line", lineNum, "mode", mode)
				continue
			}
			if err := validatePath(rpath, lineNum); err != nil {
				continue
			}
			resolveRules = append(resolveRules, ResolveRule{Mode: mode, Path: rpath})
			hasResolve = true

		default:
			slog.Warn("manifest: unknown directive, skipping", "line", lineNum, "directive", directive)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("manifest: read error: %w", err)
	}

	if !versionSeen {
		return nil, ErrNoVersion
	}

	for path := range includeSet {
		cfg.Includes = append(cfg.Includes, path)
	}
	for path := range denySet {
		cfg.Denies = append(cfg.Denies, path)
	}

	// resolve rules: use explicit directives if present, otherwise fall
	// back to DefaultResolveRules so existing manifests don't need
	// updating to get safe conflict resolution.
	if hasResolve {
		cfg.ResolveRules = ValidateResolveRules(resolveRules)
	} else {
		rules := make([]ResolveRule, len(DefaultResolveRules))
		copy(rules, DefaultResolveRules)
		cfg.ResolveRules = rules
	}

	return cfg, nil
}

// ParseFile parses a manifest from a file path. On any error (missing
// file, parse error, unknown version), it returns the fallback config for
// kind and logs a warning.
//
// kind is required and must match the repo being parsed. Falling back to the
// wrong kind's include set is silent and total — the checkout simply omits an
// entire tree — so every warning below names the kind it fell back to.
func ParseFile(path string, kind RepoKind) *ManifestConfig {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			slog.Warn("manifest: file not found, using fallback", "path", path, "kind", string(kind))
		} else {
			slog.Warn("manifest: cannot open file, using fallback", "path", path, "kind", string(kind), "error", err)
		}
		return FallbackConfigFor(kind)
	}
	defer f.Close()

	cfg, err := Parse(f)
	if err != nil {
		slog.Warn("manifest: parse failed, using fallback", "path", path, "kind", string(kind), "error", err)
		return FallbackConfigFor(kind)
	}

	if len(cfg.Includes) == 0 {
		slog.Warn("manifest: no include directives, using fallback", "path", path, "kind", string(kind))
		return FallbackConfigFor(kind)
	}

	return cfg
}

// ComputeSparseSet returns the effective sparse checkout paths:
// includes minus any paths that match a deny prefix. Deny always wins.
//
// The result always starts with "/*" and "!/*/" to include root-level files
// (like .gitattributes) without pulling in root-level directories. Specific
// directories from includes are then re-added. This ensures root-level
// control files are materialized in sparse-checkout --no-cone mode.
//
// Every include is root-anchored on the way out (see anchorPattern) so a
// manifest entry names exactly one path in the repo, never a same-named path
// nested somewhere else.
func ComputeSparseSet(cfg *ManifestConfig) []string {
	if cfg == nil {
		return nil
	}

	denySet := make(map[string]bool, len(cfg.Denies))
	for _, d := range cfg.Denies {
		denySet[d] = true
	}

	// start with root-level files: /* includes everything at root,
	// !/*/ excludes root-level directories only (re-included explicitly below).
	// The leading / anchors to the repo root so subdirectories inside
	// included paths (e.g., memory/daily/) are not accidentally excluded.
	result := []string{"/*", "!/*/"}

	for _, inc := range cfg.Includes {
		if denySet[inc] {
			continue
		}
		// check if any deny overlaps this include (parent, child, or exact)
		denied := false
		for _, d := range cfg.Denies {
			if pathOverlaps(d, inc) {
				denied = true
				break
			}
		}
		if !denied {
			result = append(result, anchorPattern(inc))
		}
	}

	return result
}

// anchorPattern pins a manifest include to the repo root.
//
// --no-cone sparse-checkout uses gitignore matching semantics, where a
// pattern whose only slash is trailing (or which has no slash at all) matches
// at ANY depth. So a bare "AGENTS.md" also matched knowledge/agents.md — on a
// case-insensitive filesystem, no less — leaking a single file out of an
// otherwise-excluded directory and making a total sparse failure look partial.
//
// A leading "/" makes the pattern relative to the repo root. Patterns with an
// interior slash are already root-relative under the same rules, so anchoring
// them is a semantic no-op; we still normalize for a uniform sparse file.
func anchorPattern(p string) string {
	if p == "" || strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

// EnsureSageoxInclude appends the control-plane ".sageox/" pattern to paths
// unless some form of it is already present.
//
// .sageox/ holds kb.yaml and sync.manifest itself. Dropping it from the
// sparse set hides the manifest, so the next pull's sparse reapply has
// nothing to read and the checkout can never recover on its own. Clone and
// doctor-repair both need this guarantee, so it lives here rather than being
// re-implemented at each call site.
func EnsureSageoxInclude(paths []string) []string {
	for _, p := range paths {
		switch p {
		case ".sageox", ".sageox/", "/.sageox", "/.sageox/":
			return paths
		}
	}
	// appended, never prepended: ComputeSparseSet emits "!/*/" to drop all
	// root-level directories, and in --no-cone mode later patterns override
	// earlier ones, so .sageox/ must come after it to be re-included.
	return append(paths, "/.sageox/")
}

// requiredTeamContextDirs are directories ox ITSELF reads out of a team-context
// checkout. They are not a preference: if one is missing from the sparse set,
// the feature that reads it is silently dead on every client.
//
// agents/ holds team rules (teamdocs.DiscoverRules) and team skills. The
// server-generated sync.manifest has omitted it — GH #862 — so sparse-checkout
// never materialized the directory and DiscoverRules walked an empty tree. No
// error surfaced anywhere: an empty tree and "this team has no rules" are the
// same value, which is why it went unnoticed. The client fallback include set
// lists agents/, but the TRACKED manifest wins whenever one exists.
var requiredTeamContextDirs = []string{"agents/"}

// EnsureRequiredIncludes floors the sparse set with the directories ox needs for
// this repo kind, whatever the server-generated manifest happens to list.
//
// This is a floor, not an override: it only ADDS, and only when the path is not
// already covered. A manifest that lists a required directory — under any of the
// leading-slash or trailing-slash spellings — is left exactly as it is.
//
// The durable fix for a manifest that omits a required directory is server-side.
// This keeps a client working in the meantime rather than failing silently, and
// costs nothing once the manifest is correct.
func EnsureRequiredIncludes(paths []string, kind RepoKind) []string {
	if kind != RepoKindTeamContext {
		return paths
	}
	for _, want := range requiredTeamContextDirs {
		if includesPath(paths, want) {
			continue
		}
		// Appended, never prepended, for the same reason as EnsureSageoxInclude:
		// ComputeSparseSet emits "!/*/" to drop root-level directories, and in
		// --no-cone mode later patterns override earlier ones.
		paths = append(paths, "/"+strings.TrimSuffix(want, "/")+"/")
	}
	return paths
}

// includesPath reports whether want is already covered by paths, tolerating the
// leading- and trailing-slash spellings a hand-edited manifest may use.
func includesPath(paths []string, want string) bool {
	norm := func(p string) string {
		return strings.Trim(strings.TrimSpace(p), "/")
	}
	target := norm(want)
	if target == "" {
		return true
	}
	for _, p := range paths {
		if norm(p) == target {
			return true
		}
	}
	return false
}

// pathOverlaps returns true if a and b overlap: same path, or one is a
// parent directory of the other.
func pathOverlaps(a, b string) bool {
	if a == b {
		return true
	}
	if strings.HasSuffix(a, "/") && strings.HasPrefix(b, a) {
		return true
	}
	if strings.HasSuffix(b, "/") && strings.HasPrefix(a, b) {
		return true
	}
	return false
}

func validatePath(path string, lineNum int) error {
	if strings.Contains(path, "..") {
		slog.Warn("manifest: rejecting path with traversal", "line", lineNum, "path", path)
		return fmt.Errorf("path traversal")
	}
	return nil
}
