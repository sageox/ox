package session

// Storage defines the interface for session persistence.
// Implementations include FileStorage (default), MemoryStorage (testing),
// and future CloudStorage (SageOx API sync).
type Storage interface {
	// Save writes a complete session with metadata and entries.
	// The filename should include the .jsonl extension.
	// The sessionType should be "raw" or "events".
	Save(filename, sessionType string, meta *StoreMeta, entries []map[string]any) error

	// Load reads a session by filename.
	// Searches both raw and events directories for file-based storage.
	Load(filename string) (*StoredSession, error)

	// List returns all session infos, sorted by date descending.
	List() ([]SessionInfo, error)

	// Delete removes a session by filename.
	Delete(filename string) error

	// Exists checks if a session with the given filename exists.
	Exists(filename string) bool

	// GetLatest returns the most recent session info.
	GetLatest() (*SessionInfo, error)
}

// Ensure Store implements Storage interface.
var _ Storage = (*Store)(nil)

// Save implements Storage.Save for file-based storage.
// It creates a session file, writes header, entries, and footer.
func (s *Store) Save(filename, sessionType string, meta *StoreMeta, entries []map[string]any) error {
	var writer *SessionWriter
	var err error

	writer, err = s.CreateRaw(filename)
	if err != nil {
		return err
	}

	if err := writer.WriteHeader(meta); err != nil {
		writer.Close()
		return err
	}

	for _, entry := range entries {
		if err := writer.WriteRaw(entry); err != nil {
			writer.Close()
			return err
		}
	}

	return writer.Close()
}

// Load implements Storage.Load for file-based storage.
func (s *Store) Load(filename string) (*StoredSession, error) {
	return s.ReadSession(filename)
}

// List implements Storage.List for file-based storage.
func (s *Store) List() ([]SessionInfo, error) {
	return s.ListSessions()
}

// Exists implements Storage.Exists for file-based storage.
func (s *Store) Exists(filename string) bool {
	_, err := s.ReadSession(filename)
	return err == nil
}

// GetLatest implements Storage.GetLatest for file-based storage.
// Already implemented on Store, this is just for interface compliance.
