package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoctorRunsCursorHookRepairAsStructuredOxArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "argv.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CURSOR_FIX_ARGV_LOG\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "ox"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CURSOR_FIX_ARGV_LOG", logPath)

	issue := adapterprotocol.DiagnoseIssue{
		Slug:     "hooks-missing",
		Severity: "warning",
		Title:    "Cursor hooks are not installed",
		Detail:   "Install the project hooks.",
		Fix:      "ox integrate install --cursor",
		FixArgv:  []string{"ox", "integrate", "install", "--cursor"},
		FixSafe:  true,
	}
	require.NoError(t, runAdapterFix(issue, false))
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, "integrate\ninstall\n--cursor\n", string(data))
}

func TestDoctorDoesNotAutoRepairMalformedCursorHooks(t *testing.T) {
	issue := adapterprotocol.DiagnoseIssue{
		Slug:     "hooks-invalid",
		Severity: "error",
		Title:    "Cursor hooks configuration is invalid",
		Detail:   "The project hooks file is malformed.",
		Fix:      "ox integrate install --cursor",
	}

	err := runAdapterFix(issue, true)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "FixArgv") || strings.Contains(err.Error(), "display-only"))
}
