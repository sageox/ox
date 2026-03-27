package glance

import (
	"fmt"
	"sort"
	"strings"
)

// Pattern represents a detected collaboration pattern.
type Pattern struct {
	Type    string   `json:"type"`    // cluster_bridge, hot_file, solo_silo
	Authors []string `json:"authors"` // involved developers
	Files   []string `json:"files"`   // involved files
	Detail  string   `json:"detail"`  // human-readable explanation
	Risk    string   `json:"risk"`    // low, medium, high
}

// DetectPatterns identifies collaboration patterns from session data.
// Uses only fields available in real sessions: file paths, usernames, timestamps.
func DetectPatterns(sessions []SessionRecord) []Pattern {
	var patterns []Pattern
	patterns = append(patterns, detectClusterBridge(sessions)...)
	patterns = append(patterns, detectHotFiles(sessions)...)
	patterns = append(patterns, detectSoloSilos(sessions)...)
	return patterns
}

// detectClusterBridge finds authors who are the sole connection between two
// otherwise disjoint package groups.
func detectClusterBridge(sessions []SessionRecord) []Pattern {
	authorFiles := make(map[string]map[string]bool)
	for _, s := range sessions {
		if authorFiles[s.User] == nil {
			authorFiles[s.User] = make(map[string]bool)
		}
		for _, f := range s.Files {
			authorFiles[s.User][f] = true
		}
	}

	var patterns []Pattern
	for author, files := range authorFiles {
		packages := make(map[string][]string)
		for f := range files {
			pkg := topLevelPackage(f)
			packages[pkg] = append(packages[pkg], f)
		}
		if len(packages) < 3 {
			continue
		}

		pkgNames := make([]string, 0, len(packages))
		for p := range packages {
			pkgNames = append(pkgNames, p)
		}
		sort.Strings(pkgNames)

		for i := 0; i < len(pkgNames); i++ {
			for j := i + 1; j < len(pkgNames); j++ {
				pkgA, pkgB := pkgNames[i], pkgNames[j]
				otherBridges := 0
				for otherAuthor, otherFiles := range authorFiles {
					if otherAuthor == author {
						continue
					}
					touchesA, touchesB := false, false
					for f := range otherFiles {
						if topLevelPackage(f) == pkgA {
							touchesA = true
						}
						if topLevelPackage(f) == pkgB {
							touchesB = true
						}
					}
					if touchesA && touchesB {
						otherBridges++
					}
				}
				if otherBridges == 0 {
					files := append(packages[pkgA], packages[pkgB]...)
					sort.Strings(files)
					patterns = append(patterns, Pattern{
						Type:    "cluster_bridge",
						Authors: []string{author},
						Files:   files,
						Detail: fmt.Sprintf("%s is the only developer bridging %s and %s — coordination bottleneck risk",
							author, pkgA, pkgB),
						Risk: "medium",
					})
				}
			}
		}
	}
	return patterns
}

// detectHotFiles finds files touched by 3+ authors — high-contention areas.
func detectHotFiles(sessions []SessionRecord) []Pattern {
	fileAuthors := make(map[string]map[string]bool)
	for _, s := range sessions {
		for _, f := range s.Files {
			if fileAuthors[f] == nil {
				fileAuthors[f] = make(map[string]bool)
			}
			fileAuthors[f][s.User] = true
		}
	}

	var patterns []Pattern
	for file, authors := range fileAuthors {
		if len(authors) < 3 {
			continue
		}
		names := make([]string, 0, len(authors))
		for a := range authors {
			names = append(names, a)
		}
		sort.Strings(names)

		patterns = append(patterns, Pattern{
			Type:    "hot_file",
			Authors: names,
			Files:   []string{file},
			Detail: fmt.Sprintf("%s has %d authors editing it (%s) — high contention risk",
				file, len(names), strings.Join(names, ", ")),
			Risk: "high",
		})
	}

	sort.Slice(patterns, func(i, j int) bool {
		return len(patterns[i].Authors) > len(patterns[j].Authors)
	})
	return patterns
}

// detectSoloSilos finds authors who work entirely in isolation —
// no file overlap with any other author in the window.
func detectSoloSilos(sessions []SessionRecord) []Pattern {
	authorFiles := make(map[string]map[string]bool)
	for _, s := range sessions {
		if authorFiles[s.User] == nil {
			authorFiles[s.User] = make(map[string]bool)
		}
		for _, f := range s.Files {
			authorFiles[s.User][f] = true
		}
	}

	fileAuthors := make(map[string]map[string]bool)
	for author, files := range authorFiles {
		for f := range files {
			if fileAuthors[f] == nil {
				fileAuthors[f] = make(map[string]bool)
			}
			fileAuthors[f][author] = true
		}
	}

	var patterns []Pattern
	for author, files := range authorFiles {
		isolated := true
		for f := range files {
			if len(fileAuthors[f]) > 1 {
				isolated = false
				break
			}
		}
		if isolated && len(files) > 0 {
			fileList := make([]string, 0, len(files))
			for f := range files {
				fileList = append(fileList, f)
			}
			sort.Strings(fileList)

			patterns = append(patterns, Pattern{
				Type:    "solo_silo",
				Authors: []string{author},
				Files:   fileList,
				Detail:  fmt.Sprintf("%s is working in isolation — no file overlap with any other author", author),
				Risk:    "low",
			})
		}
	}
	return patterns
}

// topLevelPackage extracts the first two path segments (e.g., "internal/auth" from "internal/auth/handler.go").
func topLevelPackage(path string) string {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}
