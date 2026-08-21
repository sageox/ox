package format

import (
	"fmt"
	"os"
	"path/filepath"
)

// Manifest file names. Production writes layers.json; the spec, published
// schemas, and fixture corpus use conversation.json — an explicitly open
// decision server-side, not legacy. Both are accepted (D6); layers.json is
// preferred when both are present, and the anomaly is surfaced as a warning.
const (
	ManifestNameLayers       = "layers.json"
	ManifestNameConversation = "conversation.json"
)

// WarnBothManifestNames is the warning surfaced when a folder carries both
// layers.json and conversation.json (D6: prefer layers.json, note the
// anomaly rather than erroring).
const WarnBothManifestNames = "both layers.json and conversation.json present; using layers.json"

// ClockPause is one recording pause window inside the manifest clock.
type ClockPause struct {
	PausedAt  string `json:"paused_at"`
	ResumedAt string `json:"resumed_at"`
}

// ManifestClock is the recording clock block of the conversation manifest.
// Pauses tolerates both [] and null (older writers emitted either).
type ManifestClock struct {
	T0         string       `json:"t0"`
	ClockClass string       `json:"clock_class"`
	Pauses     []ClockPause `json:"pauses"`
}

// Manifest is the conversation manifest (layers.json / conversation.json) at
// a discussion-folder root. The manifest is optional (D5): folders predating
// the layer producers have none, and reads fall back to the well-known root
// files.
//
// SchemaVersion semantics: absent (nil) is legacy-legal; an explicit 0 is
// invalid and rejected at load time.
type Manifest struct {
	SchemaVersion *int          `json:"$schema_version"`
	ID            string        `json:"id"`
	Type          string        `json:"type"`
	ContextType   string        `json:"context_type"`
	ContextID     string        `json:"context_id"`
	Title         string        `json:"title"`
	StartedAt     string        `json:"started_at"`
	EndedAt       *string       `json:"ended_at"`
	FinalizedAt   *string       `json:"finalized_at"`
	SealedAt      *string       `json:"sealed_at"`
	BoundaryRef   string        `json:"boundary_ref"`
	Clock         ManifestClock `json:"clock"`
}

// LoadManifest reads the conversation manifest from a discussion folder,
// accepting both manifest names per D6. Returns (nil, nil, nil) when the
// folder has no manifest at all (D5). When both names are present, the
// layers.json content wins and warnings carries WarnBothManifestNames.
//
// An explicit "$schema_version": 0 is invalid and returns an error; an
// absent $schema_version is legacy-legal. Empty titles are valid (D13).
func LoadManifest(discussionRoot string) (m *Manifest, warnings []string, err error) {
	root, err := openOptionalRoot(discussionRoot)
	if err != nil || root == nil {
		return nil, nil, err
	}
	defer root.Close()
	return LoadManifestIn(root)
}

// LoadManifestIn is LoadManifest over an already-open discussion-folder root,
// for callers that hold the folder open (derived from a validated discussions
// root) and must not re-open it by absolute path.
func LoadManifestIn(root *os.Root) (m *Manifest, warnings []string, err error) {
	layersPath := filepath.Join(root.Name(), ManifestNameLayers)
	convPath := filepath.Join(root.Name(), ManifestNameConversation)

	layersData, err := readOptionalFileIn(root, ManifestNameLayers)
	if err != nil {
		return nil, nil, err
	}
	convData, err := readOptionalFileIn(root, ManifestNameConversation)
	if err != nil {
		return nil, nil, err
	}

	path, data := layersPath, layersData
	if layersData == nil {
		path, data = convPath, convData
	} else if convData != nil {
		warnings = append(warnings, WarnBothManifestNames)
	}
	if data == nil {
		return nil, nil, nil
	}

	var manifest Manifest
	if err := decodeJSON(path, data, &manifest); err != nil {
		return nil, warnings, err
	}
	if manifest.SchemaVersion != nil && *manifest.SchemaVersion == 0 {
		return nil, warnings, fmt.Errorf("parse %s: explicit $schema_version 0 is invalid", path)
	}
	return &manifest, warnings, nil
}
