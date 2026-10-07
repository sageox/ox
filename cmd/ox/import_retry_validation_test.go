package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validationDocPath = "data/docs/2026/09/19/q3-plan"

// pendingValidationImport leaves a real import commit behind after a remote rejection.
func pendingValidationImport(t *testing.T) (*importRetryFixture, string) {
	t.Helper()
	if runtime.GOOS == "windows" || testing.Short() {
		t.Skip("POSIX shell hooks and real import recovery")
	}
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	f := newImportRetryFixture(t)
	tcPath := f.useFileGitRemote(t)
	runGit(t, tcPath, "branch", "-M", "main")
	runGit(t, tcPath, "push", "--set-upstream", "origin", "main")
	runGit(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/main")
	initial := runGit(t, f.bare, "rev-parse", "HEAD")
	f.text = filepath.Join(filepath.Dir(f.src), "extracted.md")
	require.NoError(t, os.WriteFile(f.text, []byte("extracted document text\n"), 0o644))
	hook := filepath.Join(f.bare, "hooks", "pre-receive")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 1\n"), 0o755))
	_, err := f.importDoc(false)
	require.ErrorContains(t, err, "git push failed")
	require.ErrorContains(t, err, "Permission denied")
	require.NotEqual(t, initial, runGit(t, tcPath, "rev-parse", "HEAD"))
	require.Equal(t, initial, runGit(t, f.bare, "rev-parse", "HEAD"))
	require.NoError(t, os.Remove(hook))
	return f, tcPath
}

// requireValidationRefusal checks ownership as well as the error: recovery must
// neither repair someone else's saved files nor publish or restage their state.
func requireValidationRefusal(t *testing.T, f *importRetryFixture, tcPath, wantErr string) {
	t.Helper()
	before := readDocFiles(t, f.docDir())
	source, err := os.ReadFile(f.src)
	require.NoError(t, err)
	text, err := os.ReadFile(f.text)
	require.NoError(t, err)
	head := runGit(t, tcPath, "rev-parse", "HEAD")
	index := runGit(t, tcPath, "ls-files", "--stage")
	remote := runGit(t, f.bare, "for-each-ref", "--format=%(refname) %(objectname)")
	uploads := f.uploads.Load()

	_, err = f.importDoc(false)
	require.ErrorContains(t, err, wantErr)
	assert.Equal(t, before, readDocFiles(t, f.docDir()))
	assert.Equal(t, head, runGit(t, tcPath, "rev-parse", "HEAD"))
	assert.Equal(t, index, runGit(t, tcPath, "ls-files", "--stage"))
	assert.Equal(t, remote, runGit(t, f.bare, "for-each-ref", "--format=%(refname) %(objectname)"))
	assert.Equal(t, uploads, f.uploads.Load(), "validation must precede another upload")
	afterSource, err := os.ReadFile(f.src)
	require.NoError(t, err)
	assert.Equal(t, source, afterSource)
	afterText, err := os.ReadFile(f.text)
	require.NoError(t, err)
	assert.Equal(t, text, afterText)
}

// A source OID match alone must never authorize publishing a damaged saved import.
func TestImport_RetryValidatesSavedManifest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr string
		mutate  func(*docMeta)
	}{
		{"unknown version", "manifest does not match", func(m *docMeta) { m.Version = "2" }},
		{"different document path", "manifest does not match", func(m *docMeta) { m.Path = "data/docs/another-document" }},
		{"wrong source size", "manifest does not match", func(m *docMeta) { m.SourceSize++ }},
		{"duplicate sidecar filename", "repeats a pointer filename", func(m *docMeta) {
			m.Sidecars["duplicate"] = m.Sidecars["text-extract"]
		}},
		{"sidecar aliases source", "repeats a pointer filename", func(m *docMeta) {
			m.Sidecars["duplicate"] = sidecar{Filename: m.SourceFilename, OID: m.SourceOID, Size: m.SourceSize}
		}},
		{"invalid sidecar object ID", "pointer does not match its manifest", func(m *docMeta) {
			s := m.Sidecars["text-extract"]
			s.OID = "sha256:not-a-valid-object-id"
			m.Sidecars["text-extract"] = s
		}},
		{"negative sidecar size", "pointer does not match its manifest", func(m *docMeta) {
			s := m.Sidecars["text-extract"]
			s.Size = -1
			m.Sidecars["text-extract"] = s
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, tcPath := pendingValidationImport(t)
			manifestPath := filepath.Join(f.docDir(), "metadata.json")
			data, err := os.ReadFile(manifestPath)
			require.NoError(t, err)
			var meta docMeta
			require.NoError(t, json.Unmarshal(data, &meta))
			tc.mutate(&meta)
			data, err = json.Marshal(meta)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(manifestPath, data, 0o644))
			requireValidationRefusal(t, f, tcPath, tc.wantErr)
		})
	}
}

