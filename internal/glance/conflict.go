package glance

import (
	"fmt"
	"sort"
	"strings"
)

// DetectConflicts builds a file-level overlap report from sessions.
// A conflict is any file touched by 2+ distinct authors.
func DetectConflicts(sessions []SessionRecord) *ConflictReport {
	// Build inverted index: filepath → map[author][]sessionName
	index := make(map[string]map[string][]string)
	allFiles := make(map[string]bool)

	for _, s := range sessions {
		for _, f := range s.Files {
			allFiles[f] = true
			if index[f] == nil {
				index[f] = make(map[string][]string)
			}
			index[f][s.User] = append(index[f][s.User], s.Name)
		}
	}

	// Filter to files with 2+ authors
	var overlaps []FileOverlap
	authorPairs := make(map[string]int)

	for file, authors := range index {
		if len(authors) < 2 {
			continue
		}
		overlaps = append(overlaps, FileOverlap{
			FilePath: file,
			Authors:  authors,
		})

		// Count author-pair overlaps
		names := make([]string, 0, len(authors))
		for name := range authors {
			names = append(names, name)
		}
		sort.Strings(names)
		for i := 0; i < len(names); i++ {
			for j := i + 1; j < len(names); j++ {
				key := fmt.Sprintf("%s|%s", names[i], names[j])
				authorPairs[key]++
			}
		}
	}

	// Sort overlaps: most authors first, then alphabetically
	sort.Slice(overlaps, func(i, j int) bool {
		ai, aj := len(overlaps[i].Authors), len(overlaps[j].Authors)
		if ai != aj {
			return ai > aj
		}
		return overlaps[i].FilePath < overlaps[j].FilePath
	})

	return &ConflictReport{
		Overlaps:     overlaps,
		ByAuthorPair: authorPairs,
		TotalFiles:   len(allFiles),
	}
}

// OverlapPairs converts the internal author-pair map to a sorted slice.
func (r *ConflictReport) OverlapPairs() []OverlapPair {
	pairs := make([]OverlapPair, 0, len(r.ByAuthorPair))
	for key, count := range r.ByAuthorPair {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 {
			continue
		}
		pairs = append(pairs, OverlapPair{
			Pair:        [2]string{parts[0], parts[1]},
			SharedFiles: count,
		})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].SharedFiles > pairs[j].SharedFiles
	})
	return pairs
}
