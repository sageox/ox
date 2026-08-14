package attest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// LayoutVersion is in the KEY PATH, not a field.
//
// A reader told "I speak v1" must never have to open a v2 object to discover it
// cannot read it. Everything published lives under `attest/v1/`.
const LayoutVersion = "v1"

// ManifestName is the per-publish index. Written LAST and atomically — see
// WriteBundle for why that ordering is the whole point of the file.
const ManifestName = "manifest.json"

// Bundle is one publish: what the corpus can demonstrate, plus every record
// backing it, laid out so a third party can read it without our code.
type Bundle struct {
	// PublishedAt is UTC. The date partition below is derived from it, and the
	// clock is stated because a partition boundary is immutable once written.
	PublishedAt string  `json:"publishedAt"`
	RepoKey     string  `json:"repoKey"`
	Report      *Report `json:"report"`
}

// Manifest is a publish directory's self-describing index: every file, its
// size, and its digest.
//
// Two jobs, and neither is the permissions optimization the first draft of this
// design claimed. First, it is the only possible COMPLETION MARKER — a publish
// that died halfway leaves a directory indistinguishable from a finished one by
// listing alone, and "is this whole?" is unanswerable without a file that is
// written last. Second, the per-file digest is the only INTEGRITY check a
// reader gets once these bytes leave our machine.
type Manifest struct {
	LayoutVersion string         `json:"layoutVersion"`
	RepoKey       string         `json:"repoKey"`
	PublishedAt   string         `json:"publishedAt"`
	Files         []ManifestFile `json:"files"`
	// Totals are duplicated here so a reader can render a summary from one
	// object without fetching the report.
	Capabilities int `json:"capabilities"`
	Attestations int `json:"attestations"`
}

// ManifestFile is one published object.
type ManifestFile struct {
	// Path is relative to the BUNDLE ROOT (`attest/v1/`), never to the
	// manifest's own directory.
	//
	// This matters on object storage specifically: keys there are LITERAL, with
	// no path traversal, so a `../` segment is not resolved to a parent prefix —
	// it becomes part of the key and the fetch simply 404s. Rooting every path
	// at the bundle root keeps each one a valid key under any prefix the bundle
	// is hosted at, and keeps it readable.
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind"`
}

// PublishResult reports what landed where.
type PublishResult struct {
	Root         string `json:"root"`
	ManifestPath string `json:"manifest_path"`
	IndexPath    string `json:"index_path"`
	Files        int    `json:"files"`
	Bytes        int64  `json:"bytes"`
}