func TestImport_RetryRejectsChangedSavedPointer(t *testing.T) {
	f, tcPath := pendingValidationImport(t)
	other := lfs.NewFileRef([]byte("a different document\n"))
	require.NoError(t, os.WriteFile(filepath.Join(f.docDir(), "q3-plan.md"), []byte(lfs.FormatPointer(other.OID, other.Size)), 0o644))
	requireValidationRefusal(t, f, tcPath, "pointer does not match its manifest")
}

func TestImport_RetryRejectsPartiallyCommittedSavedDocument(t *testing.T) {
	f, tcPath := pendingValidationImport(t)
	// an interrupted manual repair committed only part of the document
	runGit(t, tcPath, "rm", "--cached", "--", validationDocPath+"/metadata.json")
	runGit(t, tcPath, "commit", "--amend", "--no-verify", "--no-edit")
	require.FileExists(t, filepath.Join(f.docDir(), "metadata.json"))
	requireValidationRefusal(t, f, tcPath, "document is only partially committed")
}

// Another writer's publication at this document path must not be overwritten
// or treated as confirmation that the local saved import was published.
func TestImport_RetryRejectsConflictingRemoteDocument(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr string
		pointer bool
	}{
		{"different pointer", "published document differs", true},
		{"metadata only", "published document is incomplete", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, tcPath := pendingValidationImport(t)
			writer := filepath.Join(t.TempDir(), "writer")
			runGit(t, filepath.Dir(writer), "clone", f.bare, writer)
			runGit(t, writer, "config", "user.name", "Test")
			runGit(t, writer, "config", "user.email", "test@example.com")
			docDir := filepath.Join(writer, filepath.FromSlash(validationDocPath))
			require.NoError(t, os.MkdirAll(docDir, 0o755))
			files := readDocFiles(t, f.docDir())
			require.NoError(t, os.WriteFile(filepath.Join(docDir, "metadata.json"), []byte(files["metadata.json"]), 0o644))
			if tc.pointer {
				for name, content := range files {
					if name == "q3-plan.md" {
						other := lfs.NewFileRef([]byte("another writer's document\n"))
						content = lfs.FormatPointer(other.OID, other.Size)
					}
					require.NoError(t, os.WriteFile(filepath.Join(docDir, name), []byte(content), 0o644))
				}
			}
			runGit(t, writer, "add", "--", validationDocPath)
			runGit(t, writer, "commit", "--no-verify", "-m", "publish another document state")
			runGit(t, writer, "push", "origin", "main")
			runGit(t, tcPath, "fetch", "origin")
			requireValidationRefusal(t, f, tcPath, tc.wantErr)
		})
	}
}

func TestImport_RetryRejectsUnsafeUnpublishedPushConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr string
		args    []string
	}{
		{"matching branches", "push.default=matching", []string{"push.default", "matching"}},
		{"explicit refspec", "explicit push refspec", []string{"remote.origin.push", "refs/heads/main:refs/heads/main"}},
		{"mirror remote", "mirror remote", []string{"remote.origin.mirror", "true"}},
		{"invalid mirror boolean", "invalid-boolean", []string{"remote.origin.mirror", "invalid-boolean"}},
		{"multiple push URLs", "multiple remote URLs", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, tcPath := pendingValidationImport(t)
			if tc.args == nil {
				runGit(t, tcPath, "config", "--add", "remote.origin.pushurl", f.bare)
				runGit(t, tcPath, "config", "--add", "remote.origin.pushurl", f.bare)
			} else {
				runGit(t, tcPath, append([]string{"config", "--local"}, tc.args...)...)
			}
			requireValidationRefusal(t, f, tcPath, tc.wantErr)
		})
	}
}

