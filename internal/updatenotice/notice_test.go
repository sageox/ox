package updatenotice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useTempCache redirects Path at a temp file so no test touches the real
// ~/.cache/sageox/version-check.json.
func useTempCache(t *testing.T) {
	t.Helper()
	old := Path
	Path = filepath.Join(t.TempDir(), "version-check.json")
	t.Cleanup(func() { Path = old })
}

// forceTTY makes Suppressed() behave as if a human is watching stderr, which is
// never true under `go test`.
func forceTTY(t *testing.T, tty bool) {
	t.Helper()
	old := StderrIsTTY
	StderrIsTTY = func() bool { return tty }
	t.Cleanup(func() { StderrIsTTY = old })
}

func TestReleaseLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain release", in: "0.16.3", want: "0.16"},
		{name: "v prefix", in: "v0.16.3", want: "0.16"},
		{name: "double digit minor", in: "0.144.0", want: "0.144"},
		{name: "prerelease stripped", in: "0.16.0-rc1", want: "0.16"},
		{name: "build metadata stripped", in: "0.16.2+abc123", want: "0.16"},
		{name: "local dev build", in: "0.14.3-dev+2026-08-31T12:00:00Z", want: "0.14"},
		{name: "major.minor only", in: "1.0", want: "1.0"},
		{name: "leading zeros normalized", in: "0.09.0", want: "0.9"},
		{name: "empty", in: "", want: ""},
		{name: "dev sentinel", in: "dev", want: ""},
		{name: "no minor", in: "1", want: ""},
		{name: "non numeric minor", in: "0.x.1", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ReleaseLine(tt.in))
		})
	}
}

func TestShouldNotify(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		data *Data
		line string
		want bool
	}{
		{name: "no cache at all speaks", data: nil, line: "0.16", want: true},
		{name: "unparseable line stays quiet", data: nil, line: "", want: false},
		{name: "first sight of a line speaks", data: &Data{}, line: "0.16", want: true},
		{
			name: "same line inside the cap stays quiet",
			data: &Data{LastNaggedLine: "0.16", LastNaggedAt: now.Add(-1 * time.Hour)},
			line: "0.16", want: false,
		},
		{
			name: "same line one minute short of the cap stays quiet",
			data: &Data{LastNaggedLine: "0.16", LastNaggedAt: now.Add(-NotifyInterval + time.Minute)},
			line: "0.16", want: false,
		},
		{
			name: "same line exactly at the cap speaks",
			data: &Data{LastNaggedLine: "0.16", LastNaggedAt: now.Add(-NotifyInterval)},
			line: "0.16", want: true,
		},
		{
			name: "new release line resets the cap immediately",
			data: &Data{LastNaggedLine: "0.15", LastNaggedAt: now.Add(-1 * time.Minute)},
			line: "0.16", want: true,
		},
		{
			name: "stamp in the future is treated as never stamped",
			data: &Data{LastNaggedLine: "0.16", LastNaggedAt: now.Add(72 * time.Hour)},
			line: "0.16", want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ShouldNotify(tt.data, tt.line, now))
		})
	}
}

// The ledger has to survive the process that wrote it — that is the entire
// point. Asserts the on-disk key names too, since they are the contract shared
// with the daemon's writer.
func TestLedgerPersistsAcrossProcesses(t *testing.T) {
	useTempCache(t)
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

	require.NoError(t, Write(&Data{LatestVersion: "v0.16.0", CheckedAt: now, ETag: `"e"`}))
	RecordNotified("0.16", now)

	raw, err := os.ReadFile(Path)
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	assert.Equal(t, "0.16", onDisk["last_nagged_line"])
	assert.Equal(t, "2026-08-31T12:00:00Z", onDisk["last_nagged_at"], "must be RFC 3339")

	// a second "process" reads the ledger back and stays quiet
	got := Read()
	require.NotNil(t, got)
	assert.Equal(t, "0.16", got.LastNaggedLine)
	assert.Equal(t, "v0.16.0", got.LatestVersion, "stamping must not clobber the version fields")
	assert.Equal(t, `"e"`, got.ETag, "stamping must not clobber the ETag")
	assert.False(t, ShouldNotify(got, "0.16", now.Add(time.Hour)))
	assert.True(t, ShouldNotify(got, "0.16", now.Add(25*time.Hour)))
}

