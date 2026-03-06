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

	return cfg, nil
}

// ParseFile parses a manifest from a file path. On any error (missing
// file, parse error, unknown version), it returns the fallback config
// and logs a warning.
func ParseFile(path string) *ManifestConfig {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("manifest: file not found, using fallback", "path", path)
		} else {
			slog.Warn("manifest: cannot open file, using fallback", "path", path, "error", err)
		}
		return FallbackConfig()
	}
	defer f.Close()

	cfg, err := Parse(f)
	if err != nil {
		slog.Warn("manifest: parse failed, using fallback", "path", path, "error", err)
		return FallbackConfig()
	}

	if len(cfg.Includes) == 0 {
		slog.Warn("manifest: no include directives, using fallback", "path", path)
		return FallbackConfig()
	}

	return cfg
}

// ComputeSparseSet returns the effective sparse checkout paths:
// includes minus any paths that match a deny prefix. Deny always wins.
func ComputeSparseSet(cfg *ManifestConfig) []string {
	if cfg == nil {
		return nil
	}

	denySet := make(map[string]bool, len(cfg.Denies))
	for _, d := range cfg.Denies {
		denySet[d] = true
	}

	var result []string
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
			result = append(result, inc)
		}
	}

	return result
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
