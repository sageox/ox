// Package materialize selects fixed recording byte windows from local trace
// spools and writes scrubbed gzip sidecars exclusively into the recording cache.
package materialize

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/sageox/ox/internal/trace/model"
)

const (
	SpansFile  = pipeline.LedgerFileTraceSpans
	EventsFile = pipeline.LedgerFileTraceEvents
)

// Build never extends the stop boundary to chase late exports. A nil capture
// means capture was not opted into at recording start and produces no files.
// Errors are returned for the caller to log without blocking recording upload.
func Build(spoolDir, cacheDir string, capture *model.Capture) (*model.Metadata, error) {
	if capture == nil {
		return nil, nil
	}
	selected, meta, err := ranges(capture)
	if err != nil {
		return nil, err
	}
	cache, err := openCache(cacheDir)
	if err != nil {
		return nil, err
	}
	defer cache.Close()
	if err := regularOrMissing(cache, ".trace-materialize.lock"); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(cacheDir, ".trace-materialize.lock"), flock.SetPermissions(0600))
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, errors.New("trace materialization is busy")
	}
	var spool *os.Root
	info, err := os.Lstat(spoolDir)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("unsafe trace spool directory")
		}
		spool, err = os.OpenRoot(spoolDir)
		if err != nil {
			return nil, err
		}
		defer spool.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	seen := observations{}
	temps := map[string]string{}
	defer func() {
		for _, name := range temps {
			_ = cache.Remove(name)
		}
	}()
	for _, signal := range []string{"spans", "events"} {
		name := SpansFile
		if signal == "events" {
			name = EventsFile
		}
		if err := regularOrMissing(cache, name); err != nil {
			return nil, err
		}
		temp := "." + name + "." + uuid.NewString() + ".tmp"
		temps[name] = temp
		file, err := cache.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		writer := gzip.NewWriter(file)
		writeErr := buildSignal(spool, selected, signal, writer, meta, seen)
		if err := errors.Join(writeErr, writer.Close(), file.Close()); err != nil {
			return nil, fmt.Errorf("materialize trace %s: %w", signal, err)
		}
	}
	// Publish only after both streams have been fully parsed, scrubbed, compressed,
	// and closed. A corrupt second stream never publishes an unsanitized/partial first.
	if err := publish(cache, temps); err != nil {
		return nil, err
	}
	for _, s := range selected {
		meta.NativeSessions = append(meta.NativeSessions, s.native)
	}
	applyObservations(meta, seen)
	return meta, nil
}

// publish restores the previous pair if publication of either file fails.
// Callers hold the cache lock, so simultaneous finalizers cannot see each
// other's staging files or interfere with rollback.
func publish(cache *os.Root, temps map[string]string) (err error) {
	backups := map[string]string{}
	published := map[string]bool{}
	defer func() {
		if err != nil {
			for name := range published {
				err = errors.Join(err, cache.Remove(name))
			}
			for name, backup := range backups {
				err = errors.Join(err, cache.Rename(backup, name))
			}
			return
		}
		for _, backup := range backups {
			_ = cache.Remove(backup)
		}
	}()
	for _, name := range []string{SpansFile, EventsFile} {
		if _, statErr := cache.Lstat(name); statErr == nil {
			backup := "." + name + "." + uuid.NewString() + ".backup"
			if err = cache.Rename(name, backup); err != nil {
				return err
			}
			backups[name] = backup
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return statErr
		}
	}
	for _, name := range []string{SpansFile, EventsFile} {
		if err = cache.Rename(temps[name], name); err != nil {
			return err
		}
		published[name] = true
		delete(temps, name)
	}
	return nil
}

func openCache(path string) (*os.Root, error) {
	clean := filepath.Clean(path)
	// The API accepts the canonical ledger session cache only. In particular,
	// <ledger>/sessions/<name> is refused before mkdir or any content write.
	cacheBase := filepath.Dir(filepath.Dir(clean))
	if filepath.Base(filepath.Dir(clean)) != "sessions" || filepath.Base(cacheBase) != "cache" || filepath.Base(filepath.Dir(cacheBase)) != ".sageox" {
		return nil, errors.New("trace output must be a ledger .sageox/cache/sessions directory")
	}
	if err := os.MkdirAll(clean, 0700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return nil, err
	}
	// Platform /tmp aliases are allowed, but links within the cache hierarchy are
	// refused: a cache-looking path must never redirect trace bytes into git.
	baseResolved, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(cacheBase)))
	if err != nil {
		return nil, err
	}
	expected := filepath.Join(baseResolved, ".sageox", "cache", "sessions", filepath.Base(clean))
	if resolved != expected {
		return nil, errors.New("trace cache hierarchy contains a symlink")
	}
	root, err := os.OpenRoot(clean)
	if err != nil {
		return nil, err
	}
	return root, nil
}

