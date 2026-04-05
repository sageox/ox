package adapterruntime

import (
	"fmt"
	"strings"
)

// ValidateSessionID checks that a session ID is safe to use in file path
// construction. It rejects path traversal attempts (../, absolute paths)
// and path separators that could escape the intended directory.
func ValidateSessionID(id string) error {
	if id == "" {
		return nil
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("invalid session ID: contains path traversal")
	}
	if strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("invalid session ID: contains path separator")
	}
	return nil
}
