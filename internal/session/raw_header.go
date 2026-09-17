package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// ReadSessionHeader reads only the first JSONL record, retaining the existing
// normalized-reader record limit without loading any conversation entries.
func ReadSessionHeader(path string) (*StoreMeta, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	// Headers are normally small; preserve compatibility with the existing 10MiB
	// maximum record while avoiding its eager 1MiB buffer for every header read.
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("missing transcript header")
	}
	var header map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return nil, err
	}
	if metadata, ok := header["metadata"].(map[string]any); ok && (header["type"] == "header" || header["type"] == nil) {
		return ParseStoreMeta(metadata), nil
	}
	if metadata, ok := header["_meta"].(map[string]any); ok {
		return ParseStoreMeta(metadata), nil
	}
	return nil, fmt.Errorf("unrecognized transcript header")
}
