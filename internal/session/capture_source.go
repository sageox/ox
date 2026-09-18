package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/pkg/codexhistory"
	"github.com/sageox/ox/pkg/sessionprovenance"
)

// SaveCaptureSource records a bounded native snapshot before the recording
// marker is retired. It contains no transcript text or absolute source paths.
func SaveCaptureSource(state *RecordingState) error {
	if state.AdapterName != "codex" || state.AgentSessionID == "" {
		return nil
	}
	// Older recordings sometimes stored an opaque adapter handle here. Keep
	// those as legacy captures; never manufacture a native provenance receipt.
	if _, err := sessionprovenance.Path(state.AgentSessionID); err != nil {
		return nil
	}
	snap, err := codexhistory.Stream(context.Background(), state.SessionFile, nil)
	if err != nil {
		return fmt.Errorf("capture provenance: %w", err)
	}
	if snap.NativeID != state.AgentSessionID {
		return fmt.Errorf("capture native identity changed")
	}
	end := state.SourceOffset
	if end <= state.StartOffset || end > snap.Size {
		return fmt.Errorf("capture source coverage is incomplete")
	}
	ranges, err := captureSourceRanges(state, end)
	if err != nil {
		return err
	}
	source := sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: snap.NativeID, Generation: snap.Generation, SnapshotDigest: snap.Digest, ParserVersion: codexhistory.ParserVersion, ParentSessionID: snap.ParentID, CapturedAt: state.StartedAt, Ranges: ranges}
	return fileutil.AtomicWriteJSON(filepath.Join(state.SessionPath, ".capture-source.json"), source, 0600)
}
func captureSourceRanges(state *RecordingState, end int64) ([]sessionprovenance.Range, error) {
	start := state.StartOffset
	var ranges []sessionprovenance.Range
	paused := false
	last := start
	for _, event := range state.Lifecycle {
		if event.Action != LifecycleActionPause && event.Action != LifecycleActionResume {
			continue
		}
		if !event.SourceOffsetKnown || event.Offset < last || event.Offset > end {
			return nil, fmt.Errorf("legacy pause boundary lacks provable native coverage")
		}
		last = event.Offset
		switch event.Action {
		case LifecycleActionPause:
			if paused {
				return nil, fmt.Errorf("invalid pause timeline")
			}
			if event.Offset > start {
				ranges = append(ranges, sessionprovenance.Range{Start: start, End: event.Offset})
			}
			paused = true
		case LifecycleActionResume:
			if !paused {
				return nil, fmt.Errorf("invalid resume timeline")
			}
			start = event.Offset
			paused = false
		}
	}
	if !paused && start < end {
		ranges = append(ranges, sessionprovenance.Range{Start: start, End: end})
	}
	if len(ranges) == 0 {
		return nil, fmt.Errorf("capture has no eligible source ranges")
	}
	return ranges, nil
}
func ReadCaptureSource(dir string) (*sessionprovenance.Source, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".capture-source.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var source sessionprovenance.Source
	if err = json.Unmarshal(b, &source); err != nil {
		return nil, err
	}
	if source.Version != 1 || source.Agent != "codex" {
		return nil, fmt.Errorf("invalid capture source")
	}
	if _, err = sessionprovenance.Path(source.NativeSessionID); err != nil {
		return nil, err
	}
	return &source, nil
}

