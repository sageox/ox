// Package adapterstamp holds the frontmatter-aware drift-stamp helpers shared by
// ox's per-agent adapter binaries (ox-adapter-claude-code, ox-adapter-droid).
//
// Each adapter is its own `package main`, so they can't import each other. These
// helpers were previously copied byte-for-byte into both adapters' rules.go,
// meaning every bug fix had to be applied twice. They live here so the fix
// applies once.
//
// All four helpers work around the same agentx v0.1.10 limitation: agentx's stamp
// readers (ExtractCommandHash / IsRuleStale) only inspect the FIRST line of a
// file, but every rule/skill ox installs carries YAML frontmatter, which pushes
// the stamp below line 1. These helpers scan the whole file for the stamp and
// recompute staleness accordingly.
package adapterstamp

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/agentx"
)

// LooksStamped reports whether file content carries the agentx stamp
// prefix anywhere, not just the first line. Works around agentx
// ExtractCommandHash only inspecting the first line — which fails on
// files with YAML frontmatter (the very files the adapters install).
func LooksStamped(data []byte) bool {
	hash, _, _ := ExtractStampAnywhere(data, agentx.DefaultStampPrefix)
	return hash != ""
}

// ExtractStampAnywhere finds the agentx stamp on ANY line of the file (not just
// line 1) and returns the 12-char content hash, the stamped version, and the
// body that follows the stamp line (the content the hash covers — i.e. without
// frontmatter or the stamp line itself). Returns empty strings when no stamp is
// present. This generalizes agentx.ExtractCommandHash / ExtractStampVersion,
// which only inspect the first line and therefore miss stamps that sit below
// YAML frontmatter.
//
// The rules managers stamp with agentx.DefaultStampPrefix ("agentx"), NOT the
// "ox" prefix used for command files — callers must pass that prefix to match
// what is actually on disk.
func ExtractStampAnywhere(data []byte, prefix string) (hash, version string, body []byte) {
	comment := agentx.StampComment(prefix)
	s := string(data)
	for idx := 0; idx < len(s); {
		end := strings.IndexByte(s[idx:], '\n')
		var line string
		if end < 0 {
			line = s[idx:]
		} else {
			line = s[idx : idx+end]
		}
		if strings.HasPrefix(line, comment) {
			rest := strings.TrimPrefix(line, comment)
			if len(rest) < 12 {
				return "", "", nil
			}
			hash = rest[:12]
			const marker = " ver: "
			if vIdx := strings.Index(line, marker); vIdx >= 0 {
				version = strings.TrimSpace(strings.TrimSuffix(line[vIdx+len(marker):], " -->"))
			}
			// body is everything after this stamp line's newline; matches how
			// StampedContent prepends a single "<stamp>\n" before rule.Content.
			if end >= 0 {
				body = []byte(s[idx+end+1:])
			}
			return hash, version, body
		}
		if end < 0 {
			break
		}
		idx += end + 1
	}
	return "", "", nil
}

// RemoveTamperedRules deletes installed rule files whose on-disk body no longer
// matches their own agentx stamp (the body was hand-edited). agentx's Install
// short-circuits on a matching stamp hash even when the body has drifted, so
// without this pre-pass `--fix` is a no-op on tampered files. User-managed files
// (no stamp) and files whose body still matches their stamp are left untouched.
func RemoveTamperedRules(rulesDir string, ruleFiles []agentx.RuleFile) {
	for _, rf := range ruleFiles {
		path := filepath.Join(rulesDir, rf.Name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		stampHash, _, body := ExtractStampAnywhere(data, agentx.DefaultStampPrefix)
		if stampHash == "" {
			continue // user-managed — never touch
		}
		if agentx.ContentHash(body) != stampHash {
			_ = os.Remove(path) // body tampered: drop so Install writes fresh
		}
	}
}

// AppendFrontmatterStale recomputes staleness for installed rule files whose
// stamp sits below YAML frontmatter (so agentx's first-line-only check misses
// it). A rule is stale when EITHER:
//
//   - the on-disk body no longer matches its own stamp hash (the body was
//     hand-edited / tampered — the stamp covers the body WITHOUT frontmatter,
//     matching how buildContent stamps), OR
//   - the stamp hash differs from the body the live binary ships (the binary
//     was upgraded), subject to the same version-downgrade guard the command
//     staleness path uses: a rule installed by a NEWER binary is never reported
//     stale by an older one.
//
// Files already flagged missing or stale by Validate, and user-managed files
// with no stamp, are left alone.
func AppendFrontmatterStale(rulesDir string, ruleFiles []agentx.RuleFile, missing, stale []string) []string {
	already := make(map[string]bool, len(missing)+len(stale))
	for _, n := range missing {
		already[n] = true
	}
	for _, n := range stale {
		already[n] = true
	}

	for _, rf := range ruleFiles {
		if already[rf.Name] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(rulesDir, rf.Name))
		if err != nil {
			continue
		}
		stampHash, ver, body := ExtractStampAnywhere(data, agentx.DefaultStampPrefix)
		if stampHash == "" {
			continue // user-managed file (no stamp) — never flag
		}

		// (1) body tampered: on-disk body doesn't match its own stamp.
		bodyTampered := agentx.ContentHash(body) != stampHash

		// (2) binary drift: live binary ships different content than what's
		// stamped, unless the install came from a newer binary (downgrade guard).
		binaryDrift := stampHash != agentx.ContentHash(rf.Content)
		if binaryDrift && ver != "" && rf.Version != "" && agentx.CompareVersions(rf.Version, ver) {
			binaryDrift = false
		}

		if bodyTampered || binaryDrift {
			stale = append(stale, rf.Name)
			already[rf.Name] = true
		}
	}
	return stale
}
