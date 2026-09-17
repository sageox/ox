package codexhistory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/sessionhistory"
	"github.com/sageox/ox/pkg/sessionprovenance"
)

const ParserVersion = "codex-jsonl-v1"

// A single record may contain a large tool result; the bounded record limit
// avoids unbounded allocation on corrupt input without limiting session size.
const MaxRecordBytes = 32 * 1024 * 1024

// Snapshot remains an alias for existing Codex capture callers.
type Snapshot = sessionhistory.Snapshot

func Home() (string, error) {
	if s := os.Getenv("CODEX_HOME"); s != "" {
		return filepath.Abs(s)
	}
	h, e := os.UserHomeDir()
	return filepath.Join(h, ".codex"), e
}

// Discover deliberately has no calendar-directory limit: resumed and archived
// sessions often live under a creation date much older than their last turn.
func Discover(home string) ([]string, error) {
	var paths []string
	for _, dir := range []string{"sessions", "archived_sessions"} {
		err := filepath.WalkDir(filepath.Join(home, dir), func(p string, d os.DirEntry, e error) error {
			if errors.Is(e, os.ErrNotExist) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			// Regular files only: a FIFO or device node with a .jsonl suffix
			// would otherwise reach Stream/Inspect's os.Open, which can block
			// indefinitely on a FIFO with no writer -- uncancelable via ctx.
			if d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".jsonl") {
				paths = append(paths, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// Stream validates and hashes one fixed, newline-complete source generation.
// A callback receives entries in source order; tool calls/results retain their
// call IDs rather than buffering an unbounded number of pending calls.
func Stream(ctx context.Context, path string, emit func(adapterprotocol.RawEntry) error) (Snapshot, error) {
	var s Snapshot
	s.Path = path
	f, err := os.Open(path)
	if err != nil {
		return s, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return s, err
	}
	if !before.Mode().IsRegular() {
		return s, fmt.Errorf("source is not a regular file")
	}
	s.Size = before.Size()
	s.ModifiedAt = before.ModTime()
	h := sha256.New()
	rd := bufio.NewReaderSize(io.TeeReader(io.LimitReader(f, s.Size), h), 64*1024)
	var offset int64
	lineNo := 0
	for {
		if err = ctx.Err(); err != nil {
			return s, err
		}
		var line []byte
		for {
			part, e := rd.ReadSlice('\n')
			if len(line)+len(part) > MaxRecordBytes {
				return s, fmt.Errorf("source record exceeds %d bytes at line %d", MaxRecordBytes, lineNo+1)
			}
			line = append(line, part...)
			if errors.Is(e, bufio.ErrBufferFull) {
				continue
			}
			err = e
			break
		}
		if len(line) == 0 && errors.Is(err, io.EOF) {
			break
		}
		lineNo++
		offset += int64(len(line))
		if err != nil {
			return s, fmt.Errorf("incomplete source record at line %d", lineNo)
		}
		var raw struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if e := json.Unmarshal(line, &raw); e != nil {
			return s, fmt.Errorf("invalid JSON at line %d", lineNo)
		}
		ts, e := time.Parse(time.RFC3339Nano, raw.Timestamp)
		if e == nil {
			if s.StartedAt.IsZero() {
				s.StartedAt = ts
			}
			if ts.After(s.LastActivity) {
				s.LastActivity = ts
			}
		}
		if lineNo == 1 {
			header, err := parseHeader(line)
			if err != nil {
				return s, err
			}
			s.NativeID, s.CWD, s.ParentID, s.Internal, s.Generation = header.NativeID, header.CWD, header.ParentID, header.Internal, header.Generation
		}
		if raw.Type == "event_msg" {
			var ev struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw.Payload, &ev)
			switch ev.Type {
			case "task_started":
				s.InFlight = true
			case "task_complete", "task_completed", "turn_aborted":
				s.InFlight = false
			}
		}
		entries, e := ParseLine(line)
		if e != nil {
			return s, fmt.Errorf("invalid Codex content at line %d: %w", lineNo, e)
		}
		for _, entry := range entries {
			if _, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err != nil {
				return s, fmt.Errorf("invalid conversation timestamp at line %d", lineNo)
			}
			s.Entries++
			if emit != nil {
				if e = emit(entry); e != nil {
					return s, e
				}
			}
		}
	}
	after, e := f.Stat()
	if e != nil {
		return s, e
	}
	current, e := os.Stat(path)
	if e != nil {
		return s, e
	}
	if !os.SameFile(before, current) || after.Size() != s.Size || !after.ModTime().Equal(s.ModifiedAt) || offset != s.Size {
		return s, fmt.Errorf("source changed during scan")
	}
	if s.NativeID == "" || s.StartedAt.IsZero() {
		return s, fmt.Errorf("source metadata is incomplete")
	}
	s.Digest = hex.EncodeToString(h.Sum(nil))
	return s, nil
}

// Inspect reads only identity/scope hints. Callers must still Stream and compare
// the validated snapshot before applying; this is never an upload receipt.
func Inspect(path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, MaxRecordBytes+1)).ReadBytes('\n')
	if err != nil || len(line) > MaxRecordBytes {
		return Snapshot{}, fmt.Errorf("invalid native session header")
	}
	return parseHeader(line)
}

func parseHeader(line []byte) (Snapshot, error) {
	var h struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Payload   struct {
			ID         string          `json:"id"`
			CWD        string          `json:"cwd"`
			Source     json.RawMessage `json:"source"`
			Originator string          `json:"originator"`
			Parent     string          `json:"forked_from_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &h); err != nil {
		return Snapshot{}, err
	}
	if h.Type != "session_meta" {
		return Snapshot{}, fmt.Errorf("source lacks session_meta header")
	}
	if _, err := sessionprovenance.Path(h.Payload.ID); err != nil {
		return Snapshot{}, err
	}
	if h.Timestamp.IsZero() {
		return Snapshot{}, fmt.Errorf("source header lacks timestamp")
	}
	// Header identity stays stable across appends. Stream separately hashes the
	// entire snapshot to detect replacement or edits between preview and apply.
	generation := sha256.Sum256(line)
	return Snapshot{NativeID: h.Payload.ID, CWD: h.Payload.CWD, ParentID: h.Payload.Parent, StartedAt: h.Timestamp,
		Generation: hex.EncodeToString(generation[:]),
		Internal:   bytes.Contains(h.Payload.Source, []byte("subagent")) || strings.Contains(h.Payload.Originator, "guardian") || strings.Contains(h.Payload.Originator, "summarizer")}, nil
}
