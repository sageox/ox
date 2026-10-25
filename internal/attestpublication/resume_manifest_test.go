package attestpublication

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageFromJournalValidatesManifest(t *testing.T) {
	original := testPackage(t)
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{name: "valid"},
		{name: "unsupported version", mutate: func(manifest map[string]any) {
			manifest["package_version"] = "2"
		}, want: "validate journaled manifest:"},
		{name: "unknown field", mutate: func(manifest map[string]any) {
			manifest["unexpected"] = true
		}, want: "validate journaled manifest:"},
		{name: "unsafe path", mutate: func(manifest map[string]any) {
			manifest["files"].([]any)[0].(map[string]any)["path"] = "../index.html"
		}, want: "validate journaled manifest:"},
		{name: "negative size", mutate: func(manifest map[string]any) {
			manifest["files"].([]any)[0].(map[string]any)["size_bytes"] = -1
		}, want: "validate journaled manifest:"},
		{name: "missing run", mutate: func(manifest map[string]any) {
			manifest["files"] = manifest["files"].([]any)[:1]
		}, want: "validate journaled manifest semantics: manifest requires run.json"},
		{name: "unsorted paths", mutate: func(manifest map[string]any) {
			files := manifest["files"].([]any)
			files[0], files[1] = files[1], files[0]
		}, want: "validate journaled manifest semantics: manifest file paths must be sorted and unique"},
		{name: "duplicate paths", mutate: func(manifest map[string]any) {
			files := manifest["files"].([]any)
			manifest["files"] = append(files, files[len(files)-1])
		}, want: "validate journaled manifest semantics: manifest file paths must be sorted and unique"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var manifest map[string]any
			if err := json.Unmarshal(original.Manifest, &manifest); err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(manifest)
			}
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			journal := journalWithManifest(t, original, raw)
			resumed, err := packageFromJournal(journal)
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("resume error = %v, want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(resumed.Manifest, raw) {
				t.Fatal("resume changed the bound manifest")
			}
		})
	}
}

// Rebind both hashes so rejection proves manifest validation, not corruption detection.
func journalWithManifest(t *testing.T, original Package, manifest []byte) Journal {
	t.Helper()
	archive, err := zip.OpenReader(original.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	for _, file := range archive.File {
		raw := manifest
		if file.Name != manifestName {
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			raw, err = io.ReadAll(reader)
			closeErr := reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		}
		entry, err := writer.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rebound.zip")
	if err := os.WriteFile(path, body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return Journal{Version: 1, SourceRunID: original.Run.SourceRunID, ArchivePath: path,
		Binding: Binding{ManifestSHA256: digest(manifest), ArchiveSHA256: digest(body.Bytes()), ArchiveSizeBytes: int64(body.Len())}}
}
