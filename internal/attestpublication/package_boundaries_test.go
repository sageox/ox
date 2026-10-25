package attestpublication

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPackageRejectsBrokenExportBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{"missing run", func(t *testing.T, p string) { mustRemoveBoundary(t, filepath.Join(p, runFilename)) }, "requires a valid run.json"},
		{"malformed run", func(t *testing.T, p string) { mustWriteBoundary(t, filepath.Join(p, runFilename), []byte("{")) }, "decode run.json"},
		{"unsupported schema", func(t *testing.T, p string) {
			mutateBoundaryRun(t, p, func(r map[string]any) { r["schema_version"] = "999" })
		}, "validate frozen run.json"},
		{"unknown instance", func(t *testing.T, p string) {
			mutateBoundaryRun(t, p, func(r map[string]any) {
				r["outcomes"].([]any)[0].(map[string]any)["instance"].(map[string]any)["scenario_key"] = strings.Repeat("f", 64)
			})
		}, "unknown corpus instance"},
		{"duplicate outcome", func(t *testing.T, p string) {
			mutateBoundaryRun(t, p, func(r map[string]any) { o := r["outcomes"].([]any); r["outcomes"] = append(o, o[0]) })
		}, "duplicate outcome"},
		{"missing outcome", func(t *testing.T, p string) {
			mutateBoundaryRun(t, p, func(r map[string]any) { r["outcomes"] = r["outcomes"].([]any)[1:] })
		}, "missing outcome"},
		{"changed scenario key", func(t *testing.T, p string) {
			mutateBoundaryRun(t, p, func(r map[string]any) {
				firstBoundaryFeature(r)["scenarios"].([]any)[0].(map[string]any)["key"] = strings.Repeat("e", 64)
			})
		}, "has key"},
		{"embedded manifest", func(t *testing.T, p string) { mustWriteBoundary(t, filepath.Join(p, manifestName), []byte("{}")) }, "must not contain manifest.json"},
		{"unsafe file name", func(t *testing.T, p string) { mustWriteBoundary(t, filepath.Join(p, "secret%2f.txt"), nil) }, "unsafe relative path"},
		{"symlink entry", func(t *testing.T, p string) {
			if err := os.Symlink(runFilename, filepath.Join(p, "alias")); err != nil {
				t.Skip(err)
			}
		}, "contains symlink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			export := testExport(t)
			tc.mutate(t, export)
			target := filepath.Join(t.TempDir(), "archive.zip")
			if _, err := BuildPackage(export, target); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid export produced archive: %v", err)
			}
		})
	}
}

func TestBuildPackageRejectsUnavailablePaths(t *testing.T) {
	export := testExport(t)
	blocker := filepath.Join(t.TempDir(), "file")
	mustWriteBoundary(t, blocker, nil)
	for _, tc := range []struct{ source, target string }{
		{filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "archive.zip")},
		{export, filepath.Join(blocker, "archive.zip")},
		{export, ""},
	} {
		if _, err := BuildPackage(tc.source, tc.target); err == nil {
			t.Fatal("unavailable package path accepted")
		}
	}
}

func TestManifestAssemblyRejectsAmbiguousFiles(t *testing.T) {
	for _, files := range [][]ManifestFile{
		nil, {{Path: manifestName}}, {{Path: "../run.json"}}, {{Path: "report/index.html"}},
		{{Path: runFilename}, {Path: runFilename}}, {{Path: runFilename}, {Path: "report/index.html"}},
	} {
		if _, err := marshalManifest(files); err == nil {
			t.Fatalf("ambiguous manifest accepted: %#v", files)
		}
	}
}

func TestUnknownEvidenceTypeRemainsBinary(t *testing.T) {
	export := testExport(t)
	mustWriteBoundary(t, filepath.Join(export, "capture.opaque"), []byte{0, 1, 2, 255})
	pkg, err := BuildPackage(export, filepath.Join(t.TempDir(), "archive.zip"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(pkg.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, file := range manifest.Files {
		if file.Path == "capture.opaque" {
			if file.ContentType != "application/octet-stream" {
				t.Fatal(file.ContentType)
			}
			return
		}
	}
	t.Fatal("opaque evidence omitted")
}

func TestArchiveWriteFailuresPreserveTargetAndRemoveTemporary(t *testing.T) {
	for _, kind := range []string{"missing source", "directory target", "closed output"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			output, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			source, err := os.OpenRoot(testExport(t))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			files := []ManifestFile{{Path: runFilename}}
			target := "archive.zip"
			switch kind {
			case "missing source":
				files[0].Path = "absent"
			case "directory target":
				if err := output.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "closed output":
				_ = output.Close()
			}
			if err := writeArchive(target, output, source, files, []byte("{}")); err == nil {
				t.Fatal("archive failure was accepted")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".attest-archive-") {
					t.Fatalf("temporary archive leaked: %s", entry.Name())
				}
			}
		})
	}
}

