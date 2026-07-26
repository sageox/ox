package plan

// companion.go — rich HTML companion artifacts carried WITH a plan.
//
// A markdown plan has a hard richness ceiling: interactivity (tabbed views,
// field inspectors, animated timelines) cannot be expressed in prose. When a
// coworker hand-crafts a self-contained interactive HTML page alongside a
// plan, that page must travel WITH the plan — copied into the saved plan's
// ledger dir, listed in meta.json, linked prominently from the rendered page,
// and served by the live review loop — instead of becoming an orphan the
// render competes with.
//
// Companions are stored under a companions/ subdir of the plan dir (and of
// any -o/temp output dir), so the rendered page can link them with one
// uniform relative href ("companions/<name>") that works from file://, from
// a saved ledger render, and from the `ox plan review` server alike.
//
// TRUST BOUNDARY: a companion is the developer's own local file, copied
// byte-for-byte (never rewritten or sanitized) and rendered locally for that
// same developer — the same posture as the plan markdown itself (see
// newMarkdown). Artifact mode omits companion links: an artifact is a single
// self-contained page and a relative link would dangle.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// CompanionsDir is the subdirectory (of a plan dir or a render output dir)
// where companion HTML files live.
const CompanionsDir = "companions"

// Companion is the render-time view of one companion artifact: the display
// name and the href the rendered page links to.
type Companion struct {
	Name string
	Href string
}

// CompanionFile pairs a companion's sanitized basename with the source path
// it is copied from — the command layer's carrying shape between detection,
// ledger save, and output emission.
type CompanionFile struct {
	Name    string // sanitized basename, e.g. "deep-dive.html"
	SrcPath string // absolute path of the file to copy
	RelPath string // optional sanitized markdown href path to preserve beside renders
}

// mdLinkRe matches inline markdown links [text](target). Targets are then
// filtered to bare relative .html paths — a URL (scheme or leading slash) or
// an anchor is never a companion candidate.
var mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^()\s]+\.html?)\)`)

// DetectCompanionLinks scans plan markdown for relative links to local .html
// files and resolves them against baseDir (the plan file's directory). Only
// links whose target actually exists on disk are returned — a dangling link
// is prose, not a companion. Returns absolute paths, deduped, in document
// order. baseDir == "" (stdin plans have no directory) yields nil.
func DetectCompanionLinks(raw, baseDir string) []string {
	var out []string
	for _, f := range DetectCompanionFiles(raw, baseDir) {
		out = append(out, f.SrcPath)
	}
	return out
}

// DetectCompanionFiles is DetectCompanionLinks with the original markdown href
// path retained, so emitted renders can preserve both the companion card link
// and the author's inline relative link.
func DetectCompanionFiles(raw, baseDir string) []CompanionFile {
	if baseDir == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []CompanionFile
	for _, m := range mdLinkRe.FindAllStringSubmatch(raw, -1) {
		target := m[1]
		if strings.Contains(target, "://") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "#") {
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(target))
		if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
			continue
		}
		abs := filepath.Join(baseDir, rel)
		if info, err := os.Stat(abs); err != nil || info.IsDir() {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, CompanionFile{Name: CompanionName(abs), SrcPath: abs, RelPath: filepath.ToSlash(rel)})
	}
	return out
}

// CompanionName sanitizes a companion source path to the basename it is
// stored and linked under. Path separators can never survive (basename), and
// a name that could collide with the plan's own artifacts inside the
// companions/ subdir cannot arise — the subdir is the namespace.
func CompanionName(srcPath string) string {
	return filepath.Base(filepath.Clean(srcPath))
}

// CopyCompanions copies companion files into destDir/companions/, creating
// the subdir on first use. Returns the stored basenames in input order.
// Copies are byte-for-byte — a companion's HTML is never rewritten.
func CopyCompanions(files []CompanionFile, destDir string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	dir := filepath.Join(destDir, CompanionsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create companions dir: %w", err)
	}
	var names []string
	for _, f := range files {
		data, err := os.ReadFile(f.SrcPath)
		if err != nil {
			return names, fmt.Errorf("read companion %q: %w", f.SrcPath, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f.Name), data, 0o644); err != nil {
			return names, fmt.Errorf("write companion %q: %w", f.Name, err)
		}
		if f.RelPath != "" && f.RelPath != CompanionsDir+"/"+f.Name {
			rel := filepath.Clean(filepath.FromSlash(f.RelPath))
			if rel != "." && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				dst := filepath.Join(destDir, rel)
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return names, fmt.Errorf("create companion link dir %q: %w", f.RelPath, err)
				}
				if _, err := os.Stat(dst); err == nil {
					names = append(names, f.Name)
					continue
				} else if !errors.Is(err, os.ErrNotExist) {
					return names, fmt.Errorf("stat companion link %q: %w", f.RelPath, err)
				}
				if err := os.WriteFile(dst, data, 0o644); err != nil {
					return names, fmt.Errorf("write companion link %q: %w", f.RelPath, err)
				}
			}
		}
		names = append(names, f.Name)
	}
	return names, nil
}

// RecordCompanions merges companion basenames into a saved plan's meta.json
// (deduped, order-preserving) under the meta flock. No-op when the plan has
// no meta.json yet or names is empty.
func RecordCompanions(planDir string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	return MutatePlanMeta(context.Background(), planDir, func(m *Meta) (*Meta, error) {
		if m == nil {
			return nil, nil
		}
		seen := make(map[string]struct{}, len(m.Companions))
		for _, n := range m.Companions {
			seen[n] = struct{}{}
		}
		for _, n := range names {
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			m.Companions = append(m.Companions, n)
		}
		return m, nil
	})
}

// CompanionRefs projects stored companion basenames into the render-time
// Companion list. The href is the uniform relative "companions/<name>" that
// resolves next to plan.html in the ledger dir, next to an -o/temp output,
// and against the review server's /companions/ route.
func CompanionRefs(names []string) []Companion {
	var out []Companion
	for _, n := range names {
		if n == "" || n != filepath.Base(n) {
			continue // defensive: a stored name must be a bare basename
		}
		out = append(out, Companion{Name: n, Href: CompanionsDir + "/" + n})
	}
	return out
}
