package format

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
)

// LayersDirName is the layer directory under a discussion-folder root.
const LayersDirName = "layers"

// layerFileName is the envelope file inside a folder-form layer directory.
const layerFileName = "layer.json"

// layerIDPattern is the strict shape of a layer id embedded in a file or
// directory name: clyr_ + UUID (v7 in practice). Ids that do not match are
// not treated as layer artifacts at all.
var layerIDPattern = regexp.MustCompile(`^clyr_[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// LayerLayout distinguishes the two on-disk layer layouts, which coexist in
// one tree (D4).
type LayerLayout string

const (
	// LayoutFolder is layers/<kind>.<clyr_id>/layer.json.
	LayoutFolder LayerLayout = "folder"
	// LayoutFlat is the legacy layers/<kind>.<clyr_id>.json.
	LayoutFlat LayerLayout = "flat"
)

// LayerDerivedFrom records the producer lineage of a derived layer.
type LayerDerivedFrom struct {
	Producer string   `json:"producer"`
	Version  string   `json:"version"`
	Inputs   []string `json:"inputs,omitempty"`
}

// LayerContentRef is one content reference inside a layer envelope. Refs are
// consulted for metadata only (D7): transcript layer refs are unreliable as
// paths across eras, so content reads go to the fixed well-known paths.
type LayerContentRef struct {
	Ref  string `json:"ref"`
	Path string `json:"path"`
	Mime string `json:"mime"`
}

// LayerContent is the content block of a layer envelope.
type LayerContent struct {
	Kind string            `json:"kind"`
	Refs []LayerContentRef `json:"refs,omitempty"`
}

// LayerClock is the clock block of a layer envelope.
type LayerClock struct {
	T0 string `json:"t0"`
}

// LayerEnvelope is one layer manifest (layer.json / flat <kind>.<clyr>.json).
// Consulted only for revision / status / lineage metadata (D7).
type LayerEnvelope struct {
	SchemaVersion  *int              `json:"$schema_version"`
	LayerID        string            `json:"layer_id"`
	ConversationID string            `json:"conversation_id"`
	Kind           string            `json:"kind"`
	Modality       string            `json:"modality"`
	Mime           string            `json:"mime"`
	Language       *string           `json:"language"`
	Label          string            `json:"label"`
	Origin         string            `json:"origin"`
	Spec           string            `json:"spec"`
	Revision       int               `json:"revision"`
	Status         string            `json:"status"`
	DerivedFrom    *LayerDerivedFrom `json:"derived_from,omitempty"`
	Supersedes     *string           `json:"supersedes"`
	Lineage        []string          `json:"lineage,omitempty"`
	Clock          *LayerClock       `json:"clock,omitempty"`
	Content        *LayerContent     `json:"content,omitempty"`
}

// DiscoveredLayer is one valid layer found by DiscoverLayers.
type DiscoveredLayer struct {
	Envelope LayerEnvelope
	// Path is the envelope file path relative to the discussion root, using
	// forward slashes.
	Path string
	// Layout records which on-disk layout the envelope came from.
	Layout LayerLayout
}

// LayerDiscovery is the result of scanning a discussion folder's layers/
// tree: the valid layers in deterministic order plus every artifact that
// looked like a layer but could not be used (surfaced, never dropped).
type LayerDiscovery struct {
	// Layers is sorted by layer id, then path.
	Layers []DiscoveredLayer
	// Invalid surfaces parse failures, path↔body id mismatches, and losing
	// duplicates.
	Invalid []InvalidRecord
}

// parsedLayerName is the (kind, id) parsed out of a flat file name or a
// folder-form directory name.
type parsedLayerName struct {
	kind string
	id   string
}

// parseLayerName parses base (a directory name, or a file name already
// stripped of its .json extension) as <kind>.<clyr_id>. Names carrying path
// separators, parent references, or a malformed id are rejected — this is the
// confinement gate the discovery walk relies on, and the fuzz target for the
// zero-root-escape guarantee.
func parseLayerName(base string) (parsedLayerName, bool) {
	if base == "" || strings.ContainsAny(base, "/\\") || strings.Contains(base, "..") {
		return parsedLayerName{}, false
	}
	i := strings.Index(base, ".clyr_")
	if i <= 0 {
		return parsedLayerName{}, false
	}
	kind, id := base[:i], base[i+1:]
	if !layerIDPattern.MatchString(id) {
		return parsedLayerName{}, false
	}
	return parsedLayerName{kind: kind, id: id}, true
}

// DiscoverLayers scans discussionRoot/layers recursively per D4: both
// layouts, folder-wins dedup for same-id duplicates, path↔body id mismatch
// surfaced as invalid, parse failures surfaced as invalid, deterministic
// order. A folder with no layers/ directory at all returns an empty result
// (D5). Sidecar .jsonl files and unrecognized names are ignored.
//
// Every read happens through an os.Root over discussionRoot: a symlinked
// layers/ directory, envelope file, or layer.json pointing outside the root
// is never followed — it surfaces as an error or an invalid record.
func DiscoverLayers(discussionRoot string) (*LayerDiscovery, error) {
	root, err := openOptionalRoot(discussionRoot)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return &LayerDiscovery{}, nil
	}
	defer root.Close()
	return DiscoverLayersIn(root)
}

// DiscoverLayersIn is DiscoverLayers over an already-open discussion-folder
// root, for callers that hold the folder open (derived from a validated
// discussions root) and must not re-open it by absolute path.
func DiscoverLayersIn(root *os.Root) (*LayerDiscovery, error) {
	result := &LayerDiscovery{}

	var candidates []DiscoveredLayer
	if err := walkLayerCandidates(root, LayersDirName, result, &candidates); err != nil {
		return nil, err
	}

	// Deterministic pre-dedup order: folder layout first (folder wins), then
	// lexicographic path, so the winner of any duplicate set is stable.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Envelope.LayerID != b.Envelope.LayerID {
			return a.Envelope.LayerID < b.Envelope.LayerID
		}
		if a.Layout != b.Layout {
			return a.Layout == LayoutFolder
		}
		return a.Path < b.Path
	})

	seen := make(map[string]string) // layer id -> winning path
	for _, c := range candidates {
		if winner, dup := seen[c.Envelope.LayerID]; dup {
			result.Invalid = append(result.Invalid, InvalidRecord{
				Path:   c.Path,
				Reason: fmt.Sprintf("duplicate layer id %s: superseded by %s", c.Envelope.LayerID, winner),
			})
			continue
		}
		seen[c.Envelope.LayerID] = c.Path
		result.Layers = append(result.Layers, c)
	}

	sort.Slice(result.Invalid, func(i, j int) bool {
		if result.Invalid[i].Path != result.Invalid[j].Path {
			return result.Invalid[i].Path < result.Invalid[j].Path
		}
		return result.Invalid[i].Reason < result.Invalid[j].Reason
	})
	return result, nil
}

// walkLayerCandidates recursively lists relDir inside root, classifying
// folder-form and flat-form layer artifacts. relDir is expressed relative to
// the discussion root with forward slashes and is read through the root, so
// no component of it can follow a symlink out of the discussion folder.
// Entries whose names fail the parse-time confinement checks are ignored (a
// hostile name never becomes a path join).
func walkLayerCandidates(root *os.Root, relDir string, result *LayerDiscovery, out *[]DiscoveredLayer) error {
	entries, err := fs.ReadDir(root.FS(), relDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("list %s: %w", relDir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
			continue // defense in depth: never join a traversal-capable name
		}
		childRel := relDir + "/" + name

		if entry.IsDir() {
			if parsed, ok := parseLayerName(name); ok {
				loadLayerEnvelope(root, childRel+"/"+layerFileName, parsed, LayoutFolder, result, out)
			}
			// Recurse regardless: a non-recursive scan silently misses
			// folder-form layers, and nested trees have been observed.
			if err := walkLayerCandidates(root, childRel, result, out); err != nil {
				return err
			}
			continue
		}
		base, isJSON := strings.CutSuffix(name, ".json")
		if !isJSON {
			continue // .jsonl sidecars and other files are not envelopes
		}
		parsed, ok := parseLayerName(base)
		if !ok {
			continue
		}
		loadLayerEnvelope(root, childRel, parsed, LayoutFlat, result, out)
	}
	return nil
}

// loadLayerEnvelope reads and validates one candidate envelope file through
// the discussion root, appending to out on success and to result.Invalid on
// any defect. A missing layer.json inside a layer-shaped directory is
// surfaced as invalid, and so is a symlinked envelope that escapes the root.
func loadLayerEnvelope(root *os.Root, relPath string, parsed parsedLayerName, layout LayerLayout, result *LayerDiscovery, out *[]DiscoveredLayer) {
	data, err := readOptionalFileIn(root, relPath)
	if err != nil {
		result.Invalid = append(result.Invalid, InvalidRecord{Path: relPath, Reason: err.Error()})
		return
	}
	if data == nil {
		result.Invalid = append(result.Invalid, InvalidRecord{Path: relPath, Reason: "layer directory without " + layerFileName})
		return
	}
	var env LayerEnvelope
	if err := decodeJSON(relPath, data, &env); err != nil {
		result.Invalid = append(result.Invalid, InvalidRecord{Path: relPath, Reason: err.Error()})
		return
	}
	if env.LayerID != parsed.id {
		result.Invalid = append(result.Invalid, InvalidRecord{
			Path:   relPath,
			Reason: fmt.Sprintf("layer id mismatch: path says %s, body says %q", parsed.id, env.LayerID),
		})
		return
	}
	*out = append(*out, DiscoveredLayer{Envelope: env, Path: relPath, Layout: layout})
}
