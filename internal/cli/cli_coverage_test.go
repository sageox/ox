package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
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

// A dismissed spinner must not return an empty successful result. Hold the
// operation until the real terminal displays the spinner, then finish or cancel.
func TestWithSpinnerTerminalResults(t *testing.T) {
	if testing.Short() {
		t.Skip("short: waits for the spinner's display delay in a real terminal")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("pseudo-terminal integration is supported on macOS and Linux")
	}
	t.Setenv("TERM", "xterm-256color")
	opErr := errors.New("operation failed")
	for _, tt := range []struct {
		name      string
		interrupt bool
		noResult  bool
		fast      bool
		err       error
	}{
		{name: "completed result"},
		{name: "operation error", err: opErr},
		{name: "Ctrl+C", interrupt: true},
		{name: "Ctrl+C without result", interrupt: true, noResult: true},
		{name: "fast result", fast: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ptmx, tty, err := pty.Open()
			require.NoError(t, err)
			defer ptmx.Close()
			defer tty.Close()
			require.NoError(t, pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 100}))
			stdin, stdout, stderr, disabled := os.Stdin, os.Stdout, os.Stderr, noInteractive
			os.Stdin, os.Stdout, os.Stderr, noInteractive = tty, tty, tty, false
			defer func() { os.Stdin, os.Stdout, os.Stderr, noInteractive = stdin, stdout, stderr, disabled }()
			require.True(t, IsInteractive(), "must exercise the interactive path")

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			release := make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			defer finish()
			finished := make(chan struct{})
			op := func() (string, error) {
				defer close(finished)
				if !tt.fast {
					select {
					case <-release:
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}
				return "completed value", tt.err
			}

			const message = "Waiting for test operation"
			var output bytes.Buffer
			readDone := make(chan error, 1)
			go func() {
				buf := make([]byte, 4096)
				answered, acted := false, false
				for {
					n, readErr := ptmx.Read(buf)
					output.Write(buf[:n])
					if !answered && bytes.Contains(output.Bytes(), []byte(ansi.RequestPrimaryDeviceAttributes)) {
						_, _ = ptmx.Write([]byte("\x1b]11;rgb:0000/0000/0000\a\x1b[?1;2c"))
						answered = true
					}
					if !acted && bytes.Contains(output.Bytes(), []byte(message)) {
						if tt.interrupt {
							_, _ = ptmx.Write([]byte{3}) // Ctrl+C in raw terminal mode
						} else {
							finish()
						}
						acted = true
					}
					if readErr != nil {
						readDone <- readErr
						return
					}
				}
			}()

			var value string
			if tt.noResult {
				err = WithSpinnerNoResult(message, func() error { _, err := op(); return err })
			} else {
				value, err = WithSpinner(message, op)
			}
			os.Stdin, os.Stdout, os.Stderr, noInteractive = stdin, stdout, stderr, disabled
			require.NoError(t, tty.Close())
			readErr := <-readDone
			require.True(t, errors.Is(readErr, io.EOF) || errors.Is(readErr, syscall.EIO), "terminal: %v", readErr)
			require.NoError(t, ctx.Err(), "spinner failed to return promptly: %s", output.String())
			if tt.interrupt {
				assert.ErrorIs(t, err, tea.ErrInterrupted)
				assert.Empty(t, value)
				select {
				case <-finished:
					t.Error("interrupted wait must return before the operation finishes")
				default:
				}
			} else {
				assert.ErrorIs(t, err, tt.err)
				assert.Equal(t, "completed value", value)
			}
			finish()
			<-finished
		})
	}
}

// The quit command is asynchronous: a late result must not replace a Ctrl+C
// already handled by the model, and late input must not discard a completed result.
func TestSpinnerKeepsFirstTerminalOutcome(t *testing.T) {
	interrupt := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	result := SpinnerResult[string]{Value: "completed"}
	for _, tt := range []struct {
		name  string
		msgs  []tea.Msg
		value string
		err   error
	}{
		{name: "result arrives after Ctrl+C", msgs: []tea.Msg{interrupt, result}, err: tea.ErrInterrupted},
		{name: "Ctrl+C arrives after result", msgs: []tea.Msg{result, interrupt}, value: "completed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var model tea.Model = newSpinnerModel[string]("working")
			for _, msg := range tt.msgs {
				model, _ = model.Update(msg)
			}
			final := model.(spinnerModel[string])
			assert.True(t, final.done)
			assert.Equal(t, tt.value, final.output.Value)
			assert.ErrorIs(t, final.output.Err, tt.err)
		})
	}
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