// An unreadable Git result is uncertainty, never evidence that publication is safe.
// Each shim fails one actual inspection in an otherwise real import recovery.
func TestImport_RetryRefusesUncertainRepositoryState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pattern    string
		wantErr    string
		occurrence int
	}{
		{"branch identity", "symbolic-ref --quiet HEAD", "cannot determine import publication", 1},
		{"push remote", "for-each-ref --format=%(push:remotename) *", "cannot determine import publication remote", 1},
		{"push tracking ref", "rev-parse --symbolic-full-name @{push}", "cannot determine import publication ref", 1},
		{"push refspec configuration", "config --get-all remote.origin.push", "publication configuration", 1},
		{"mirror configuration", "config --bool --get remote.origin.mirror", "publication configuration", 1},
		{"push mode configuration", "config --get push.default", "publication configuration", 1},
		{"remote transport", "remote get-url --all origin", "cannot determine import publication URL", 1},
		{"fork point", "merge-base --fork-point *", "safe import retry history", 1},
		{"outgoing ancestry", "rev-list --parents --max-count=2 *", "safe import retry history", 1},
		{"commit ownership", "diff --name-only --no-renames -z *", "inspect import commit", 1},
		{"committed file mode", "ls-tree --format=%(objectmode) *", "inspect import commit file mode", 1},
		{"Git clean content", "hash-object --stdin *", "compare import content using Git", 1},
		{"saved file tree", "ls-tree -z HEAD *", "inspect import Git state", 1},
		{"saved file blob", "show HEAD:data/docs/*", "read import Git blob", 1},
		{"committed metadata comparison", "hash-object --stdin --path=data/docs/*/metadata.json", "compare import content using Git", 1},
		{"staged metadata comparison", "hash-object --stdin --path=data/docs/*/metadata.json", "compare import content using Git", 2},
		{"staged file tree", "ls-files --stage -z -- *", "inspect import Git state", 1},
		{"staged file blob", "show :data/docs/*", "read import Git blob", 1},
		{"remote document tree", "ls-tree -z refs/remotes/origin/*", "cannot determine import publication", 1},
		{"fork point document tree", "ls-tree -z FORK -- *data/docs/*", "safe import retry history", 1},
		{"parent attributes tree", "ls-tree -z * -- :(literal).gitattributes", "inspect import Git state", 1},
		{"committed attributes tree", "ls-tree -z * -- :(literal).gitattributes", "inspect import Git state", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, tcPath := pendingValidationImport(t)
			realGit, err := exec.LookPath("git")
			require.NoError(t, err)
			realGit, err = filepath.Abs(realGit)
			require.NoError(t, err)
			binDir := t.TempDir()
			marker := filepath.Join(binDir, "failed-inspection")
			t.Setenv("TEST_VALIDATION_REAL_GIT", realGit)
			t.Setenv("TEST_VALIDATION_REPO", tcPath)
			t.Setenv("TEST_VALIDATION_MARKER", marker)
			fork := runGit(t, tcPath, "merge-base", "--fork-point", "@{push}", "HEAD")
			t.Setenv("TEST_VALIDATION_PATTERN", strings.ReplaceAll(tc.pattern, "FORK", fork))
			t.Setenv("TEST_VALIDATION_OCCURRENCE", strconv.Itoa(tc.occurrence))
			// Strip invocation options only while inspecting; the real git receives
			// the original arguments. Exit 2 distinguishes failure from absent config.
			script := `#!/bin/sh
set -eu
should_fail() {
    repo=''
    while [ "$#" -ge 2 ]; do
        case "$1" in
            -C) repo="$2"; shift 2 ;;
            -c) shift 2 ;;
            *) break ;;
        esac
    done
    [ "$repo" = "$TEST_VALIDATION_REPO" ] || return 1
    case "$*" in
        $TEST_VALIDATION_PATTERN) return 0 ;;
        *) return 1 ;;
    esac
}
if [ ! -e "$TEST_VALIDATION_MARKER" ] && should_fail "$@"; then
    count=0
    if [ -e "$TEST_VALIDATION_MARKER.count" ]; then
        count=$(cat "$TEST_VALIDATION_MARKER.count")
    fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$TEST_VALIDATION_MARKER.count"
    if [ "$count" -eq "$TEST_VALIDATION_OCCURRENCE" ]; then
        printf '%s\n' "$*" > "$TEST_VALIDATION_MARKER"
        echo 'injected repository inspection failure' >&2
        exit 2
    fi
fi
exec "$TEST_VALIDATION_REAL_GIT" "$@"
`
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			requireValidationRefusal(t, f, tcPath, tc.wantErr)
			require.FileExists(t, marker, "the real recovery must reach the failing inspection")
		})
	}
}
