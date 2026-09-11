package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sageox/ox/internal/session/adapters"
)

// CheckSlugAdapterSiblings is the slug for the adapter-siblings check.
const CheckSlugAdapterSiblings = "adapter-siblings"

const adapterSiblingsCheckName = "Adapter binaries"

// expectedAdapterSiblings are the adapter binaries every official ox
// release ships (.config/goreleaser.yml). ox-adapter-test is deliberately
// excluded: it exists only for development/testing and is never shipped.
var expectedAdapterSiblings = []string{
	"aider", "amp", "claude-code", "codex", "droid",
	"gemini", "goose", "omp", "opencode", "pi",
}

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugAdapterSiblings,
		Name:     adapterSiblingsCheckName,
		Category: "Ecosystem",
		FixLevel: FixLevelCheckOnly,
		Description: "Verifies every adapter binary an official ox release ships is actually " +
			"discoverable next to the running ox binary -- unconditionally, not only once a " +
			"recording is already depending on one.",
		Run: func(fix bool) checkResult {
			return checkAdapterSiblings()
		},
	})
}

// checkAdapterSiblings answers "did the install actually put every adapter
// where ox can find it?" -- unconditionally. The only prior guard,
// checkRecordingAdapters (doctor_adapters.go), fires only once a recording
// is already using an adapter that turns out to be missing, by which point
// session hooks have already been silently no-oping. This check runs
// every time, before any recording starts.
func checkAdapterSiblings() checkResult {
	return adapterSiblingsResult(adapters.BundledAdapterDirs())
}

// adapterSiblingsResult is the pure, directory-driven core of
// checkAdapterSiblings, split out so tests can drive it with real temp
// directories instead of faking the process's own os.Executable() (which
// adapters.BundledAdapterDirs already has dedicated coverage for).
func adapterSiblingsResult(dirs []string) checkResult {
	if len(dirs) == 0 {
		return SkippedCheck(adapterSiblingsCheckName, "could not determine the ox binary's location", "")
	}

	found := make(map[string]bool, len(expectedAdapterSiblings))
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			// directory doesn't exist or is unreadable -- not fatal, the
			// other scanned dir(s) may still hold the siblings.
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "ox-adapter-") {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "ox-adapter-"), ".exe")
			if name == "" {
				continue
			}
			// A present-but-not-executable ox-adapter-* is invisible to the
			// real resolver (internal/session/adapters/discovery.go's own
			// executable-bit check) -- match it here so this check can't
			// report "present" for a binary session hooks would skip.
			path := filepath.Join(dir, e.Name())
			fi, err := os.Stat(path)
			if err != nil || fi.Mode()&0111 == 0 {
				continue
			}
			found[name] = true
		}
	}

	var missing []string
	for _, name := range expectedAdapterSiblings {
		if !found[name] {
			missing = append(missing, name)
		}
	}

	present := len(expectedAdapterSiblings) - len(missing)
	if len(missing) == 0 {
		return PassedCheck(adapterSiblingsCheckName, fmt.Sprintf("%d/%d present", present, len(expectedAdapterSiblings)))
	}

	sort.Strings(missing)
	detail := fmt.Sprintf(
		"missing: %s. Scanned: %s. If ox is reached through a symlink, every adapter must "+
			"live next to the real binary (or the symlink) -- not merely somewhere else on "+
			"PATH. Reinstall via brew or install.sh, which co-locate every adapter with ox.",
		strings.Join(missing, ", "), strings.Join(dirs, ", "),
	)
	return WarningCheck(adapterSiblingsCheckName,
		fmt.Sprintf("%d/%d present", present, len(expectedAdapterSiblings)),
		detail,
	)
}
