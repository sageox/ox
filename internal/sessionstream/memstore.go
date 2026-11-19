package sessionstream

import (
	"context"
	"maps"
	"sort"
	"strings"
	"sync"
)

// MemoryStore is an in-memory ObjectStore with S3's conditional-write
// semantics, for the twin and tests. FailNextPut injects a storage failure.
type MemoryStore struct {
	mu      sync.Mutex
	objects map[string]memObject
	// FailNextPut, when non-nil, is returned by the next PutIfAbsent.
	FailNextPut error
	// FailAfterPut, when non-nil, stores the object and then returns this error:
	// the drop-after-write fault (bytes landed, caller believes they did not).
	FailAfterPut error
}

type memObject struct {
	body []byte
	meta map[string]string
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{objects: map[string]memObject{}} }

// PutIfAbsent implements ObjectStore.
func (m *MemoryStore) PutIfAbsent(_ context.Context, key string, body []byte, meta map[string]string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.FailNextPut; err != nil {
		m.FailNextPut = nil
		return false, err
	}
	if _, ok := m.objects[key]; ok {
		return false, nil
	}
	m.objects[key] = memObject{body: append([]byte(nil), body...), meta: maps.Clone(meta)}
	if err := m.FailAfterPut; err != nil {
		m.FailAfterPut = nil
		return false, err
	}
	return true, nil
}

// Get implements ObjectStore.
func (m *MemoryStore) Get(_ context.Context, key string) ([]byte, map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if !ok {
		return nil, nil, ErrObjectNotFound
	}
	return append([]byte(nil), o.body...), maps.Clone(o.meta), nil
}

// Keys lists stored keys under prefix in lexicographic order.
func (m *MemoryStore) Keys(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// DeletePrefix removes every object under prefix and reports how many.
func (m *MemoryStore) DeletePrefix(prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			delete(m.objects, k)
			n++
		}
	}
	return n
}

// Reassemble concatenates a layer's objects in offset order. It is the
// source-only rebuild a recovery drill compares against the client's bytes.
func (m *MemoryStore) Reassemble(ctx context.Context, bytesPrefix string) ([]byte, []IndexObject, error) {
	var out []byte
	var idx []IndexObject
	for _, k := range m.Keys(bytesPrefix) {
		off, ok := ParseBytesKeyOffset(k)
		if !ok {
			continue
		}
		b, _, err := m.Get(ctx, k)
		if err != nil {
			return nil, nil, err
		}
		idx = append(idx, IndexObject{Offset: off, Size: int64(len(b)), SHA256: HexSHA256(b)})
		out = append(out, b...)
	}
	return out, idx, nil
}
