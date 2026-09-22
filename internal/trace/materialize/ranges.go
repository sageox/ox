package materialize

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/trace/model"
)

type selection struct {
	native    model.NativeRanges
	stop      model.Offsets
	stopKnown bool
}

func validID(id string) bool {
	return len(id) == 36 && uuid.Validate(id) == nil && id == strings.ToLower(id)
}

func ranges(capture *model.Capture) ([]selection, *model.Metadata, error) {
	if len(capture.Boundaries) < 2 || capture.Boundaries[0].Action != "start" || capture.Boundaries[len(capture.Boundaries)-1].Action != "stop" {
		return nil, nil, errors.New("trace capture requires fixed start and stop boundaries")
	}
	meta := &model.Metadata{StoppedAt: capture.Boundaries[len(capture.Boundaries)-1].At, Scrubbed: map[string]int64{}, Errors: append([]string(nil), capture.Errors...)}
	byID := map[string]*selection{}
	lastKnown := map[string]model.Offsets{}
	active := false
	for i, boundary := range capture.Boundaries {
		if boundary.At.IsZero() {
			return nil, nil, errors.New("trace boundary has no timestamp")
		}
		for id, offset := range boundary.Offsets {
			if !validID(id) || offset.Spans < 0 || offset.Events < 0 {
				return nil, nil, errors.New("invalid trace session or byte offset")
			}
			if previous, ok := lastKnown[id]; ok && (offset.Spans < previous.Spans || offset.Events < previous.Events) {
				return nil, nil, errors.New("trace spool shrank between known boundaries")
			}
			lastKnown[id] = offset
			if byID[id] == nil {
				byID[id] = &selection{native: model.NativeRanges{ID: id, SpansBytes: []model.ByteRange{}, EventsBytes: []model.ByteRange{}}}
			}
		}
		if i > 0 {
			previous := capture.Boundaries[i-1]
			for id, start := range previous.Offsets {
				end, ok := boundary.Offsets[id]
				if !ok {
					meta.Errors = append(meta.Errors, "trace boundary missing a session offset; range omitted")
					continue
				}
				if end.Spans < start.Spans || end.Events < start.Events {
					return nil, nil, errors.New("trace spool shrank between boundaries")
				}
				if active {
					byID[id].native.SpansBytes = addRange(byID[id].native.SpansBytes, start.Spans, end.Spans)
					byID[id].native.EventsBytes = addRange(byID[id].native.EventsBytes, start.Events, end.Events)
				} else {
					meta.PausedBytesSkipped += (end.Spans - start.Spans) + (end.Events - start.Events)
				}
			}
		}
		switch boundary.Action {
		case "start":
			if i != 0 {
				return nil, nil, errors.New("duplicate trace start boundary")
			}
			active = true
		case "resume":
			active = true
		case "pause":
			active = false
		case "native-session":
		case "stop":
			if i != len(capture.Boundaries)-1 {
				return nil, nil, errors.New("trace data after stop boundary")
			}
			for id, offset := range boundary.Offsets {
				byID[id].stop = offset
				byID[id].stopKnown = true
			}
		default:
			return nil, nil, fmt.Errorf("unknown trace boundary action %q", boundary.Action)
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]selection, 0, len(ids))
	for _, id := range ids {
		result = append(result, *byID[id])
	}
	return result, meta, nil
}

func addRange(ranges []model.ByteRange, start, end int64) []model.ByteRange {
	if start == end {
		return ranges
	}
	if n := len(ranges); n > 0 && ranges[n-1][1] == start {
		ranges[n-1][1] = end
		return ranges
	}
	return append(ranges, model.ByteRange{start, end})
}