// A fresh cache has no ledger fields on disk at all — the zero time must not
// serialize as a bogus year-1 date that a human reading the file would trip on.
func TestWrite_OmitsEmptyLedger(t *testing.T) {
	useTempCache(t)
	require.NoError(t, Write(&Data{LatestVersion: "v0.16.0", CheckedAt: time.Now()}))

	raw, err := os.ReadFile(Path)
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(raw, &onDisk))
	assert.NotContains(t, onDisk, "last_nagged_line")
	assert.NotContains(t, onDisk, "last_nagged_at")
}

// A successful `ox upgrade` must forget both ledger fields, so the FIRST notice
// about the next release line is not swallowed by a stale cap.
func TestReset_ClearsLedgerSoNextLineSpeaks(t *testing.T) {
	useTempCache(t)
	now := time.Now()
	RecordNotified("0.16", now)
	require.False(t, ShouldNotify(Read(), "0.16", now.Add(time.Hour)))

	Reset()

	assert.Nil(t, Read(), "upgrade must drop the cache entirely, ledger included")
	assert.True(t, ShouldNotify(Read(), "0.16", now.Add(time.Hour)))
}

// The daemon and `ox doctor` both rebuild Data from a fresh GitHub check. If
// they save it without carrying the ledger, every version refresh restores the
// per-command nag.
func TestCarryLedger(t *testing.T) {
	t.Parallel()
	stamped := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	src := &Data{LatestVersion: "v0.15.0", LastNaggedLine: "0.16", LastNaggedAt: stamped}
	dst := &Data{LatestVersion: "v0.16.0", CheckedAt: stamped}

	CarryLedger(dst, src)

	assert.Equal(t, "0.16", dst.LastNaggedLine)
	assert.Equal(t, stamped, dst.LastNaggedAt)
	assert.Equal(t, "v0.16.0", dst.LatestVersion, "carrying the ledger must not touch version fields")

	// nil-safe: callers pass whatever Read() gave them
	CarryLedger(dst, nil)
	CarryLedger(nil, src)
	assert.Equal(t, "0.16", dst.LastNaggedLine)
}

func TestRecordNotified_IgnoresUnparseableLine(t *testing.T) {
	useTempCache(t)
	RecordNotified("", time.Now())
	assert.Nil(t, Read(), "an unknown release line must not create a ledger entry")
}

func TestLine(t *testing.T) {
	useTempCache(t)

	// no cache yet: fall back to the running binary's own line
	assert.Equal(t, "0.14", Line("0.14.3"))

	// cache knows a newer line: both tiers key on it
	require.NoError(t, Write(&Data{LatestVersion: "v0.16.0", CheckedAt: time.Now()}))
	assert.Equal(t, "0.16", Line("0.14.3"))

	// unparseable cached version falls back rather than returning ""
	require.NoError(t, Write(&Data{LatestVersion: "garbage", CheckedAt: time.Now()}))
	assert.Equal(t, "0.14", Line("0.14.3"))
}

// Notices must never reach an agent transcript or a machine-output stream.
func TestSuppressed(t *testing.T) {
	tests := []struct {
		name          string
		tty           bool
		machineOutput bool
		want          bool
	}{
		{name: "human at a terminal speaks", tty: true, machineOutput: false, want: false},
		{name: "stderr captured by an agent stays silent", tty: false, machineOutput: false, want: true},
		{name: "json mode stays silent even on a tty", tty: true, machineOutput: true, want: true},
		{name: "json mode and no tty stays silent", tty: false, machineOutput: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			forceTTY(t, tt.tty)
			SetMachineOutput(tt.machineOutput)
			t.Cleanup(func() { SetMachineOutput(false) })
			assert.Equal(t, tt.want, Suppressed())
		})
	}
}
