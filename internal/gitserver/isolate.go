package gitserver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
)

// IsolateManagedClones makes every git that this process starts from now on,
// when it runs in a clone under paths.DataDir() (a Ledger, team context, or
// Knowledge Bubble), run no hooks and get credentials only from ox's helper.
//
// It writes those settings to a file and adds an includeIf entry for it to the
// process environment (GIT_CONFIG_COUNT). Git reads environment config after a
// clone's .git/config, so the settings win over anything the clone, the home's
// global config, or the system config set; and every git that ox starts
// inherits them, whichever code builds the command. A clone outlives a
// disposable machine, so its .git/config cannot be trusted to keep them.
func IsolateManagedClones() error {
	helper := DefaultHelperCommand()
	if strings.ContainsAny(helper, "\n\r") {
		return fmt.Errorf("credential helper command spans lines: %q", helper)
	}
	// Git matches gitdir patterns against the clone's real path.
	dataDir := paths.DataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	dataDir, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return err
	}

	// Rewritten on every start: the cache dir is on the home, so a previous
	// machine may have changed the file.
	file := filepath.Join(paths.CacheDir(), "git", "managed-clones.gitconfig")
	content := []byte("[core]\n\thooksPath = /dev/null\n" +
		"[credential]\n\thelper =\n\thelper = " + gitConfigQuote(helper) + "\n")
	if current, err := os.ReadFile(file); err != nil || !bytes.Equal(current, content) {
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return err
		}
		if err := fileutil.AtomicWriteBytes(file, content, 0o600); err != nil {
			return err
		}
	}

	key := "includeIf.gitdir:" + filepath.ToSlash(dataDir) + "/.path"
	n := 0
	if v := os.Getenv("GIT_CONFIG_COUNT"); v != "" {
		if n, err = strconv.Atoi(v); err != nil || n < 0 {
			return fmt.Errorf("GIT_CONFIG_COUNT is %q, not a count", v)
		}
	}
	for i := range n {
		if os.Getenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i)) == key && os.Getenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)) == file {
			return nil // inherited from the ox process that started this one
		}
	}
	if err := os.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", n), key); err != nil {
		return err
	}
	if err := os.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", n), file); err != nil {
		return err
	}
	return os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(n+1))
}

// gitConfigQuote quotes v as a git config file value, so '#' and ';' in it
// do not start a comment.
func gitConfigQuote(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}
