// Package attestpublication publishes producer-owned frozen BDD exports.
package attestpublication

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	packageVersion = "1"
	runFilename    = "run.json"
	manifestName   = "manifest.json"
)

//go:embed contract/schema/v1/run.schema.json
var runSchema []byte

//go:embed contract/schema/v1/manifest.schema.json
var manifestSchema []byte

type ManifestFile struct {
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
}

type Manifest struct {
	PackageVersion string         `json:"package_version"`
	RunDocument    string         `json:"run_document"`
	Files          []ManifestFile `json:"files"`
}

type Package struct {
	Run              RunIdentity
	Manifest         []byte
	ManifestSHA256   string
	ArchivePath      string
	ArchiveSHA256    string
	ArchiveSizeBytes int64
}

type RunIdentity struct {
	SourceRunID string `json:"source_run_id"`
	Corpus      struct {
		Key string `json:"key"`
	} `json:"corpus"`
}

// BuildPackage reads only the producer's frozen export. It never inspects the
// calling checkout, because publication after a feature edit must retain the
// corpus admitted when the run started.
func BuildPackage(exportDir, archivePath string) (Package, error) {
	output, err := openPublicationRoot(filepath.Dir(archivePath))
	if err != nil {
		return Package{}, err
	}
	defer output.Close()
	return buildPackage(exportDir, archivePath, output)
}

func buildPackage(exportDir, archivePath string, output *os.Root) (Package, error) {
	if archivePath == "" {
		return Package{}, fmt.Errorf("archive path is required")
	}
	root, err := os.OpenRoot(exportDir)
	if err != nil {
		return Package{}, err
	}
	defer root.Close()
	files, runRaw, identity, err := readExport(root)
	if err != nil {
		return Package{}, err
	}
	if err := validateJSON(runSchema, runRaw); err != nil {
		return Package{}, fmt.Errorf("validate frozen run.json: %w", err)
	}
	if err := validateRunSemantics(runRaw); err != nil {
		return Package{}, fmt.Errorf("validate frozen run.json semantics: %w", err)
	}
	manifest, err := marshalManifest(files)
	if err != nil {
		return Package{}, err
	}
	if err := validateJSON(manifestSchema, manifest); err != nil {
		return Package{}, fmt.Errorf("validate package manifest: %w", err)
	}
	if err := validateManifestSemantics(manifest); err != nil {
		return Package{}, fmt.Errorf("validate package manifest semantics: %w", err)
	}
	if err := writeArchive(archivePath, output, root, files, manifest); err != nil {
		return Package{}, err
	}
	file, err := output.Open(filepath.Base(archivePath))
	if err != nil {
		return Package{}, err
	}
	defer file.Close()
	archiveDigest, archiveSize, err := hashArchiveFile(file)
	if err != nil {
		return Package{}, err
	}
	return Package{Run: identity, Manifest: manifest, ManifestSHA256: digest(manifest), ArchivePath: archivePath, ArchiveSHA256: archiveDigest, ArchiveSizeBytes: archiveSize}, nil
}

func readExport(root *os.Root) ([]ManifestFile, []byte, RunIdentity, error) {
	var files []ManifestFile
	var runRaw []byte
	var identity RunIdentity
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("frozen export contains symlink %q", name)
		}
		rel := name
		if err := validRelativePath(rel); err != nil {
			return fmt.Errorf("frozen export path %q: %w", rel, err)
		}
		if rel == manifestName {
			// A producer export has no manifest. Refusing one avoids binding an old
			// self-reference to a new archive by accident.
			return fmt.Errorf("frozen export must not contain %s", manifestName)
		}
		raw, err := root.ReadFile(name)
		if err != nil {
			return err
		}
		if rel == runFilename {
			runRaw = raw
			if err := json.Unmarshal(raw, &identity); err != nil {
				return fmt.Errorf("decode run.json: %w", err)
			}
		}
		files = append(files, ManifestFile{Path: rel, SizeBytes: int64(len(raw)), SHA256: digest(raw), ContentType: contentType(rel)})
		return nil
	})
	if err != nil {
		return nil, nil, RunIdentity{}, err
	}
	if len(runRaw) == 0 || identity.SourceRunID == "" || identity.Corpus.Key == "" {
		return nil, nil, RunIdentity{}, fmt.Errorf("frozen export requires a valid %s with source_run_id and corpus.key", runFilename)
	}
	slices.SortFunc(files, func(a, b ManifestFile) int { return strings.Compare(a.Path, b.Path) })
	return files, runRaw, identity, nil
}

