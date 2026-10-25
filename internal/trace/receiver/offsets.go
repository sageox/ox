package receiver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/sageox/ox/internal/trace/model"
)

// SnapshotOffsets observes complete requests under the same lock as append and
// prune. Missing files are zero; unsafe or unreadable entries are omitted so a
// caller cannot accidentally widen a capture window after an I/O failure.
func SnapshotOffsets(spool string, ids []string) (map[string]model.Offsets, error) {
	result := make(map[string]model.Offsets, len(ids))
	root, err := openSpool(spool, false)
	if errors.Is(err, fs.ErrNotExist) {
		for _, id := range ids {
			if ValidSessionID(id) {
				result[strings.ToLower(id)] = model.Offsets{}
			}
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer root.Close()
	unlock, err := lockSpool(root)
	if err != nil {
		return result, err
	}
	defer unlock()
	var failures []error
	for _, id := range ids {
		id = strings.ToLower(id)
		if !ValidSessionID(id) {
			failures = append(failures, errors.New("invalid native trace session id"))
			continue
		}
		offsets, err := snapshotSessionOffsets(root, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		result[id] = offsets
	}
	return result, errors.Join(failures...)
}

func snapshotSessionOffsets(root *os.Root, id string) (model.Offsets, error) {
	var offsets model.Offsets
	if err := safeEntry(root, id, true); err != nil {
		return offsets, err
	}
	dir, err := root.OpenRoot(id)
	if errors.Is(err, fs.ErrNotExist) {
		return offsets, nil
	}
	if err != nil {
		return offsets, err
	}
	defer dir.Close()
	for name, dest := range map[string]*int64{"traces.jsonl": &offsets.Spans, "logs.jsonl": &offsets.Events} {
		if err := safeEntry(dir, name, false); err != nil {
			return model.Offsets{}, err
		}
		info, err := dir.Stat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return model.Offsets{}, fmt.Errorf("stat trace spool: %w", err)
		}
		*dest = info.Size()
	}
	return offsets, nil
}
