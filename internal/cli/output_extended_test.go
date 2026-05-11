package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSilent(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"silent error", ErrSilent, true},
		{"wrapped silent error", errors.Join(ErrSilent, errors.New("extra")), true},
		{"regular error", errors.New("something went wrong"), false},
		{"nil error", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsSilent(tt.err))
		})
	}
}

func TestSilentError_Error(t *testing.T) {
	assert.Equal(t, "", SilentError{}.Error())
}

func TestSetNoInteractive(t *testing.T) {
	// save and restore
	origVal := noInteractive
	defer func() { noInteractive = origVal }()

	SetNoInteractive(true)
	assert.True(t, noInteractive)

	SetNoInteractive(false)
	assert.False(t, noInteractive)
}

func TestIsInteractive_NoInteractiveFlag(t *testing.T) {
	origVal := noInteractive
	defer func() { noInteractive = origVal }()

	SetNoInteractive(true)
	assert.False(t, IsInteractive(), "should be non-interactive when flag is set")
}

func TestPrintSuccess_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintSuccess("all good")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Contains(t, buf.String(), "all good")
}

func TestPrintSuccess_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintSuccess("all good")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, `"status"`)
	assert.Contains(t, output, `"success"`)
	assert.Contains(t, output, `"all good"`)
}

func TestPrintError_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	PrintError("something broke")

	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Contains(t, buf.String(), "something broke")
}

func TestPrintError_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintError("something broke")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, `"error"`)
	assert.Contains(t, output, `"something broke"`)
}

func TestPrintWarning_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	PrintWarning("heads up")

	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Contains(t, buf.String(), "heads up")
}

func TestPrintWarning_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintWarning("heads up")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, `"warning"`)
	assert.Contains(t, output, `"heads up"`)
}

func TestPrintInfo_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintInfo("fyi")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Contains(t, buf.String(), "fyi")
}

func TestPrintInfo_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintInfo("fyi")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, `"info"`)
	assert.Contains(t, output, `"fyi"`)
}

func TestPrintPreserved_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintPreserved("kept as-is")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Contains(t, buf.String(), "kept as-is")
}

func TestPrintPreserved_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintPreserved("kept as-is")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, `"preserved"`)
	assert.Contains(t, output, `"kept as-is"`)
}

func TestPrintHint_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintHint("try this next")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Contains(t, buf.String(), "try this next")
}

func TestPrintHint_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintHint("try this next")

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	// hints are suppressed in JSON mode
	assert.Empty(t, buf.String())
}

func TestPrintActionHint_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintActionHint("ox login", "Authenticate with SageOx", 1)

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, "ox login")
	assert.Contains(t, output, "Authenticate with SageOx")
	assert.Contains(t, output, "Step 1")
}

func TestPrintActionHint_NoStep(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintActionHint("ox doctor", "Check setup", 0)

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, "ox doctor")
	assert.NotContains(t, output, "Step")
}

func TestPrintActionHint_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintActionHint("ox login", "Authenticate", 1)

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Empty(t, buf.String())
}

func TestPrintSuggestionBox_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	PrintSuggestionBox("Fix Required", "Something needs attention", "ox doctor --fix")

	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, "Fix Required")
	assert.Contains(t, output, "Something needs attention")
	assert.Contains(t, output, "ox doctor --fix")
}

func TestPrintSuggestionBox_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	PrintSuggestionBox("Title", "Message", "fix")

	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Empty(t, buf.String())
}

func TestPrintDisclaimer_TextMode(t *testing.T) {
	SetJSONMode(false)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintDisclaimer()

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, "Sage")
	assert.Contains(t, output, "Ox")
	assert.Contains(t, output, "Claude Code")
}

func TestPrintDisclaimer_JSONMode(t *testing.T) {
	SetJSONMode(true)
	defer SetJSONMode(false)

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintDisclaimer()

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.Empty(t, buf.String())
}

func TestPrintJSON(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintJSON(map[string]string{"key": "value"})

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	output := buf.String()
	assert.Contains(t, output, `"key"`)
	assert.Contains(t, output, `"value"`)
}

// =============================================================================
// Writer-aware *To variants — parallel-safe, no os.Stdout/Stderr mutation
// =============================================================================

func TestPrintSuccessTo_WritesToWriter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	PrintSuccessTo(&buf, "all good")
	assert.Contains(t, buf.String(), "all good")
}

func TestPrintErrorTo_WritesToWriter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	PrintErrorTo(&buf, "something broke")
	assert.Contains(t, buf.String(), "something broke")
}

func TestPrintWarningTo_WritesToWriter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	PrintWarningTo(&buf, "heads up")
	assert.Contains(t, buf.String(), "heads up")
}

func TestPrintInfoTo_WritesToWriter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	PrintInfoTo(&buf, "for your information")
	assert.Contains(t, buf.String(), "for your information")
}

func TestPrintPreservedTo_WritesToWriter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	PrintPreservedTo(&buf, "kept original")
	assert.Contains(t, buf.String(), "kept original")
}

func TestPrintHintTo_WritesToWriter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	PrintHintTo(&buf, "consider running ox doctor")
	assert.Contains(t, buf.String(), "consider running ox doctor")
}
