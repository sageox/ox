// Package filesmount discovers a locally mounted SageOx Files drive and maps
// teams to folders inside it.
//
// The mount is a macOS File Provider domain. macOS derives its path from the
// app and the domain display name — ~/Library/CloudStorage/SageOxFiles-<name> —
// so there is no fixed location to hard-code and the display name can change
// under us. Discovery therefore looks for a marker file rather than a path:
// a directory is a SageOx drive when it carries a readable ".sageox" naming
// this product, and the drive says which teams it holds and where.
//
// Nothing here writes. The provider refuses every write at the operating
// system boundary, and the git checkout under ~/.local/share/sageox remains
// the only writable copy.
package filesmount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// SentinelName is the marker file published at the root of a SageOx drive.
const SentinelName = ".sageox"

const (
	// productSageOxFiles is the value a SageOx Files drive publishes. Another
	// product's marker file, or a stray file of the same name, is not a mount.
	productSageOxFiles = "sageox-files"

	// supportedSentinelVersion is the highest schema this build understands.
	// The publisher bumps it only for a change a reader cannot absorb by
	// ignoring unknown fields, so a higher version means "written for a newer
	// ox" and is declined rather than guessed at.
	supportedSentinelVersion = 1

	// readTimeout bounds every filesystem call against the mount.
	//
	// Reads there are not local: touching a dataless file makes macOS call the
	// File Provider extension, which fetches from the Drive API. A signed-out
	// or offline mount can therefore block for as long as the network takes,
	// and no ox command should stall on an optional accelerator.
	readTimeout = 2 * time.Second
)

// Sentinel is the marker document's contents.
//
// Unknown fields are ignored on purpose: adding one must not require a new ox.
type Sentinel struct {
	SentinelVersion int    `json:"sentinel_version"`
	Product         string `json:"product"`
	ReadOnly        bool   `json:"read_only"`
	Teams           []Team `json:"teams"`
}

// Team locates one team's folder inside a mount.
//
// Path is relative to the mount root. Matching on TeamID rather than on
// DisplayName is the point: display names are prose, they collide, and they
// change without the team changing.
type Team struct {
	TeamID      string `json:"team_id"`
	DisplayName string `json:"display_name"`
	Path        string `json:"path"`
}

// Mount is a verified SageOx drive on this machine.
type Mount struct {
	// Root is the directory carrying the sentinel.
	Root     string
	Sentinel Sentinel
}

// TeamPath returns the absolute directory holding a team's context, and whether
// this mount carries that team at all.
//
// The returned path is confirmed to exist and to stay inside the mount, so a
// caller can read from it directly.
func (m Mount) TeamPath(teamID string) (string, bool) {
	if teamID == "" {
		return "", false
	}
	for _, team := range m.Sentinel.Teams {
		if team.TeamID != teamID {
			continue
		}
		resolved, err := resolveInside(m.Root, team.Path)
		if err != nil {
			return "", false
		}
		if !isDir(resolved) {
			return "", false
		}
		return resolved, true
	}
	return "", false
}

// Discover returns every SageOx drive mounted for the current user.
//
// A machine normally has zero or one. The result is a slice because nothing
// prevents a second domain, and silently picking one of two would be worse
// than letting the caller see both.
//
// Discovery never fails the caller: an unreadable candidate is skipped, since
// the mount is an accelerator and its absence is an ordinary state.
func Discover(ctx context.Context) []Mount {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var mounts []Mount
	seen := make(map[string]bool)
	for _, candidate := range candidateRoots(home) {
		// Resolve first: ~/SageOx is normally a symlink into CloudStorage, and
		// without this the same drive is reported twice.
		root, err := filepath.EvalSymlinks(candidate)
		if err != nil || seen[root] {
			continue
		}
		seen[root] = true
		sentinel, err := readSentinel(ctx, root)
		if err != nil {
			continue
		}
		mounts = append(mounts, Mount{Root: root, Sentinel: sentinel})
	}
	return mounts
}

// FindTeam returns the mounted folder for one team.
//
// This is the whole point of the package for most callers: given a team, either
// a directory to read from or a clear "not mounted".
func FindTeam(ctx context.Context, teamID string) (string, bool) {
	for _, mount := range Discover(ctx) {
		if path, ok := mount.TeamPath(teamID); ok {
			return path, true
		}
	}
	return "", false
}

// candidateRoots lists the places a drive can appear, most specific first.
//
// The convenience symlink comes first because it is one stat rather than a
// directory scan, and it is what the Files app maintains for exactly this use.
func candidateRoots(home string) []string {
	roots := []string{filepath.Join(home, "SageOx")}
	if runtime.GOOS != "darwin" {
		return roots
	}
	// macOS requires a File Provider to live under CloudStorage, but names the
	// directory after the app and domain display name, so the entry has to be
	// found rather than constructed.
	cloudStorage := filepath.Join(home, "Library", "CloudStorage")
	entries, err := os.ReadDir(cloudStorage)
	if err != nil {
		return roots
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			roots = append(roots, filepath.Join(cloudStorage, entry.Name()))
		}
	}
	return roots
}

var errNotAMount = errors.New("filesmount: not a SageOx drive")

func readSentinel(ctx context.Context, root string) (Sentinel, error) {
	raw, err := readFileWithin(ctx, filepath.Join(root, SentinelName), readTimeout)
	if err != nil {
		return Sentinel{}, err
	}
	var sentinel Sentinel
	if err := json.Unmarshal(raw, &sentinel); err != nil {
		return Sentinel{}, fmt.Errorf("filesmount: parse %s: %w", SentinelName, err)
	}
	if sentinel.Product != productSageOxFiles {
		return Sentinel{}, errNotAMount
	}
	if sentinel.SentinelVersion < 1 || sentinel.SentinelVersion > supportedSentinelVersion {
		return Sentinel{}, fmt.Errorf(
			"filesmount: sentinel version %d is not supported by this ox (understands up to %d)",
			sentinel.SentinelVersion, supportedSentinelVersion)
	}
	return sentinel, nil
}

// readFileWithin reads a file, giving up after d.
//
// os.ReadFile cannot be canceled, so the read runs on its own goroutine and
// the deadline abandons it. The goroutine ends when the syscall returns; the
// buffered channel keeps it from blocking on a send nobody is waiting for.
func readFileWithin(ctx context.Context, path string, d time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := os.ReadFile(path) //nolint:gosec // path is built from the user's own home directory
		done <- result{data: data, err: err}
	}()

	select {
	case r := <-done:
		return r.data, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("filesmount: reading %s: %w", path, ctx.Err())
	}
}

// resolveInside joins a server-supplied relative path onto the mount root and
// refuses anything that leaves it.
//
// The path comes from the drive, which is remote data, so it is not trusted to
// stay in bounds: "../.." or an absolute path would otherwise turn a team
// lookup into a read of somewhere else on disk.
func resolveInside(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("filesmount: team path %q is not relative to the mount", rel)
	}
	if strings.ContainsRune(rel, '\x00') {
		return "", errors.New("filesmount: team path contains a null byte")
	}
	joined := filepath.Join(root, rel)
	// filepath.Join cleans the result, so a traversal shows up as a path that
	// is no longer under root.
	if joined != root && !strings.HasPrefix(joined, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("filesmount: team path %q escapes the mount", rel)
	}
	return joined, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
