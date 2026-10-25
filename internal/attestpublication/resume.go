package attestpublication

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// PublishExport holds publication ownership before reading either the journal or
// export. Once journaled, the archived bytes remain authoritative on every retry.
func (publisher Publisher) PublishExport(ctx context.Context, repoID, exportDir, outputDir string) (PublishResult, error) {
	journalPath := filepath.Join(outputDir, "attest-upload.json")
	return withPublicationLock(ctx, journalPath, func(state *publicationState) (PublishResult, error) {
		journal, err := loadJournal(journalPath, state)
		var archive Package
		if errors.Is(err, os.ErrNotExist) {
			archive, err = buildPackage(exportDir, filepath.Join(outputDir, "attest-run.zip"), state.root)
			if err == nil {
				state.archive, err = state.root.Open("attest-run.zip")
			}
		} else if err == nil {
			// The CLI owns this fixed archive name. A forged journal cannot redirect
			// resume reads to another pathname, even inside the output directory.
			if filepath.Clean(journal.ArchivePath) != filepath.Clean(filepath.Join(outputDir, "attest-run.zip")) {
				return PublishResult{}, errors.New("journaled archive path does not match publication output")
			}
			state.archive, err = state.root.Open("attest-run.zip")
			if err == nil {
				archive, err = packageFromJournalFile(journal, state.archive)
			}
		}
		if state.archive != nil {
			defer state.archive.Close()
		}
		if err != nil {
			return PublishResult{}, fmt.Errorf("read frozen Attest package: %w", err)
		}
		return publisher.publish(ctx, repoID, journalPath, archive, state)
	})
}

func withPublicationLock(ctx context.Context, journalPath string, publish func(*publicationState) (PublishResult, error)) (PublishResult, error) {
	if err := ctx.Err(); err != nil {
		return PublishResult{}, err
	}
	root, err := openPublicationRoot(filepath.Dir(journalPath))
	if err != nil {
		return PublishResult{}, err
	}
	defer root.Close()
	lock, err := root.OpenFile(filepath.Base(journalPath)+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return PublishResult{}, fmt.Errorf("open publication lock: %w", err)
	}
	defer lock.Close()
	locked, err := lockPublicationFile(lock)
	if err != nil {
		return PublishResult{}, fmt.Errorf("lock attest publication: %w", err)
	}
	if !locked {
		return PublishResult{}, errors.New("another publication owns this output; retry after it finishes")
	}
	// Keep the inode in place; closing its handle releases kernel ownership.
	return publish(&publicationState{root: root})
}

func packageFromJournal(journal Journal) (Package, error) {
	file, err := os.Open(journal.ArchivePath)
	if err != nil {
		return Package{}, err
	}
	defer file.Close()
	return packageFromJournalFile(journal, file)
}

func packageFromJournalFile(journal Journal, file *os.File) (Package, error) {
	archiveSHA, size, err := hashArchiveFile(file)
	if err != nil {
		return Package{}, err
	}
	if archiveSHA != journal.Binding.ArchiveSHA256 || size != journal.Binding.ArchiveSizeBytes {
		return Package{}, errors.New("journaled archive changed; refusing to rebuild or resume different bytes")
	}
	archive, err := zip.NewReader(file, size)
	if err != nil {
		return Package{}, err
	}
	read := func(name string) ([]byte, error) {
		entry, err := archive.Open(name)
		if err != nil {
			return nil, err
		}
		defer entry.Close()
		return io.ReadAll(entry)
	}
	manifest, err := read(manifestName)
	if err != nil {
		return Package{}, err
	}
	if digest(manifest) != journal.Binding.ManifestSHA256 {
		return Package{}, errors.New("journaled archive manifest changed")
	}
	if err := validateJSON(manifestSchema, manifest); err != nil {
		return Package{}, fmt.Errorf("validate journaled manifest: %w", err)
	}
	if err := validateManifestSemantics(manifest); err != nil {
		return Package{}, fmt.Errorf("validate journaled manifest semantics: %w", err)
	}
	runRaw, err := read(runFilename)
	if err != nil {
		return Package{}, err
	}
	if err := validateJSON(runSchema, runRaw); err != nil {
		return Package{}, err
	}
	if err := validateRunSemantics(runRaw); err != nil {
		return Package{}, err
	}
	var run RunIdentity
	if err := json.Unmarshal(runRaw, &run); err != nil {
		return Package{}, err
	}
	pkg := Package{Run: run, Manifest: manifest, ManifestSHA256: digest(manifest), ArchivePath: journal.ArchivePath, ArchiveSHA256: archiveSHA, ArchiveSizeBytes: size}
	if err := journal.Matches(pkg); err != nil {
		return Package{}, err
	}
	return pkg, nil
}