// RecordSourceCoverage must share the pointer/metadata commit and Ledger lock.
// Existing exclusions and projection fields always survive a new publication.
func RecordSourceCoverage(ledger, name, oid string, source *sessionprovenance.Source) (string, error) {
	// FileRef retains its historical algorithm prefix; provenance uses canonical hex.
	oid = strings.TrimPrefix(oid, "sha256:")
	if source == nil {
		return "", nil
	}
	record, err := ReadSourceRecord(ledger, source.NativeSessionID)
	if err != nil {
		return "", err
	}
	if record == nil {
		record = &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: source.NativeSessionID, Generation: source.Generation}
	}
	if record.Generation != "" && record.Generation != source.Generation {
		return "", fmt.Errorf("source generation conflict")
	}
	changed := record.Generation != source.Generation || record.UpdatedAt.IsZero()
	record.Generation = source.Generation
	for _, span := range source.Ranges {
		if record.Excludes(span.Start, span.End) {
			return "", fmt.Errorf("source range excluded")
		}
		found := false
		for _, coverage := range record.Coverage {
			if span.Start < coverage.End && span.End > coverage.Start {
				if coverage.SessionName == name && coverage.Start == span.Start && coverage.End == span.End && coverage.RawOID == oid {
					found = true
					continue
				}
				return "", fmt.Errorf("conflicting source coverage")
			}
		}
		if !found {
			changed = true
			record.Coverage = append(record.Coverage, sessionprovenance.Coverage{Start: span.Start, End: span.End, SessionName: name, RawOID: oid})
		}
	}
	if !changed {
		return sessionprovenance.Path(source.NativeSessionID)
	}
	record.UpdatedAt = time.Now().UTC()
	if err = WriteSourceRecord(ledger, record); err != nil {
		return "", err
	}
	return sessionprovenance.Path(source.NativeSessionID)
}

// CheckCapturePublication runs before copying or uploading content. The commit
// path repeats reconciliation because another machine can delete it meanwhile.
func CheckCapturePublication(ctx context.Context, ledger, name, rawPath string, source *sessionprovenance.Source) error {
	// Explicit deletion intent must also stop a queued capture/summary retry.
	for _, marker := range []string{".deletion-pending.json", ".capture-exclusion-pending.json"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(rawPath), marker)); err == nil {
			return fmt.Errorf("source has pending local deletion or capture exclusion")
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	if source == nil {
		return nil
	}
	return gitutil.WithRepoLock(ctx, ledger, func() error {
		if err := gitutil.CheckSourcePublication(ctx, ledger); err != nil {
			return err
		}
		record, err := ReadSourceRecord(ledger, source.NativeSessionID)
		if err != nil {
			return err
		}
		if source.Version != 1 || source.Agent != "codex" || source.Generation == "" || len(source.Ranges) == 0 {
			return fmt.Errorf("invalid capture source")
		}
		for _, span := range source.Ranges {
			if span.Start < 0 || span.End <= span.Start {
				return fmt.Errorf("invalid source range")
			}
		}
		if record == nil {
			return nil
		}
		if record.Generation != "" && record.Generation != source.Generation {
			return fmt.Errorf("source generation conflict")
		}
		var oid string
		for _, span := range source.Ranges {
			if span.Start < 0 || span.End <= span.Start {
				return fmt.Errorf("invalid source range")
			}
			if record.Excludes(span.Start, span.End) {
				return fmt.Errorf("source range excluded")
			}
			for _, coverage := range record.Coverage {
				if span.Start >= coverage.End || span.End <= coverage.Start {
					continue
				}
				if coverage.SessionName != name || coverage.Start != span.Start || coverage.End != span.End {
					return fmt.Errorf("conflicting source coverage")
				}
				if oid == "" && lfs.IsPointerFile(rawPath) {
					ref, err := lfs.ReadPointerFile(rawPath)
					if err != nil {
						return err
					}
					oid = ref.BareOID()
				}
				if oid == "" {
					file, err := os.Open(rawPath)
					if err != nil {
						return err
					}
					hash := sha256.New()
					_, err = io.Copy(hash, file)
					closeErr := file.Close()
					if err != nil {
						return err
					}
					if closeErr != nil {
						return closeErr
					}
					oid = fmt.Sprintf("%x", hash.Sum(nil))
				}
				if coverage.RawOID != oid {
					return fmt.Errorf("conflicting source content")
				}
			}
		}
		return nil
	})
}
