package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

const cursorMaxSourceBytes int64 = 64 << 20

var (
	errCursorInvalidOffset  = errors.New("invalid-offset")
	errCursorSourceChanged  = errors.New("source-changed")
	errCursorSourceTooLarge = errors.New("source-too-large")
)

type cursorReadBatch struct {
	entries []adapterprotocol.RawEntry
	offset  int64
	skipped int
}

func handleRead(p adapterprotocol.ReadParams) (*adapterprotocol.ReadResult, error) {
	batch, err := readCursorFromOffset(p.SessionFile, 0)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadResult{Entries: batch.entries, Skipped: batch.skipped}, nil
}

func handleReadMetadata(p adapterprotocol.ReadParams) (*adapterprotocol.ReadMetadataResult, error) {
	if _, err := readCursorFromOffset(p.SessionFile, 0); err != nil {
		return nil, err
	}
	// This export has no structured model or Cursor-version metadata.
	return &adapterprotocol.ReadMetadataResult{}, nil
}

func handleReadFromOffset(p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
	batch, err := readCursorFromOffset(p.SessionFile, p.Offset)
	if err != nil {
		return nil, err
	}
	return &adapterprotocol.ReadFromOffsetResult{Entries: batch.entries, NewOffset: batch.offset}, nil
}

func readCursorFromOffset(path string, offset int64) (*cursorReadBatch, error) {
	return readCursorSnapshot(path, offset, adapterruntime.TailJSONLWithStats)
}

func readCursorSnapshot(path string, offset int64, tail func(string, int64, adapterruntime.LineParser) ([]adapterprotocol.RawEntry, int64, adapterruntime.TailStats, error)) (*cursorReadBatch, error) {
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".jsonl" {
		return nil, errors.New("invalid-source-path: expected an absolute JSONL path")
	}
	before, err := os.Stat(path)
	if err != nil {
		return nil, cursorSourceReadError(err)
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("invalid-source-path: expected a regular file")
	}
	if before.Size() > cursorMaxSourceBytes {
		return nil, fmt.Errorf("%w: maximum source size is 64 MiB", errCursorSourceTooLarge)
	}
	if offset < 0 || offset > before.Size() {
		return nil, errCursorInvalidOffset
	}
	if offset > 0 {
		f, openErr := os.Open(path)
		if openErr != nil {
			return nil, cursorSourceReadError(openErr)
		}
		var previous [1]byte
		_, readErr := f.ReadAt(previous[:], offset-1)
		_ = f.Close()
		if readErr != nil || previous[0] != '\n' {
			return nil, errCursorInvalidOffset
		}
	}
	var firstParseError error
	skipped := 0
	entries, next, stats, err := tail(path, offset, func(line []byte) ([]adapterprotocol.RawEntry, error) {
		parsed, parseErr := parseCursorLine(line)
		if parseErr != nil && firstParseError == nil {
			firstParseError = parseErr
		}
		if parseErr == nil && len(parsed) == 0 {
			skipped++
		}
		return parsed, parseErr
	})
	if err != nil {
		return nil, cursorSourceReadError(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errCursorSourceChanged
	}
	if stats.ParseErrors > 0 {
		if firstParseError != nil {
			return nil, firstParseError
		}
		return nil, fmt.Errorf("%w: complete record exceeds the line limit", errCursorSourceFormat)
	}
	if entries == nil {
		entries = []adapterprotocol.RawEntry{}
	}
	return &cursorReadBatch{entries: entries, offset: next, skipped: skipped}, nil
}

func cursorSourceReadError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err // keep the failure category, not a private native path
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("source-not-found: %w", err)
	}
	return fmt.Errorf("source-unreadable: %w", err)
}
