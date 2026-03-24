package discussion

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// LoadSummary reads and parses summary.json from a discussion directory.
// Returns nil if the file doesn't exist (not all discussions have server-generated summaries yet).
func LoadSummary(dirPath string) (*DiscussionSummary, error) {
	data, err := os.ReadFile(filepath.Join(dirPath, "summary.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read summary.json: %w", err)
	}
	var s DiscussionSummary
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse summary.json: %w", err)
	}
	return &s, nil
}

// LoadKeyframes reads and parses keyframes.json from a discussion directory.
// Returns nil if the file doesn't exist.
func LoadKeyframes(dirPath string) (*KeyframesManifest, error) {
	data, err := os.ReadFile(filepath.Join(dirPath, "keyframes.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read keyframes.json: %w", err)
	}
	var m KeyframesManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse keyframes.json: %w", err)
	}
	return &m, nil
}

// LoadAnnotations reads and parses annotations.json from a discussion directory.
// Returns nil if the file doesn't exist.
func LoadAnnotations(dirPath string) (*AnnotationsFile, error) {
	data, err := os.ReadFile(filepath.Join(dirPath, "annotations.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read annotations.json: %w", err)
	}
	var a AnnotationsFile
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("parse annotations.json: %w", err)
	}
	return &a, nil
}

// VisualTypes returns the sorted unique set of content types from keyframes linked to the given chapter.
// Returns nil if no visual keyframes exist for this chapter.
func VisualTypes(keyframes *KeyframesManifest, chapterID string) []string {
	if keyframes == nil {
		return nil
	}
	seen := make(map[string]bool)
	for _, kf := range keyframes.Keyframes {
		if kf.ChapterID == chapterID && kf.ContentType != "" {
			seen[kf.ContentType] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	types := make([]string, 0, len(seen))
	for t := range seen {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// AllVisualTypes returns the sorted unique set of content types across all keyframes.
// Returns nil if the manifest is nil or contains no visual content.
func AllVisualTypes(keyframes *KeyframesManifest) []string {
	if keyframes == nil {
		return nil
	}
	seen := make(map[string]bool)
	for _, kf := range keyframes.Keyframes {
		if kf.ContentType != "" {
			seen[kf.ContentType] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	types := make([]string, 0, len(seen))
	for t := range seen {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}
