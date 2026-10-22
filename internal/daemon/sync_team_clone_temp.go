package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// teamCloneTempPath names the staging directory a team context is cloned into
// before being moved to repoPath. It is a hidden sibling (".<id>.tmp-<ns>") so
// that scans of the teams directory, which treat every plain directory as a
// team, never see a half-built clone.
func teamCloneTempPath(repoPath string) string {
	return filepath.Join(filepath.Dir(repoPath),
		fmt.Sprintf(".%s.tmp-%d", filepath.Base(repoPath), time.Now().UnixNano()))
}

// sweepStaleTeamCloneTemps removes staging directories left by a clone that was
// killed mid-transfer. The caller holds the pre-clone lock for repoPath, so no
// live clone of this path can own one.
func sweepStaleTeamCloneTemps(repoPath string) {
	pattern := filepath.Join(filepath.Dir(repoPath), fmt.Sprintf(".%s.tmp-*", filepath.Base(repoPath)))
	stale, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, dir := range stale {
		_ = os.RemoveAll(dir)
	}
}
