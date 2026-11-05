package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// InstallFromDir copies ox and every ox-adapter-* binary in srcDir into
// destDir, replacing any copies already there. It shares ReplaceRunningBinary's
// commit: adapters go first and ox last, and any failure rolls back every
// completed rename. It returns the names of the installed binaries.
func InstallFromDir(srcDir, destDir string) ([]string, error) {
	names, err := installSourceNames(srcDir)
	if err != nil {
		return nil, err
	}
	if err := checkWritable(destDir); err != nil {
		return nil, err
	}

	var staged []stagedBinary
	defer func() {
		for _, s := range staged {
			_ = os.Remove(s.tmpPath)
		}
	}()
	for _, name := range names {
		tmpPath, err := stageCopy(filepath.Join(srcDir, name), destDir)
		if err != nil {
			return nil, fmt.Errorf("stage %s: %w", name, err)
		}
		staged = append(staged, stagedBinary{destPath: filepath.Join(destDir, name), tmpPath: tmpPath, isOx: name == "ox"})
	}

	if runtime.GOOS == "darwin" {
		for _, s := range staged {
			codesignAdHoc(s.tmpPath)
		}
	}
	if err := commitStaged(orderAdaptersFirst(staged)); err != nil {
		return nil, err
	}
	return names, nil
}

// installSourceNames lists ox plus the ox-adapter-* regular files in srcDir.
func installSourceNames(srcDir string) ([]string, error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", srcDir, err)
	}
	var names []string
	hasOx := false
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || (name != "ox" && !strings.HasPrefix(name, "ox-adapter-")) {
			continue
		}
		if name == "ox" {
			hasOx = true
		}
		names = append(names, name)
	}
	if !hasOx {
		return nil, errors.New("no ox binary in " + srcDir)
	}
	return names, nil
}

func stageCopy(src, destDir string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(destDir, ".ox-install-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cherr := tmp.Chmod(0o755)
	clerr := tmp.Close()
	if err := errors.Join(werr, cherr, clerr); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}
