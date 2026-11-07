package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const teamCloneStagingMarkerSuffix = ".ox-staging"

// teamCloneTempPath names the staging directory a team context is cloned into
// before being moved to repoPath. It is a hidden sibling (".<id>.tmp-<ns>") so
// that scans of the teams directory, which treat every plain directory as a
// team, never see a half-built clone.
func teamCloneTempPath(repoPath string) string {
	return filepath.Join(filepath.Dir(repoPath),
		fmt.Sprintf(".%s.tmp-%d", filepath.Base(repoPath), time.Now().UnixNano()))
}

// markTeamCloneStaging records that tempPath is ox's own staging directory by
// creating a sibling marker file. The marker lives beside the directory, not
// inside it, because git refuses to clone into a non-empty directory. It is
// the proof sweepStaleTeamCloneTemps requires before deleting anything.
func markTeamCloneStaging(tempPath string) error {
	return os.WriteFile(tempPath+teamCloneStagingMarkerSuffix, []byte("ox team-context clone staging\n"), 0o600)
}

func unmarkTeamCloneStaging(tempPath string) {
	_ = os.Remove(tempPath + teamCloneStagingMarkerSuffix)
}

// isTeamCloneStagingName reports whether name is exactly
// ".<base>.tmp-<digits>", the format teamCloneTempPath generates. The base is
// matched literally, never as a glob.
func isTeamCloneStagingName(name, base string) bool {
	prefix := "." + base + ".tmp-"
	digits, ok := strings.CutPrefix(name, prefix)
	if !ok || digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sweepStaleTeamCloneTemps removes staging directories left by a clone that was
// killed mid-transfer. The caller holds the pre-clone lock for repoPath, so no
// live clone of this exact path can own one. A candidate is removed only when
// its name matches the generated format literally, it is a real directory (not
// a symlink), and ox's sibling marker file is present; anything else is left
// alone, because it may hold a coworker's files.
func sweepStaleTeamCloneTemps(repoPath string) {
	parent := filepath.Dir(repoPath)
	base := filepath.Base(repoPath)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !isTeamCloneStagingName(entry.Name(), base) {
			continue
		}
		dir := filepath.Join(parent, entry.Name())
		marker := dir + teamCloneStagingMarkerSuffix
		if fi, err := os.Lstat(marker); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			continue
		}
		_ = os.Remove(marker)
	}
}