func TestPublicationStateRejectsUnavailableDirectoryHandles(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = root.Close()
	if _, err := openPublicationChild(root, "child"); err == nil {
		t.Fatal("closed parent accepted")
	}
	if _, _, err := createPublicationTemp(root, ".temp-"); err == nil {
		t.Fatal("closed output accepted")
	}
	if err := (&publicationState{root: root}).save("journal", Journal{Version: 1}); err == nil {
		t.Fatal("journal on closed output accepted")
	}
	source, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatal(err)
	}
	_ = source.Close()
	if _, _, err := hashArchiveFile(source); err == nil {
		t.Fatal("closed archive accepted")
	}
	if _, err := lockPublicationFile(source); err == nil {
		t.Fatal("closed lock accepted")
	}
}

func TestPublicationJournalRenameFailureCleansTemporary(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.Mkdir("journal", 0700); err != nil {
		t.Fatal(err)
	}
	if err := (&publicationState{root: root}).save("journal", Journal{Version: 1}); err == nil {
		t.Fatal("directory journal overwritten")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "journal" || !entries[0].IsDir() {
		t.Fatalf("journal failure changed directory: %#v", entries)
	}
}

func TestPublicationLockRejectsInvalidLockPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal")
	if err := os.Mkdir(path+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := withPublicationLock(context.Background(), path, func(*publicationState) (PublishResult, error) {
		t.Fatal("invalid lock invoked publisher")
		return PublishResult{}, nil
	}); err == nil {
		t.Fatal("directory lock accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := withPublicationLock(ctx, path, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestResumeRejectsInvalidArchiveAndSemanticSources(t *testing.T) {
	original := testPackage(t)
	run, err := os.ReadFile(filepath.Join(testExport(t), runFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		entries map[string][]byte
		change  func(*Journal)
		want    string
	}{
		{"missing manifest", map[string][]byte{runFilename: run}, nil, "file does not exist"},
		{"missing run", map[string][]byte{manifestName: original.Manifest}, nil, "file does not exist"},
		{"wrong manifest hash", map[string][]byte{manifestName: original.Manifest, runFilename: run}, func(j *Journal) { j.Binding.ManifestSHA256 = strings.Repeat("0", 64) }, "manifest changed"},
		{"malformed run", map[string][]byte{manifestName: original.Manifest, runFilename: []byte("{")}, nil, "unexpected EOF"},
		{"different source identity", map[string][]byte{manifestName: original.Manifest, runFilename: run}, func(j *Journal) { j.SourceRunID = "another-run" }, "frozen package changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := zip.NewWriter(&body)
			for name, raw := range tc.entries {
				entry, err := writer.Create(name)
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
			path := filepath.Join(t.TempDir(), "archive.zip")
			mustWriteBoundary(t, path, body.Bytes())
			j := Journal{Version: 1, SourceRunID: original.Run.SourceRunID, ArchivePath: path, Binding: Binding{ArchiveSHA256: digest(body.Bytes()), ArchiveSizeBytes: int64(body.Len()), ManifestSHA256: digest(tc.entries[manifestName])}}
			if tc.change != nil {
				tc.change(&j)
			}
			if _, err := packageFromJournal(j); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "not-a-zip")
	raw := []byte("not an archive")
	mustWriteBoundary(t, path, raw)
	if _, err := packageFromJournal(Journal{ArchivePath: path, Binding: Binding{ArchiveSHA256: digest(raw), ArchiveSizeBytes: int64(len(raw))}}); err == nil {
		t.Fatal("nonzip archive accepted")
	}
	if _, err := packageFromJournal(Journal{ArchivePath: path + "missing"}); err == nil {
		t.Fatal("missing archive accepted")
	}
}

func firstBoundaryFeature(run map[string]any) map[string]any {
	return run["corpus"].(map[string]any)["capabilities"].([]any)[0].(map[string]any)["features"].([]any)[0].(map[string]any)
}
func mutateBoundaryRun(t *testing.T, dir string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, runFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	mutate(run)
	raw, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteBoundary(t, path, raw)
}
func mustWriteBoundary(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func mustRemoveBoundary(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
