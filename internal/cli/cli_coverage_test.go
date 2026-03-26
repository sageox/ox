package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithSpinner_NonInteractive_Success(t *testing.T) {
	origVal := noInteractive
	defer func() { noInteractive = origVal }()
	SetNoInteractive(true)

	val, err := WithSpinner("loading...", func() (string, error) {
		return "result", nil
	})

	assert.NoError(t, err)
	assert.Equal(t, "result", val)
}

func TestWithSpinner_NonInteractive_Error(t *testing.T) {
	origVal := noInteractive
	defer func() { noInteractive = origVal }()
	SetNoInteractive(true)

	val, err := WithSpinner("loading...", func() (int, error) {
		return 0, errors.New("boom")
	})

	assert.Error(t, err)
	assert.Equal(t, "boom", err.Error())
	assert.Equal(t, 0, val)
}

func TestWithSpinnerNoResult_NonInteractive_Success(t *testing.T) {
	origVal := noInteractive
	defer func() { noInteractive = origVal }()
	SetNoInteractive(true)

	err := WithSpinnerNoResult("syncing...", func() error {
		return nil
	})

	assert.NoError(t, err)
}

func TestWithSpinnerNoResult_NonInteractive_Error(t *testing.T) {
	origVal := noInteractive
	defer func() { noInteractive = origVal }()
	SetNoInteractive(true)

	err := WithSpinnerNoResult("syncing...", func() error {
		return errors.New("sync failed")
	})

	assert.Error(t, err)
	assert.Equal(t, "sync failed", err.Error())
}

func TestTrackCommandCompletion_WithTelemetry(t *testing.T) {
	client := telemetry.NewClient("test-session", telemetry.WithEnabled(true))

	ctx := &Context{
		Config:           &config.Config{},
		TelemetryClient:  client,
		CommandStartTime: time.Now().Add(-100 * time.Millisecond),
	}

	// root-level command (parent is "ox")
	rootCmd := &cobra.Command{Use: "ox"}
	childCmd := &cobra.Command{Use: "status"}
	rootCmd.AddCommand(childCmd)

	assert.NotPanics(t, func() { ctx.TrackCommandCompletion(childCmd) })
}

func TestTrackCommandCompletion_NestedCommand(t *testing.T) {
	client := telemetry.NewClient("test-session", telemetry.WithEnabled(true))

	ctx := &Context{
		Config:           &config.Config{},
		TelemetryClient:  client,
		CommandStartTime: time.Now().Add(-50 * time.Millisecond),
	}

	// nested command: "agent prime" (parent is "agent", not "ox")
	rootCmd := &cobra.Command{Use: "ox"}
	agentCmd := &cobra.Command{Use: "agent"}
	primeCmd := &cobra.Command{Use: "prime"}
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(primeCmd)

	assert.NotPanics(t, func() { ctx.TrackCommandCompletion(primeCmd) })
}

func TestTrackCommandError_WithTelemetry(t *testing.T) {
	client := telemetry.NewClient("test-session", telemetry.WithEnabled(true))

	ctx := &Context{
		Config:           &config.Config{},
		TelemetryClient:  client,
		CommandStartTime: time.Now().Add(-100 * time.Millisecond),
	}

	rootCmd := &cobra.Command{Use: "ox"}
	childCmd := &cobra.Command{Use: "init"}
	rootCmd.AddCommand(childCmd)

	assert.NotPanics(t, func() {
		ctx.TrackCommandError(childCmd, errors.New("config missing"))
	})
}

func TestTrackCommandError_LongErrorTruncated(t *testing.T) {
	client := telemetry.NewClient("test-session", telemetry.WithEnabled(true))

	ctx := &Context{
		Config:           &config.Config{},
		TelemetryClient:  client,
		CommandStartTime: time.Now(),
	}

	rootCmd := &cobra.Command{Use: "ox"}
	cmd := &cobra.Command{Use: "sync"}
	rootCmd.AddCommand(cmd)

	// error message longer than 50 chars should be truncated in the error code
	longErr := errors.New("this is a very long error message that exceeds the fifty character limit for error codes")
	assert.NotPanics(t, func() { ctx.TrackCommandError(cmd, longErr) })
}

func TestTrackCommandError_NilError(t *testing.T) {
	client := telemetry.NewClient("test-session", telemetry.WithEnabled(true))

	ctx := &Context{
		Config:           &config.Config{},
		TelemetryClient:  client,
		CommandStartTime: time.Now(),
	}

	rootCmd := &cobra.Command{Use: "ox"}
	cmd := &cobra.Command{Use: "login"}
	rootCmd.AddCommand(cmd)

	// nil error should still use ERR_UNKNOWN code path
	assert.NotPanics(t, func() { ctx.TrackCommandError(cmd, nil) })
}

func TestOpenInBrowser_SkipBrowserNotSet(t *testing.T) {
	// when SKIP_BROWSER is not "1", and we're headless, expect ErrHeadless
	t.Setenv("SKIP_BROWSER", "0")
	t.Setenv("SSH_CLIENT", "10.0.0.1 12345 22")

	err := OpenInBrowser("https://example.com")
	assert.ErrorIs(t, err, ErrHeadless)
}

func TestSuggestionBox_EmptyFix(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	result := SuggestionBox("Warning", "Check this out", "")
	assert.Contains(t, result, "Warning")
	assert.Contains(t, result, "Check this out")
	assert.NotContains(t, result, "Run:")
}

func TestSuggestionBox_WithFix(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	result := SuggestionBox("Warning", "Check this out", "ox doctor --fix")
	assert.Contains(t, result, "Run:")
	assert.Contains(t, result, "ox doctor --fix")
}

func TestSuggestionBox_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	result := SuggestionBox("Warning", "Check this out", "ox doctor --fix")
	assert.Empty(t, result)
}

func TestFormatTipText_HighlightsCommands(t *testing.T) {
	result := FormatTipText("Run `ox login` to authenticate")
	// backtick-wrapped text should be transformed (no longer contains backticks)
	assert.NotContains(t, result, "`ox login`")
	assert.Contains(t, result, "ox login")
}

func TestFormatTipText_NoBackticks(t *testing.T) {
	result := FormatTipText("No commands here")
	assert.Equal(t, "No commands here", result)
}

func TestPrintTip_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	PrintTip("Run `ox doctor` to check setup")

	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Contains(t, buf.String(), "ox doctor")
}

func TestPrintTip_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	PrintTip("Run `ox doctor` to check setup")

	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Empty(t, buf.String())
}

func TestSetJSONMode(t *testing.T) {
	origVal := jsonMode
	defer func() { jsonMode = origVal }()

	SetJSONMode(true)
	assert.True(t, jsonMode)

	SetJSONMode(false)
	assert.False(t, jsonMode)
}