// WriteBundle lays out a publish under dest/attest/v1/.
//
// Layout, with the two corrections that came out of design review:
//
//		attest/v1/status/<repo-key>/<YYYY>/<MM>/<DD>/<stamp>/report.json
//		attest/v1/status/<repo-key>/<YYYY>/<MM>/<DD>/<stamp>/manifest.json
//		attest/v1/attestations/<repo-key>/<capability-slug>.v1.json
//		attest/v1/index/<repo-key>/<stamp>.json
//
//	 1. The index is ONE IMMUTABLE OBJECT PER PUBLISH, not an appended JSONL file.
//	    Standard object storage has no append operation, so "append a line" is a
//	    read-modify-write of the whole object — a lost-update race between two
//	    concurrent publishers, and flatly incompatible with write-once retention.
//	    An index is a derived compaction over these objects, computed by whoever
//	    wants one.
//	 2. Date partitioning is by UTC, stated rather than implied, because the
//	    boundary is immutable once written and a local-time publisher would
//	    silently scatter a day across two prefixes.
func WriteBundle(dest, repoKey string, report *Report, records *Records, runs []RunResult, now time.Time) (*PublishResult, error) {
	utc := now.UTC()
	// A second-resolution key could overwrite an immutable publication when an
	// operator corrects and republishes immediately. Nanoseconds keep distinct
	// local publishes distinct without making the portable layout AWS-specific.
	stamp := utc.Format("20060102T150405.000000000Z")
	base := filepath.Join(dest, "attest", LayoutVersion)
	runDir := filepath.Join(base, "status", repoKey,
		utc.Format("2006"), utc.Format("01"), utc.Format("02"), stamp)

	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("create publish dir: %w", err)
	}

	manifest := Manifest{
		LayoutVersion: LayoutVersion,
		RepoKey:       repoKey,
		PublishedAt:   utc.Format(time.RFC3339),
		Capabilities:  report.Capabilities,
		Attestations:  records.Count,
	}
	result := &PublishResult{Root: base}

	bundle := Bundle{PublishedAt: manifest.PublishedAt, RepoKey: repoKey, Report: report}
	mf, err := writeJSON(filepath.Join(runDir, "report.json"), bundle, base, "report")
	if err != nil {
		return nil, err
	}
	manifest.Files = append(manifest.Files, mf)

	// Attestations are published under their own prefix rather than inside the
	// dated directory: a record is current-state, not a point-in-time snapshot,
	// and burying it in a timestamp would make "what is proven now?" require a
	// listing to answer.
	attDir := filepath.Join(base, "attestations", repoKey)
	if records.Count > 0 {
		if err := os.MkdirAll(attDir, 0o755); err != nil {
			return nil, fmt.Errorf("create attestations dir: %w", err)
		}
	}
	ids := make([]string, 0, len(records.byCapability))
	for id := range records.byCapability {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic manifest ordering
	for _, id := range ids {
		name := fmt.Sprintf("%s.v%d.json", slugify(id), AttestationVersion)
		f, werr := writeJSON(filepath.Join(attDir, name), records.byCapability[id], base, "attestation")
		if werr != nil {
			return nil, werr
		}
		manifest.Files = append(manifest.Files, f)
	}

	// Only publish the normalized summaries referenced by an attestation. Raw
	// runner reports can contain screenshots and diagnostics, neither of which
	// belongs in a broadly readable team artifact by default.
	if len(runs) > 0 {
		runsDir := filepath.Join(base, "runs", repoKey)
		if err := os.MkdirAll(runsDir, 0o755); err != nil {
			return nil, fmt.Errorf("create runs dir: %w", err)
		}
		for _, run := range runs {
			f, werr := writeJSON(filepath.Join(runsDir, slugify(run.RunID)+".json"), run, base, "run-summary")
			if werr != nil {
				return nil, werr
			}
			manifest.Files = append(manifest.Files, f)
		}
	}

	// The index entry: one small object naming this publish, so a reader can
	// find the newest without listing a deep date tree.
	idxDir := filepath.Join(base, "index", repoKey)
	if err := os.MkdirAll(idxDir, 0o755); err != nil {
		return nil, fmt.Errorf("create index dir: %w", err)
	}
	idxEntry := map[string]any{
		"publishedAt":  manifest.PublishedAt,
		"repoKey":      repoKey,
		"capabilities": report.Capabilities,
		"attestations": records.Count,
		"counts":       report.Counts,
		"statusPath": filepath.ToSlash(filepath.Join("status", repoKey,
			utc.Format("2006"), utc.Format("01"), utc.Format("02"), stamp)),
	}
	for _, f := range manifest.Files {
		result.Bytes += f.Bytes
	}
	result.Files = len(manifest.Files)

	// LAST, and only now: the manifest is the completion marker. A publish that
	// dies before this point leaves a directory with no manifest, which is
	// exactly how a reader tells "torn" from "finished".
	manifestPath := filepath.Join(runDir, ManifestName)
	if err := writeAtomic(manifestPath, mustJSONBytes(manifest)); err != nil {
		return nil, err
	}
	result.ManifestPath = manifestPath
	// The index is written after the completion marker. Object-store readers
	// discover a publish from its index, so this ordering makes a torn upload
	// invisible instead of advertising a report whose manifest is not ready.
	indexPath := filepath.Join(idxDir, stamp+".json")
	if _, err := writeJSON(indexPath, idxEntry, base, "index"); err != nil {
		return nil, err
	}
	result.IndexPath = indexPath
	return result, nil
}

// writeJSON writes an object and returns its manifest entry.
func writeJSON(path string, v any, relTo, kind string) (ManifestFile, error) {
	raw := mustJSONBytes(v)
	if err := writeAtomic(path, raw); err != nil {
		return ManifestFile{}, err
	}
	rel, err := filepath.Rel(relTo, path)
	if err != nil {
		rel = path
	}
	sum := sha256.Sum256(raw)
	return ManifestFile{
		Path:   filepath.ToSlash(rel),
		Bytes:  int64(len(raw)),
		SHA256: hex.EncodeToString(sum[:]),
		Kind:   kind,
	}, nil
}

// writeAtomic writes via a temp file and renames, so a reader can never observe
// a half-written object — the same reason the manifest goes last.
func writeAtomic(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("finalize %s: %w", path, err)
	}
	return nil
}

func mustJSONBytes(v any) []byte {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Every type published here is a plain struct of JSON-safe fields; a
		// marshal failure would be a programming error, not a runtime condition.
		panic("attest: publish payload is not JSON-serializable: " + err.Error())
	}
	return append(raw, '\n')
}
