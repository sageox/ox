package adapters

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

const adapterBinaryPrefix = "ox-adapter-"

// AdapterDirs returns the ordered list of directories to scan for adapter binaries.
// Priority: OX_ADAPTER_PATH (highest), bundled dir, user local dir.
func AdapterDirs() []string {
	var dirs []string

	// highest priority: OX_ADAPTER_PATH env var (colon-separated)
	if envPath := os.Getenv("OX_ADAPTER_PATH"); envPath != "" {
		dirs = append(dirs, filepath.SplitList(envPath)...)
	}

	// bundled: same directory as the ox binary
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}

	// user local: platform-specific
	switch runtime.GOOS {
	case "darwin", "linux":
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, ".local", "share", "ox", "adapters"))
		}
	case "windows":
		if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
			dirs = append(dirs, filepath.Join(appData, "ox", "adapters"))
		}
	}

	return dirs
}

// DiscoverExternalAdapters scans known directories for ox-adapter-* binaries,
// calls info on each, and returns successfully discovered adapters.
//
// The returned adapters are not yet registered. The caller decides whether
// to register them (and whether they supersede built-in adapters).
func DiscoverExternalAdapters() []*ExternalAdapter {
	dirs := AdapterDirs()
	seen := make(map[string]bool) // track adapter names to avoid duplicates

	var adapters []*ExternalAdapter

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			// directory doesn't exist or is unreadable — not an error
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasPrefix(name, adapterBinaryPrefix) {
				continue
			}

			// extract adapter name from binary name
			adapterName := strings.TrimPrefix(name, adapterBinaryPrefix)
			// on Windows, strip .exe suffix
			adapterName = strings.TrimSuffix(adapterName, ".exe")

			if adapterName == "" {
				continue
			}

			// skip if we already found this adapter in a higher-priority dir
			if seen[adapterName] {
				continue
			}

			binaryPath := filepath.Join(dir, name)

			// verify executable
			fi, err := os.Stat(binaryPath)
			if err != nil {
				slog.Debug("adapter binary stat failed", "path", binaryPath, "error", err)
				continue
			}
			if fi.Mode()&0111 == 0 {
				slog.Warn("adapter binary not executable", "path", binaryPath)
				continue
			}

			// call info to get metadata
			ea, err := NewExternalAdapter(binaryPath)
			if err != nil {
				slog.Warn("adapter info failed", "path", binaryPath, "error", err)
				continue
			}

			// validate protocol version
			if ea.info.ProtocolVersion < adapterprotocol.ProtocolVersion {
				slog.Warn("adapter protocol version too old",
					"adapter", ea.info.Name,
					"adapter_version", ea.info.ProtocolVersion,
					"minimum", adapterprotocol.ProtocolVersion,
				)
				continue
			}

			seen[adapterName] = true
			adapters = append(adapters, ea)
			slog.Debug("discovered external adapter",
				"name", ea.info.Name,
				"path", binaryPath,
				"type", ea.info.Type,
				"version", ea.info.Version,
			)
		}
	}

	return adapters
}

// RegisterExternalAdapters discovers and registers external adapters.
// External adapters with the same name as a built-in adapter take priority:
// the built-in is unregistered and the external adapter replaces it.
// This allows users to override built-in parsing logic by dropping a newer
// adapter binary into a controlled directory (see ADR-006 for allowed paths).
//
// Safe to call multiple times — rediscovery is idempotent since the same
// binary produces the same adapter name.
func RegisterExternalAdapters() error {
	adapters := DiscoverExternalAdapters()

	for _, ea := range adapters {
		name := ea.Name()

		// external takes priority over built-in: the external binary is presumably
		// a newer or more capable version of the same adapter.
		registryMu.Lock()
		if _, exists := registry[name]; exists {
			slog.Info("external adapter supersedes built-in",
				"name", name,
				"path", ea.BinaryPath(),
			)
			delete(registry, name)
		}
		registry[name] = ea
		registryMu.Unlock()
	}

	return nil
}

// ListExternalAdapters returns info about all discovered external adapters
// without registering them. Useful for `ox adapter list`.
func ListExternalAdapters() []ExternalAdapterInfo {
	adapters := DiscoverExternalAdapters()
	infos := make([]ExternalAdapterInfo, len(adapters))
	for i, ea := range adapters {
		infos[i] = ExternalAdapterInfo{
			Name:            ea.info.Name,
			DisplayName:     ea.info.DisplayName,
			Version:         ea.info.Version,
			Type:            ea.info.Type,
			BinaryPath:      ea.binaryPath,
			ProtocolVersion: ea.info.ProtocolVersion,
			Capabilities:    ea.info.Capabilities,
			ServeMode:       ea.info.ServeMode,
		}
	}
	return infos
}

// ExternalAdapterInfo is a summary of a discovered external adapter.
type ExternalAdapterInfo struct {
	Name            string   `json:"name"`
	DisplayName     string   `json:"display_name"`
	Version         string   `json:"version"`
	Type            string   `json:"type"`
	BinaryPath      string   `json:"binary_path"`
	ProtocolVersion int      `json:"protocol_version"`
	Capabilities    []string `json:"capabilities"`
	ServeMode       bool     `json:"serve_mode"`
}

// String implements fmt.Stringer.
func (i ExternalAdapterInfo) String() string {
	return fmt.Sprintf("%s v%s (%s) at %s", i.Name, i.Version, i.Type, i.BinaryPath)
}