func marshalManifest(files []ManifestFile) ([]byte, error) {
	if len(files) == 0 || files[0].Path == manifestName {
		return nil, fmt.Errorf("manifest requires frozen export files")
	}
	seenRun := false
	for index, file := range files {
		if err := validRelativePath(file.Path); err != nil {
			return nil, err
		}
		if index > 0 && files[index-1].Path >= file.Path {
			return nil, fmt.Errorf("manifest file paths must be sorted and unique")
		}
		seenRun = seenRun || file.Path == runFilename
	}
	if !seenRun {
		return nil, fmt.Errorf("manifest requires %s", runFilename)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(Manifest{PackageVersion: packageVersion, RunDocument: runFilename, Files: files}); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func writeArchive(target string, output, root *os.Root, files []ManifestFile, manifest []byte) error {
	f, temporary, err := createPublicationTemp(output, ".attest-archive-")
	if err != nil {
		return err
	}
	defer func() { _ = output.Remove(temporary) }()
	archive := zip.NewWriter(f)
	for _, file := range files {
		if err := addArchiveFile(archive, root, file.Path); err != nil {
			_ = archive.Close()
			_ = f.Close()
			return err
		}
	}
	if err := addArchiveBytes(archive, manifestName, manifest); err != nil {
		_ = archive.Close()
		_ = f.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := output.Rename(temporary, filepath.Base(target)); err != nil {
		return err
	}
	syncArchiveParent(output)
	return nil
}

func addArchiveFile(archive *zip.Writer, root *os.Root, name string) error {
	f, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	defer f.Close()
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
	w, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func addArchiveBytes(archive *zip.Writer, name string, raw []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
	w, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
}

func validateJSON(schemaRaw, raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaRaw))
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", doc); err != nil {
		return err
	}
	schema, err := compiler.Compile("schema.json")
	if err != nil {
		return err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}

type semanticScenario struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Examples []struct {
		Rows []json.RawMessage `json:"rows"`
	} `json:"examples"`
}

type semanticRun struct {
	Corpus struct {
		Key          string `json:"key"`
		Capabilities []struct {
			Features []struct {
				Path      string             `json:"path"`
				Title     string             `json:"title"`
				Scenarios []semanticScenario `json:"scenarios"`
				Rules     []struct {
					Title     string             `json:"title"`
					Scenarios []semanticScenario `json:"scenarios"`
				} `json:"rules"`
			} `json:"features"`
		} `json:"capabilities"`
	} `json:"corpus"`
	Outcomes []struct {
		Instance struct {
			ScenarioKey          string `json:"scenario_key"`
			ExamplesBlockOrdinal *int   `json:"examples_block_ordinal"`
			RowOrdinal           *int   `json:"row_ordinal"`
		} `json:"instance"`
	} `json:"outcomes"`
}

func validateRunSemantics(raw []byte) error {
	var run semanticRun
	if err := json.Unmarshal(raw, &run); err != nil {
		return err
	}
	expected := make(map[string]struct{})
	addScenarios := func(featurePath, featureTitle string, ruleTitle *string, scenarios []semanticScenario) error {
		for _, scenario := range scenarios {
			want, err := semanticScenarioKey(run.Corpus.Key, featurePath, featureTitle, ruleTitle, scenario.Title)
			if err != nil {
				return err
			}
			if scenario.Key != want {
				return fmt.Errorf("scenario %q has key %s, want %s", scenario.Title, scenario.Key, want)
			}
			switch scenario.Kind {
			case "scenario":
				expected[scenario.Key] = struct{}{}
			case "outline":
				for blockOrdinal, block := range scenario.Examples {
					for rowOrdinal := range block.Rows {
						expected[semanticInstanceKey(scenario.Key, &blockOrdinal, &rowOrdinal)] = struct{}{}
					}
				}
			}
		}
		return nil
	}
	for _, capability := range run.Corpus.Capabilities {
		for _, feature := range capability.Features {
			if err := addScenarios(feature.Path, feature.Title, nil, feature.Scenarios); err != nil {
				return err
			}
			for _, rule := range feature.Rules {
				// An authored bare Rule has title "". Its address must not collapse
				// into nil, which means the scenario was outside every Rule.
				if err := addScenarios(feature.Path, feature.Title, &rule.Title, rule.Scenarios); err != nil {
					return err
				}
			}
		}
	}
	seen := make(map[string]struct{}, len(run.Outcomes))
	for _, outcome := range run.Outcomes {
		key := semanticInstanceKey(outcome.Instance.ScenarioKey, outcome.Instance.ExamplesBlockOrdinal, outcome.Instance.RowOrdinal)
		if _, ok := expected[key]; !ok {
			return fmt.Errorf("outcome references unknown corpus instance %s", key)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate outcome for corpus instance %s", key)
		}
		seen[key] = struct{}{}
	}
	for key := range expected {
		if _, ok := seen[key]; !ok {
			return fmt.Errorf("missing outcome for corpus instance %s", key)
		}
	}
	return nil
}

func validateManifestSemantics(raw []byte) error {
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	seenRun := false
	for index, file := range manifest.Files {
		if index > 0 && manifest.Files[index-1].Path >= file.Path {
			return fmt.Errorf("manifest file paths must be sorted and unique")
		}
		if file.Path == runFilename {
			seenRun = true
		}
	}
	if !seenRun {
		return fmt.Errorf("manifest requires %s", runFilename)
	}
	return nil
}

func semanticScenarioKey(corpusKey, featurePath, featureTitle string, ruleTitle *string, scenarioTitle string) (string, error) {
	var tuple bytes.Buffer
	encoder := json.NewEncoder(&tuple)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode([]any{corpusKey, featurePath, featureTitle, ruleTitle, scenarioTitle}); err != nil {
		return "", err
	}
	return digest(bytes.TrimSuffix(tuple.Bytes(), []byte("\n"))), nil
}

func semanticInstanceKey(scenarioKey string, blockOrdinal, rowOrdinal *int) string {
	if blockOrdinal == nil || rowOrdinal == nil {
		return scenarioKey
	}
	return fmt.Sprintf("%s:%d:%d", scenarioKey, *blockOrdinal, *rowOrdinal)
}

func validRelativePath(path string) error {
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\%?#:\x00") || filepath.ToSlash(filepath.Clean(path)) != path {
		return fmt.Errorf("unsafe relative path %q", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("unsafe relative path %q", path)
		}
	}
	return nil
}

// Pin package metadata independently of host MIME databases so retries on a
// different operating system produce the same manifest and archive identity.
var packageContentTypes = map[string]string{
	".css": "text/css", ".csv": "text/csv", ".html": "text/html", ".htm": "text/html",
	".js": "text/javascript", ".mjs": "text/javascript", ".json": "application/json",
	".md": "text/markdown", ".txt": "text/plain", ".xml": "application/xml",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".svg": "image/svg+xml", ".webp": "image/webp", ".ico": "image/vnd.microsoft.icon",
	".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".wav": "audio/wav",
	".pdf": "application/pdf", ".zip": "application/zip", ".woff": "font/woff", ".woff2": "font/woff2",
}

func contentType(path string) string {
	if value, ok := packageContentTypes[strings.ToLower(filepath.Ext(path))]; ok {
		return value
	}
	return "application/octet-stream"
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func hashArchiveFile(f *os.File) (string, int64, error) {
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	size, err := io.Copy(h, io.NewSectionReader(f, 0, info.Size()))
	return hex.EncodeToString(h.Sum(nil)), size, err
}