func regularOrMissing(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe trace file %q", name)
	}
	return nil
}

func buildSignal(spool *os.Root, selected []selection, signal string, out io.Writer, meta *model.Metadata, seen observations) error {
	for _, s := range selected {
		selectedRanges, stopOffset := s.native.SpansBytes, s.stop.Spans
		input := "traces.jsonl"
		if signal == "events" {
			selectedRanges, stopOffset = s.native.EventsBytes, s.stop.Events
			input = "logs.jsonl"
		}
		if spool == nil {
			if len(selectedRanges) > 0 {
				return errors.New("selected trace spool is missing")
			}
			continue
		}
		info, err := spool.Lstat(s.native.ID)
		if errors.Is(err, fs.ErrNotExist) && len(selectedRanges) == 0 {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe native trace directory")
		}
		session, err := spool.OpenRoot(s.native.ID)
		if err != nil {
			return err
		}
		err = readSignal(session, input, selectedRanges, stopOffset, s.stopKnown, signal, out, meta, seen)
		_ = session.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func readSignal(session *os.Root, input string, selected []model.ByteRange, stopOffset int64, stopKnown bool, signal string, out io.Writer, meta *model.Metadata, seen observations) error {
	if err := regularOrMissing(session, input); err != nil {
		return err
	}
	file, err := session.Open(input)
	if errors.Is(err, fs.ErrNotExist) && len(selected) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if stopKnown && info.Size() > stopOffset {
		meta.LateBytes += info.Size() - stopOffset
	}
	for _, window := range selected {
		if window[1] > info.Size() {
			return errors.New("selected trace range is no longer available")
		}
		if err := copyRange(file, window, out, signal, meta, seen); err != nil {
			return err
		}
	}
	return nil
}

func copyRange(file *os.File, window model.ByteRange, out io.Writer, signal string, meta *model.Metadata, seen observations) error {
	length := window[1] - window[0]
	decoder := json.NewDecoder(io.NewSectionReader(file, window[0], length))
	var cleanStart int64
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var last [1]byte
			_, readErr := file.ReadAt(last[:], window[1]-1)
			if errors.Is(err, io.ErrUnexpectedEOF) || (readErr == nil && last[0] != '\n') {
				meta.TrailingBytesSkipped += length - cleanStart
				break
			}
			return errors.New("invalid terminated OTLP JSON in selected trace range")
		}
		end := decoder.InputOffset()
		terminated, after, err := newlineAfter(file, window[0]+end, window[1])
		if err != nil {
			return err
		}
		if !terminated {
			meta.TrailingBytesSkipped += length - cleanStart
			break
		}
		cleanStart = after - window[0]
		valueDecoder := json.NewDecoder(bytes.NewReader(raw))
		valueDecoder.UseNumber()
		var value map[string]any
		if err := valueDecoder.Decode(&value); err != nil || value == nil {
			return errors.New("trace record must be an OTLP JSON object")
		}
		scrub(value, meta.Scrubbed, seen)
		if err := json.NewEncoder(out).Encode(value); err != nil {
			return err
		}
		if signal == "spans" {
			meta.SpansLines++
			meta.Spans += countRecords(value, signal)
		} else {
			meta.EventsLines++
			meta.Events += countRecords(value, signal)
		}
	}
	return nil
}

func newlineAfter(file *os.File, start, end int64) (bool, int64, error) {
	reader := io.NewSectionReader(file, start, end-start)
	var buf [4096]byte
	offset := start
	for {
		n, err := reader.Read(buf[:])
		for _, b := range buf[:n] {
			offset++
			if b == '\n' {
				return true, offset, nil
			}
			if !strings.ContainsRune(" \t\r", rune(b)) {
				return false, offset, errors.New("trace JSON records must be newline separated")
			}
		}
		if errors.Is(err, io.EOF) {
			return false, offset, nil
		}
		if err != nil {
			return false, offset, err
		}
	}
}
